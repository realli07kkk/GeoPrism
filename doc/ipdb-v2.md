# IPDB v2 存储设计与协议

本文是 `backend/ipdb` 的权威设计文档：v2 key / value 编码、`SchemaFeatures`、构建与原子发布、`OpenCurrent` 判版、最长前缀匹配（LPM）与三段 CIDR 查询、文件锁、overlay 缓存、磁盘布局与不变量。调用方视角（单 IP / CIDR 流程、ipinfo 合并）见 [ip-lookup.md](ip-lookup.md)；决策原因见 [decisions.md](decisions.md)。

<a id="scope"></a>
## 1. 定位与范围

- **base 库不可变**：由 CSV 构建、ReadOnly 打开、运行期永不写入（D-01）。
- **overlay 物理独立**：在线结果只能进 `overlay/`，与 base 版本目录互不影响；跨 base 重建存活。
- **正确性目标**：单 IP 返回真正的最长前缀（允许网段重叠）；CIDR 返回所有相交网段（含多层祖先与后代）。
- **明确不做**：多进程共享同一 Pebble 目录、overlay 容量 / LRU 回收（只有 TTL + 机会性删除）、v1 → v2 数据级迁移（D-12）。

<a id="status"></a>
## 2. 演进与现状

v1 用"起始地址 → value（含 prefixLen）"单索引 + `SeekGE+Prev` 近似 LPM，前提是**网段不重叠**；运行期回写 `/32`、`/128` 打破了这个前提（见 [history.md](history.md#writeback)）。v2 重构按 roadmap `ipdb-v2-lpm` 分 5 个子 feature 推进：

| 子 feature | 状态 | 产出 |
|---|---|---|
| `ipdb-v2-schema` | done | v2 三套 key codec、base value v2 / overlay value v1、`SchemaFeatures` |
| `ipdb-v2-base-build` | done | `buildV2FromCSV`（双写、staging、重复 prefix 拒绝）、`openBaseV2` |
| `ipdb-v2-query` | done | 真 LPM、三段 CIDR、公开入口原子切 v2、`currentFormatVersion=2`、property test |
| `ipdb-overlay-store` | done | `OverlayStore`（TTL、锁、quarantine）；**backend-only，未接 CLI** |
| `ipdb-lookup-integration` | **planned** | App 双句柄、`IPCandidate`、同步 `Put`、删 `WriteRecord`，见 [ip-lookup.md §11](ip-lookup.md#open) |

演进中的两个取舍：

- 原计划公开 `OpenCurrentBase` 推迟到 integration；收口阶段用 `Store` 过渡壳（§8）让 cli 调用点零 diff（D-13）。
- 子 feature 曾按 `ipdb-v2-codec` / `base-lpm` / `cidr-index` 拆分，2026-06-22 重排为上表 5 条；旧条目均未启动，无遗留。

<a id="layout"></a>
## 3. 磁盘布局（`~/.geoprism/ipdb/`）

```text
ipdb/
├── CURRENT                              # 内容 = 当前激活 base 的 buildID（纯文本）
├── BUILD.lock                           # builder 独占：串行化构建
├── VERSIONS.lock                        # reader 共享 / 发布与回收独占：版本生命周期
├── versions/
│   ├── .staging-{buildID}-{random}/db   # 构建中（os.MkdirTemp）；关库后 rename 为正式目录
│   └── {buildID}/db                     # base 库（v2 格式，不可变，只保留当前版本）
└── overlay/
    ├── OVERLAY.lock                     # 非阻塞独占生命周期锁；Open 到 Close 持有
    ├── db                               # overlay 缓存（独立 Pebble）
    └── quarantine-{utc}[-N]             # metadata / DB 损坏时的隔离证据，不自动清理
```

- 默认 BuildID：`time.Now().UTC().Format("20060102T150405.000000000")`，同 tick / 时钟回拨 / 已存在同名目录时追加 `-1`、`-2`…（`allocateDefaultBuildID`，在 `BUILD.lock` 内分配）。公开入口 `ipdb build` 不传 BuildID，因此字符集与路径分隔符风险不存在。
- 旧版本与崩溃遗留的 staging 由 `cleanupOldVersions` 回收（只保留 `CURRENT` 指向的版本）。

<a id="keys"></a>
## 4. key 编码

首字节 `kind` 区分索引类型；数值与 v1 的 `0x04` / `0x06` 不重叠，便于识别。

| kind | 用途 | 布局 | value |
|---|---|---|---|
| `0x14` / `0x16` | primary LPM 主索引（V4 / V6） | `[kind][prefixLen:1B][maskedAddr:4B\|16B]` | base value v2（canonical） |
| `0x24` / `0x26` | cidr 二级索引（V4 / V6） | `[kind][maskedAddr:4B\|16B][prefixLen:1B]` | **零长度**（D-09） |
| `0x34` / `0x36` | overlay 单 IP 缓存（V4 / V6） | `[kind][addr:4B\|16B]` | overlay value v1 |
| `0x00` | metadata | `[0x00]'m''e''t''a'` | `Metadata` JSON |

长度精确：primary V4 = 6、V6 = 18；cidr V4 = 6、V6 = 18；overlay V4 = 5、V6 = 17。decode 时长度不符即报错。

`validatePrefixV2`（encode 侧）与 `decodePrefixV2`（decode 侧）**对称校验**（D-11）：

- `IsValid()`（`netip.PrefixFrom` 越界时返回 `Bits()==-1` 的 invalid Prefix，**不会 panic**，必须显式判断）。
- 已 `Masked()`（`p.Addr() == p.Masked().Addr()`），codec 不内部 Mask。
- `prefixLen` 在 family 上限内（V4 ≤ 32，V6 ≤ 128）。
- 拒绝 IPv4-mapped IPv6（`Is4In6()`，`::ffff:x.x.x.x`）：family 二义会让 V4 ladder 永远查不到被编码成 V6 key 的记录（D-10）。overlay key 同样拒绝，并拒绝带 zone 的 IPv6。
- decode 额外校验：addr family 与 kind 一致；key 中的 addr 必须是 masked（带 host bits 视为损坏）。
- primary ↔ cidr 对同一网段必须能互相还原出同一个 `netip.Prefix`。

<a id="values"></a>
## 5. value 编码（两套独立协议）

与库级 `FormatVersion` 解耦，value 自带协议版本号。

**base value v2**：`[ver=2][flags=0]` + 7 × (`uvarint len` + UTF-8 字段)，字段顺序固定：`Country, CountryCode, Continent, ContinentCode, ASN, ASName, ASDomain`。不含 `prefixLen`（已进 key）、不含 `Network`（由 Store 据 key 回填）。decode 要求 `flags==0` 且消费完全部字节（多余尾部报错）。

**overlay value v1**：`[ver=1][flags=0]` + 7 字段（同上）+ `[sourceLen uvarint][source]` + `[fetchedAtUnix int64 BE][expiresAtUnix int64 BE]`。`expiresAtUnix==0` 表示永不过期（零值 `time.Time` 显式写 0，**不得**用 `time.Time{}.Unix()`）。

**解码安全（D-11）**：

- 所有 `uint64 → int` 转换先做上界检查：`addLenOffset(offset, fieldLen, valueLen)` 用 `uint64` 比较确保不越过 value 末尾，避免 `int(fieldLen)` 溢出为负导致边界检查失真。
- 损坏数据返回 error，**不得 panic**。
- 适用于任何外部字节 decode（base value、overlay value、key）。

<a id="metadata"></a>
## 6. Metadata 与 SchemaFeatures

`Metadata`（JSON 存于 `meta` key）：`format_version`、`source_csv`、`built_at`、`row_count`、`ipv4_count`、`ipv6_count`、`builder`、`pebble_module`、`pebble_version`、`schema_features`（omitempty）。

`SchemaFeatures uint32` capability 位：

| 位 | 含义 |
|---|---|
| `PrimaryLPM = 1<<0` | 含 primary LPM 主索引 |
| `CIDRStartIdx = 1<<1` | 含 cidr 起始地址二级索引 |
| `CIDRInlineValue = 1<<2` | 预留：未来若 cidr 查询成瓶颈，以此引入"cidr 索引内联整份 Record"；当前不使用 |

v2 库写 `PrimaryLPM | CIDRStartIdx`。v1 库 JSON 无该字段 → 反序列化为 0 = 无 v2 capability，`OpenCurrent` 据此拒绝。

<a id="build"></a>
## 7. 构建（`BuildFromCSV` → `buildV2FromCSV`）

输入契约：

- CSV 路径必须是绝对路径（`runIPDBBuild` 与 `buildV2FromCSV` 双重校验）。
- 表头必须精确等于 `network,country,country_code,continent,continent_code,asn,as_name,as_domain`。
- `network` 可为 CIDR 或单个 IP（IP → `/32` 或 `/128`）；必须是规范网段（`addr == masked.Addr()`），否则拒绝。
- **每个 family 内起始地址非递减**；乱序直接 reject（输入契约 + 性能优化）。造 fixture 时随机 prefix 必须先按 `(family, addrBytes)` 排序。
- **重复 prefix 严格拒绝**（D-08）：同 family 内 `Masked()` 后完全相同的 prefix 即重复（与业务字段是否相同无关），返回 `ErrDuplicatePrefix`，错误含规范 prefix、重复行号、首次出现行号，例：`CSV 第 128 行出现重复网段 10.0.0.0/8，首次出现于第 42 行`。**不同 prefix 重叠允许**（如 `10.0.0.0/8` + `10.1.0.0/16`）。
- codec 错误（未 Masked、越界、IPv4-mapped IPv6）向上传播，不静默跳过。

写入（`writeV2Records`）：

- 每行同一 batch 双写：`primary → base value v2`，`cidr → []byte{}`；两个 `Set` 之间不判 commit，保证同一行不跨 batch 边界被拆开。
- 批量提交阈值 `v2BatchCommitSize = 10000` 行（包级 var，测试可注入极小值验证"双写不拆"）。
- 全部行写完后写 `Metadata`（`FormatVersion=2`、`SchemaFeatures=PrimaryLPM|CIDRStartIdx`、计数、Pebble 版本）并 `Flush()`。
- 不变量：`primaryCount == cidrCount == Metadata.RowCount`。

发布流程（原子性，D-13 / D-14）：

1. 取 `BUILD.lock` 独占（串行化 builder；取不到报错，不等待）。
2. 清理上次崩溃遗留的 staging（`cleanupStagingDirectories`，持 BUILD 锁时安全）。
3. 分配 BuildID（锁内），`os.MkdirTemp(versions, ".staging-{buildID}-")` 创建独占 staging（chmod 0755），在其 `db/` 下打开 Pebble。
4. 写入、`Flush`、**关库**。
5. 取 `VERSIONS.lock` 独占（等待旧 reader 释放共享锁，阻止新 reader 进入）。
6. `rename(staging → versions/{buildID})`。
7. 切 `CURRENT`：`CreateTemp(CURRENT.tmp-*)` → chmod 0644 → 写 buildID → `Sync` → `Close` → `Rename`。
8. 标记 `success=true`，`cleanupOldVersions`（先重读校验 `CURRENT == buildID` 再删其它版本）；清理失败只 `log` 告警，不把成功发布误报为失败。
9. 任何失败路径：`defer` 兜底关库并删除 `cleanupDir`（rename 前指向 staging，rename 后指向 finalDir；`success` 后不再删）。

<a id="open"></a>
## 8. 打开（`OpenCurrent`）与 `Store` 过渡壳

`store.go` `OpenCurrent(rootDir)` 的判定边界（按顺序）：

| 情形 | 结果 |
|---|---|
| `rootDir` 不存在 / `CURRENT` 不存在或为空 / `versions/{buildID}/db` 不存在 | `ErrNoCurrentDatabase` |
| 取 `VERSIONS.lock` 共享锁失败 | 普通 error（`获取 IPDB 版本生命周期锁失败`） |
| probe DB（ReadOnly）打不开 / 读 metadata 失败 / JSON 损坏 | 普通 error（"库打不开"，不是旧版格式） |
| 读到 metadata 且 `FormatVersion != 2` | **`ErrLegacyFormat`**（仅此一类） |
| `FormatVersion == 2` 但缺 `PrimaryLPM` 或 `CIDRStartIdx` | **`ErrIncompleteSchema`** |
| 一切就绪 | 关 probe DB → `openBaseV2` → 包成 `*Store` |

要点：

- **probe DB 必须在 `openBaseV2` 之前彻底关闭**（后者会二次打开同目录）；所有错误分支都先关 probe 再返回。
- `VERSIONS.lock` 共享锁从读 `CURRENT` 前一直持有到 `Store.Close`，期间 builder 的发布 / 回收（独占）会等待，保证 reader 正在使用的版本目录不会被删。锁最终挂在 `BaseStore.versionsLock` 上，`BaseStore.Close` **先关 Pebble 再释放锁**。
- `openBaseV2`（内部入口）只做 ReadOnly 打开 + 读 metadata + sanity（`FormatVersion==2`，不符返回普通 error 而非 sentinel）；**不**做 capability 拒绝、**不**做 v1 识别。

`Store` 过渡壳（D-13）：`{base *BaseStore, rootDir, buildID}`，`LookupIP` / `LookupCIDR` / `Metadata` / `Close` 签名与 v1 零 diff，转调 v2。`WriteRecord` 保留签名但**显式返回** `base 库只读，不支持运行期写入`（D-01），不依赖任何 Pebble 句柄。integration 阶段拆壳，cli 改持 `*BaseStore` / `*OverlayStore`。

<a id="lookup"></a>
## 9. 查询（`BaseStore`）

**单 IP：LPM ladder**（`LookupIP(addr netip.Addr) (Record, bool, error)`）

- `maxBits` = 32（V4）或 128（V6）；从 `l = maxBits` 递减到 0，每个 `l` 用 `PrefixFrom(addr, l).Masked()` 编码 primary key 做 point Get，**首个命中即为最长前缀**。
- `pebble.ErrNotFound` 继续 ladder；其它错误上抛；decode 失败 / closer 关闭失败上抛。
- 命中的 `Record.Network` 由命中的 prefix 回填（value 不含 Network）。
- 算法对重叠记录正确，不依赖"网段不重叠"。
- 最多 33 / 129 次 Get。

**CIDR：三段合并**（`LookupCIDR(query netip.Prefix) ([]Record, error)`），query 先 `Masked()`：

1. **ancestors**（`lookupCIDRAncestors`）：`l = 0..query.Bits()-1`，对 `query.Addr()` 逐 `l` mask 后用 primary key 精确 `Get`（`primaryKeyExists`），覆盖所有包含 `query.Addr()` 的超集——包括起始地址小于 query 起始的左相交超集（例：query `1.0.0.128/25` 也能找到 `1.0.0.0/24`，这是纯 mask 链会漏掉的情形）。
2. **self + descendants**（`lookupCIDRDescendants`）：扫 cidr 索引 `[kind][query.Addr()]` 到 `[kind][queryLastAddr][0xFF]`（`cidrScanBounds`），decode cidr key 后只保留 `prefix.Bits() >= query.Bits()`。
3. 去重后按 `encodeCIDRKeyV2` 字节序排序（= 起始地址升序 + prefixLen 升序），**确定性输出**。
4. 逐条 `fetchRecordByPrefix` 回查 primary 取 value 并回填 Network；cidr 有、primary 无 → **`ErrCorruptIndex`**，不静默跳过。

<a id="locks"></a>
## 10. 文件锁与并发（D-14）

| 锁 | 模式 | 持有者 / 周期 |
|---|---|---|
| `BUILD.lock` | 独占，阻塞 | builder：清理 staging → 构建 → 发布 → 回收 全程 |
| `VERSIONS.lock` | reader **共享**（`OpenCurrent` → `Close`）；builder 发布 / 回收时**独占** | 保证只保留当前版本时不删除并发 reader 正在使用的库 |
| `OVERLAY.lock` | 独占，**非阻塞** | `OverlayStore` 从 Open 到 Close；冲突返回 `ErrOverlayLocked` |

锁序：builder 先 `BUILD(EX)` 再 `VERSIONS(EX)`，不得反向。

实现：Unix（darwin / linux / freebsd / netbsd / openbsd / dragonfly / solaris）用 `flock` 并对 `EINTR` 重试；Windows 用 `LockFileEx`（`golang.org/x/sys/windows`）；其它平台返回"当前操作系统不支持 IPDB 生命周期文件锁"（`file_lock_unsupported.go`）。

<a id="overlay"></a>
## 11. Overlay（`OverlayStore`）

独立于 base 版本目录的单 IP 缓存，backend-only，**未接入 CLI**（接入设计见 [ip-lookup.md §11](ip-lookup.md#open)）。

- `OpenOverlay(rootDir)`：`MkdirAll(overlay/)` → 非阻塞独占 `OVERLAY.lock`（冲突 → `ErrOverlayLocked`，不等待、不改目录）→ `overlay/db` 存在则打开并校验 `OverlayMetadata{FormatVersion=1, CreatedAt}`，否则新建并写 metadata（`pebble.Sync`）。
- **quarantine**：仅在 Open / metadata 确认阶段发现 Pebble 损坏或 metadata 不可解析 / 版本不符 / 缺 `CreatedAt` 时触发：把 `db` 整体 rename 到 `quarantine-{UTC 纳秒时间戳}[-N]`，再新建空库；隔离目录**不自动清理**（取证用）。目标名探测用 `os.Lstat`（dangling symlink 也算占用，见 [development.md §5](development.md#fs)）；重建失败时保留隔离目录并返回合并错误。运行期 `Get` 遇到损坏 value **不** quarantine，只机会性删除该 key。
- `Get(addr, now) (Record, OverlayMeta, bool, error)`：key = `[kind][addr]`（`/32`、`/128`），point Get；value 损坏 → 删除该 key 并返回 miss；`!ExpiresAt.IsZero() && !now.Before(ExpiresAt)` → 过期，删除并返回 miss（**`now == ExpiresAt` 即过期**；零值永不过期）；命中时 `Network` 回填为 `/32` 或 `/128`。
- `Put(addr, record, meta)`：`pebble.Sync` 持久化、last-write-wins；拒绝 invalid / IPv4-mapped / 带 zone 的地址；`record.Network` 非空时必须等于 `{addr}/32` 或 `/128`；非零 `ExpiresAt` 不得落在 `Unix()==0`（该值保留给"永不过期"）。
- 句柄内 `sync.Mutex` 串行化 `Get` / `Put` / `Close`；`Close` **先关 Pebble 再释放锁**，nil 与重复 Close 均安全；已关闭句柄上的 `Get` / `Put` 返回 `ErrOverlayClosed`。
- 读取 value 后先 `Close` closer 再解码（避免在持锁期间持有 Pebble iterator）；metadata 与 value 在 Close 前复制。

<a id="invariants"></a>
## 12. 不变量清单

改 `backend/ipdb` 前逐条确认没有被破坏：

1. base keyspace 运行期只读；任何在线回写只能走 overlay（D-01）。
2. 同 family 内 `Masked()` 后完全相同的 prefix 构建必失败（`ErrDuplicatePrefix`）；不同 prefix 可重叠（D-08）。
3. cidr 索引 value 零长度；canonical Record 只在 primary；`LookupCIDR` 必须回查 primary，缺失即 `ErrCorruptIndex`（D-09）。
4. 同一行的 primary 与 cidr 必须同一 batch 原子写；`primaryCount == cidrCount == Metadata.RowCount`。
5. codec 的 encode 与 decode 对称校验；拒绝 IPv4-mapped IPv6；损坏数据返回 error 不 panic（D-10 / D-11）。
6. `CURRENT` 只指向已 Flush 关闭的完整目录；切换通过临时文件 + `Rename`。
7. reader 在 `Store.Close` 前持有 `VERSIONS.lock` 共享锁；`Close` 先关 DB 再放锁。
8. v2 kind 字节（`0x14`–`0x36`）不得与 v1（`0x04` / `0x06`）重叠。

<a id="anchors"></a>
## 13. 代码锚点

| 关注点 | 位置 |
|---|---|
| 常量 / 类型 | `backend/ipdb/types.go`（kind、`SchemaFeatures`、`Record`、`Match`、`Metadata`、`BuildOptions`、`OverlayMeta`、文件名常量） |
| 公开构建入口 | `backend/ipdb/builder.go` `BuildFromCSV` / `writeCurrentVersion` / `cleanupStagingDirectories` / `cleanupOldVersions` / `parseNetworkField` |
| v2 构建 | `backend/ipdb/builder_v2.go` `buildV2FromCSV` / `writeV2Records` / `allocateDefaultBuildID` / `ErrDuplicatePrefix` |
| 公开打开入口 / 过渡壳 | `backend/ipdb/store.go` `OpenCurrent` / `Store` / `ErrNoCurrentDatabase` |
| v2 base 查询 | `backend/ipdb/store_v2.go` `BaseStore` / `openBaseV2` / `LookupIP` / `LookupCIDR` / `lookupCIDRAncestors` / `lookupCIDRDescendants` / `fetchRecordByPrefix` / `cidrScanBounds` / `ErrLegacyFormat` / `ErrIncompleteSchema` / `ErrCorruptIndex` |
| 编码 | `backend/ipdb/codec.go`（v1 函数 `encodePrefixKey` / `encodeAddrKey` / `encodeRecordValue` 等为死代码；v2 自 `validatePrefixV2` 起） |
| overlay | `backend/ipdb/overlay.go` `OpenOverlay` / `OverlayStore` `Get` / `Put` / `Close` / `quarantineAndCreateOverlay` / `nextOverlayQuarantineDir` |
| 文件锁 | `backend/ipdb/file_lock_unix.go` / `file_lock_windows.go` / `file_lock_unsupported.go` `acquireFileLock` / `tryAcquireFileLock` |
| 日志 | `backend/ipdb/logger.go` `silentLogger`（屏蔽 Pebble 普通日志） |

<a id="tests"></a>
## 14. 测试（作为契约证据）

- 构建：`builder_v2_test.go` `TestBuildV2FromCSVDualIndex`、`…RejectsDuplicatePrefix`、`…AllowsDistinctOverlap`、`…DualWriteNotSplitByBatch`、`…StagingSuccess` / `…CleanupOnFailure`、`…RejectsOutOfOrder`、`…RejectsIPv4MappedIPv6`、`TestConcurrentBuildsKeepCurrentTarget`、`TestConcurrentBuildsAcrossProcessesKeepCurrentTarget`、`TestDefaultBuildIDReallocatesExistingName`、`TestBuildLockReleasedAfterProcessExit`、`TestCleanupWaitsForCurrentReader`、`TestCrashStagingCleanupPreservesCurrentWithReservedPrefix`；`builder_store_test.go` 端到端 `TestBuildFromCSVAndLookupIP` 等。
- 编码：`codec_v2_test.go`（round trip、字节布局互异、未 Masked / 越界 / 4in6 / 未知 kind / 错长度 / 非 masked addr、value 版本 / flags / 截断 / 多余尾部 / 超大 fieldLen）；`types_v2_test.go`（capability 位、`schema_features` omitempty、v1 metadata 回归）。
- 查询：`store_v2_test.go`（`TestBaseStoreLookupIPLPM`、`…NetworkBackfill`、`…LookupCIDRThreePhase` / `MultiAncestors` / `AncestorsSupersetStart` / `Descendants` / `IPv6` / `CorruptIndex`、`TestOpenCurrentV2Success` / `RejectsLegacyFormat` / `RejectsIncompleteSchema` / `NonLegacyErrors` / `ProbeDBReleased` / `ErrorReleasesVersionsLock`、`TestStoreWriteRecordFails`）；`lookup_property_test.go` `TestLookupPropertyLPM` / `TestLookupPropertyCIDR`（随机 prefix 集 + 暴力 oracle，400 轮）。
- overlay：`overlay_test.go`（metadata 创建与重开、锁冲突与跨进程 / 进程退出、不隔离锁定状态下的损坏、quarantine 触发条件 / 唯一名 / dangling symlink / 新建失败保留隔离、Put/Get round trip、last-write-wins、TTL 边界、坏 value 机会性删除、拒绝非法输入、同步写、过期删除与并发 Put 串行化、Close 等待在途操作、`TestBaseRebuildDoesNotTouchOpenOverlay`）。

<a id="open"></a>
## 15. 未决事项

- **`ipdb-lookup-integration`（planned）**：见 [ip-lookup.md §11](ip-lookup.md#open)。落地后拆 `Store` 壳、删 `WriteRecord`、清 `Store` 上的 `rootDir` / `buildID` 镜像字段。
- **`codec.go` 体量**：581 行，v1 死代码与 v2 混在一起；建议拆 `codec_v2.go` 并删除无调用的 v1 函数（`encodePrefixKey`、`encodeAddrKey`、`decodeKeyAddr`、`boundsForAddr`、`encodeRecordValue`、`decodeRecordValue`）与 `types.go` 的 `metadataKeyName`（无调用）。属重构，不改行为。
- **IPv6 ladder 性能**：每次 `PrefixFrom(...).Masked()` + 编码分配新 key；benchmark 记录 IPv4 ≈7.4µs、IPv6 ≈64µs（2026-07 验收时数据）；可预分配 buffer 优化，未做。
- **property test 轮数**：400 轮硬编码；可做成可配置，未做。
- **平台验证**：Windows 只有交叉编译，没有真实内核的跨进程锁运行测试（建议 CI 加 Windows job）；Solaris 在 build tag 内，但 Pebble 对 Solaris 的支持需按当前 `pebble/v2` 版本复核（未验证）。
- **overlay 清理 / 浏览命令**：未做；quarantine 目录需人工清理。
