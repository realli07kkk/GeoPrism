---
doc_type: roadmap
slug: ipdb-v2-lpm
status: active
created: 2026-06-20
last_reviewed: 2026-07-15
tags: [ipdb, lpm, pebble, storage, cidr, overlay]
related_requirements: [offline-ip-lookup]
related_architecture: [ip-lookup]
---

# IPDB v2：真正的最长前缀匹配存储重构

## 1. 背景

当前离线库（format version 1）的 Pebble key 只编码网段的 masked 起始地址，prefix length 存在 value 里；单 IP 查询靠"SeekGE 后回看一条前驱再判 Contains"近似最长前缀匹配（LPM）。这套近似算法成立的唯一前提是 **基础库网段互不重叠且有序**——`BuildFromCSV` 在导入阶段强制了这一点（`backend/ipdb/builder.go:162-167`）。

但 ipinfo 在线回写会把单 IP 的 /32（IPv6 /128）写进同一个 keyspace（`internal/cli/ip_merge.go` → `backend/ipdb/store.go:85`），打破了"不重叠"前提，导致：

- 被大网段覆盖的 IP，因为最近前驱变成不包含它的 /32 而返回 MISS；
- 回写 /32 与已有网段起始地址相同时直接覆盖原网段记录（永久，重建前不可逆）。

该正确性问题已由 issue `2026-06-20-ipdb-writeback-breaks-lpm` 记录并止血（运行期回写已全部禁用）。本 roadmap 做根治：把存储格式升级到 v2，用真正的 prefix-key 结构实现 LPM，并把"不可变基础库"与"运行期在线缓存"物理分离。这是 M3"更新功能"（增量 / 远程刷新库）的前置条件——M3 需要一个能安全承载多来源、可重叠记录的存储层。

## 2. 范围与明确不做

### 本 roadmap 覆盖

- v2 key/value 编码：primary LPM 主索引（按 prefix length 组织）+ CIDR 二级索引（按起始地址组织，**零长度 value**）+ 去掉 value 里的 prefixLen
- **base value v2 与 overlay value v1 两套物理独立的 value 协议**（不共用 flag，各自独立演进）
- v1 / v2 codec **并存**：v1 读路径保持不动，v2 codec 显式命名新增；最终切换前不动 `currentFormatVersion`
- 单 IP 查询改为真正的 LPM（逐前缀长度精确查找 ladder），重叠记录下结果正确
- CIDR 查询改为 **ancestors（primary 精确 Get）+ self + descendants（cidr 区间扫描）** 三段合并，重叠/嵌套网段下返回全部相交记录
- base 构建：**相同 prefix 严格拒绝**（`ErrDuplicatePrefix`），**允许不同 prefix 的重叠**（删除现有 overlap reject）；staging 目录原子构建；primary/cidr 同 batch 双写
- base / overlay 物理分离：base 为 CSV 构建的不可变库、**`ReadOnly` 打开**；overlay 为 ipinfo 单 IP 缓存（独立 metadata / version / TTL）
- 回写目标从"写进 base keyspace"改为 **同步写进 overlay**（`OverlayStore.Put`，失败仅 warning）
- format version 升级与 v1 旧库识别（`ErrLegacyFormat`）+ capability 校验（`ErrIncompleteSchema`）
- **切换原子性**：`ipdb-v2-base-build` 只实现未激活的内部构建/打开能力，直到 `ipdb-v2-query` 收口才原子切换公开 `BuildFromCSV` / `OpenCurrentBase` / 查询路径 / `currentFormatVersion=2`

### 明确不做

- **在线查询语义统一 + 回写生命周期队列**（审计意见 #2：`ipdb-first` 短路、`--offline` / `--verify-online` / `--no-writeback` flag、`WriteBackQueue` + `App.Close` 等待）——独立 feat，消费本 roadmap 暴露的 overlay 接口契约，不在此实现。本 roadmap 回写采用**同步 `OverlayStore.Put`**。
- **M3 更新功能本身**（增量更新、远程下载 / 校验库、版本比对）——本 roadmap 是其前置存储能力。
- **`data_source_priority` 策略语义改动**——保持现状（默认 ipdb-first，CIDR 不受控）；只调整底层存储与候选模型，不改对外优先级行为。
- **overlay 缓存的可视化 / 清理命令**（`ipdb overlay clear` 之类）与**容量 / LRU 回收**——只做 TTL + 过期机会性删除，不做主动容量回收。记观察项。
- **多进程共享同一 Pebble 目录的并发支持**——只保证"任一存储组件因 lock 打不开时独立降级、不拖垮其它组件"，不解决多进程共享读写。

## 3. 模块拆分（概设）

```
IPDB v2
├── 编码层 codec-v2        ：v2 key/value 编解码；base value v2 与 overlay value v1 两套独立协议；v1/v2 并存
├── base 存储             ：CSV 构建不可变库（同 batch 双写 primary+cidr）；真 LPM 单 IP + CIDR 三段查询；ReadOnly 打开
├── overlay 存储          ：运行期 ipinfo 单 IP 缓存，独立 metadata/version/TTL，物理独立于 base
└── 查询编排 + 迁移        ：App 分别持有 base/overlay 并各自降级；cli 三来源候选合并；v1 识别与重建提示
```

### 模块 A · 编码层 codec-v2
- **职责**：定义 v2 的 key/value 二进制布局并提供编解码函数。primary 索引按 `[kind][prefixLen][maskedAddr]`（支撑逐前缀长度精确查找），CIDR 二级索引按 `[kind][maskedAddr][prefixLen]`（支撑按起始地址区间扫描，**value 零长度**），overlay 按 `[kind][addr]`。base value v2 与 overlay value v1 是**两套独立 value 协议**，不共用 flag。**v1 codec 保持不动，v2 codec 以显式命名（…V2）并存**。不负责任何查询 / 存储逻辑。
- **承载的子 feature**：`ipdb-v2-schema`
- **触碰的现有代码**：`backend/ipdb/codec.go`（新增 V2 编解码函数，不删 v1）、`backend/ipdb/types.go`（新增 kind 字节、`SchemaFeatures` 常量与字段；**不改 `currentFormatVersion`**）

### 模块 B · base 存储
- **职责**：从 CSV 构建 v2 不可变 base 库（primary 与 cidr 两套索引**同一 batch 原子双写**）；单 IP 查询用 primary 索引做真正的 LPM ladder；CIDR 查询用"祖先精确 Get + 自身/后代区间扫描"三段合并；base 运行期 `ReadOnly` 打开、永不被回写改动；相同 prefix 拒绝、允许不同 prefix 重叠；staging 目录构建后 rename。`ipdb-v2-base-build` 阶段以**内部入口**（`buildV2FromCSV` / `openBaseV2`）实现且不激活；`ipdb-v2-query` 阶段才把公开入口与 `currentFormatVersion` 切到 v2。
- **承载的子 feature**：`ipdb-v2-base-build`、`ipdb-v2-query`
- **触碰的现有代码**：`backend/ipdb/builder.go`（双写 / staging / 去除重叠 reject 改为重复 prefix reject / 内部入口）、`backend/ipdb/store.go`（`BaseStore` 结构、`openBaseV2` → `OpenCurrentBase`、`LookupIP` / `LookupCIDR` 改写）

### 模块 C · overlay 存储
- **职责**：运行期 ipinfo 单 IP 结果缓存，独立 Pebble keyspace + **独立 `OverlayMetadata`（`overlayFormatVersion`）**；只存 /32、/128；每条带 source / 抓取时间 / 过期时间；TTL 过期视为未命中并机会性删除；corruption / lock 独立降级。永不被 CSV 构建触碰。
- **承载的子 feature**：`ipdb-overlay-store`
- **触碰的现有代码**：新增 `backend/ipdb/overlay.go` 及测试，提供同步 `OpenOverlay` / `Get` / `Put` / `Close` 能力；小幅扩展 `backend/ipdb/file_lock_*` 的非阻塞取得能力且不改变 base 锁语义；不接入 CLI

### 模块 D · 查询编排 + 迁移
- **职责**：`App` 分别懒加载并持有 `BaseStore` / `OverlayStore`（各自独立的 err 字段与降级），不再聚合成单一句柄；cli 用 `IPCandidate` 把 base / overlay / live 三来源交给**纯函数** `selectCandidate` 选择；live 查询成功后**同步**调用 `OverlayStore.Put`（失败仅 warning）；打开 v1 base 返回 `ErrLegacyFormat` 提示重建、缺 capability 返回 `ErrIncompleteSchema`。
- **承载的子 feature**：`ipdb-lookup-integration`
- **触碰的现有代码**：`backend/ipdb/store.go`（`OpenCurrentBase` / 删除 `WriteRecord`）、`internal/cli/app.go`、`internal/cli/ip_lookup.go`、`internal/cli/ip_match.go`、`internal/cli/ip_merge.go`、`internal/cli/cidr_lookup.go`；消费模块 C 已提供的 `OpenOverlay`

## 4. 模块间接口契约 / 共享协议（架构层详设）

> 以下为 feature-design 的硬约束输入。要改先回 `cs-roadmap update`。

### 4.1 v2 key 布局 + capability（codec-v2 → base / overlay）

**方向**：编码层 → base 存储、overlay 存储
**形式**：Pebble key 字节协议 + Go 函数签名 + capability 位

```
kind 字节（与 v1 的 0x04/0x06 不冲突，便于版本/用途识别）：
  meta            = 0x00          # 沿用，value 为 JSON Metadata
  primaryV4       = 0x14
  primaryV6       = 0x16
  cidrV4          = 0x24
  cidrV6          = 0x26
  overlayV4       = 0x34
  overlayV6       = 0x36

primary key（LPM 主索引）：[kind][prefixLen:1B][maskedAddr: 4B|16B]
cidr key（二级索引）   ：[kind][maskedAddr: 4B|16B][prefixLen:1B]   # value 零长度
overlay key            ：[kind][addr: 4B|16B]                       # 只有 /32、/128

capability（backend/ipdb/types.go）：
  type SchemaFeatures uint32
  const (
      SchemaFeaturePrimaryLPM    SchemaFeatures = 1 << 0   // 含 primary LPM 主索引
      SchemaFeatureCIDRStartIdx  SchemaFeatures = 1 << 1   // 含 cidr 起始地址二级索引
      // 预留：SchemaFeatureCIDRInlineValue（CIDR 索引内联整份 Record，未来若成瓶颈再启用）
  )

函数签名（backend/ipdb/codec.go）：
  func encodePrimaryKeyV2(p netip.Prefix) ([]byte, error)
  func decodePrimaryKeyV2(key []byte) (netip.Prefix, error)
  func encodeCIDRKeyV2(p netip.Prefix) ([]byte, error)
  func decodeCIDRKeyV2(key []byte) (netip.Prefix, error)
  func encodeOverlayKeyV2(a netip.Addr) ([]byte, error)
  func decodeOverlayKeyV2(key []byte) (netip.Addr, error)
```

**约束**：
- `encodePrimaryKeyV2` / `encodeCIDRKeyV2` 入参必须已 `Masked()`（调用方保证），否则返回 error。
- prefixLen 单字节，取值 0–32（IPv4）/ 0–128（IPv6），越界返回 error。
- primary 与 cidr 两套 key 对同一网段必须可互相还原出同一个 `netip.Prefix`。
- **CIDR 二级索引 value 为零长度**：逻辑记录的 canonical value 只存在于 primary；CIDR 查询解出 prefix 后回查 primary 取 value（见 §4.3）。
- 完整 v2 base 库的 `Metadata.SchemaFeatures` 必须含 `SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx`，否则视为不完整 schema。

### 4.2 v2 value 布局（codec-v2 ↔ base / overlay，两套独立协议）

**方向**：编码层 ↔ base / overlay 存储
**形式**：value 字节协议 + 函数签名

```
Base value v2：
  [baseValueVersion:1B=2][flags:1B=0] + 7×(uvarint 长度前缀 + UTF-8 字段)
  7 字段顺序固定（去掉 prefixLen，已进 key）：
    Country, CountryCode, Continent, ContinentCode, ASN, ASName, ASDomain

Overlay value v1（独立协议，不复用 base flag）：
  [overlayValueVersion:1B=1][flags:1B=0] + 7×字段（同上顺序）
  + [sourceLen uvarint][source bytes]
  + [fetchedAtUnix: int64 BE][expiresAtUnix: int64 BE]

函数签名：
  func encodeBaseRecordValueV2(rec Record) ([]byte, error)
  func decodeBaseRecordValueV2(value []byte) (Record, error)
  func encodeOverlayRecordValueV1(rec Record, meta OverlayMeta) ([]byte, error)
  func decodeOverlayRecordValueV1(value []byte) (Record, OverlayMeta, error)
```

**约束**：
- `Record.Network` 不进 value，由 Store 据 key 的 prefix 还原后回填；**decode 返回的 `Record.Network` 为空**，回填责任在 `BaseStore` / `OverlayStore`。
- base value 与 overlay value 是**两套独立协议**，不共用 `flagOverlayMeta`，各自独立演进版本。
- decode 遇到 version 字节不符返回明确错误，其含义是"**库内部损坏 / 用错 decoder**"，**不**用于识别 v1；v1/v2 的**库级**识别只依赖 `Metadata.FormatVersion`（读 JSON metadata）。
- `expiresAtUnix == 0` 表示**永不过期**；不得用 `time.Time{}.Unix()` 作为零值编码。

### 4.3 base 存储查询接口（base → 查询编排）

**方向**：base 存储 → 查询编排 / cli
**形式**：Go 方法签名

```
# 实施顺序调整（2026-06-22，ipdb-v2-query design 拍板）：
# OpenCurrentBase 这个公开 API 的实际落地推迟到 ipdb-lookup-integration
# （届时 App 改持 *BaseStore/*OverlayStore、拆 Store）。ipdb-v2-query 收口时
# 用 Store 过渡壳：OpenCurrent 内部转调 v2 BaseStore 真查询、capability/v1
# 拒绝逻辑落在 OpenCurrent（不是新公开 API）。下面的 OpenCurrentBase 签名
# 描述的是"收口后系统能力"，integration 阶段按需拆出公开 API。
func OpenCurrentBase(rootDir string) (*BaseStore, error)
  # ReadOnly 打开当前 CURRENT 指向的 base；只接受完整 v2：
  #   Metadata.FormatVersion != 2          → ErrLegacyFormat
  #   SchemaFeatures 缺 primary|cidr        → ErrIncompleteSchema

func (s *BaseStore) LookupIP(addr netip.Addr) (rec Record, matched bool, err error)
  # 真正的 LPM ladder：
  #   for L := maxBits(addr); L >= 0; L-- {
  #     key := encodePrimaryKeyV2(PrefixFrom(addr, L).Masked())
  #     if v, closer, err := db.Get(key); err == nil { 命中即返回（最长前缀） }
  #   }
  # 全程未命中返回 matched=false

func (s *BaseStore) LookupCIDR(query netip.Prefix) ([]Record, error)
  # 1. ancestors：L 从 0 到 query.Bits()-1，
  #    primary 精确 Get(encodePrimaryKeyV2(PrefixFrom(query.Addr(), L).Masked()))
  # 2. self + descendants：cidr 索引扫起始地址 [queryStart, queryEnd]，
  #    仅保留 prefix.Bits() >= query.Bits()；对每条 cidr key 解出 prefix 后回查 primary 取 value
  # 3. 去重 + 按 (startAddr, prefixLen) 确定性排序
  # cidr key 存在但对应 primary key 不存在 → ErrCorruptIndex（不静默跳过）

func (s *BaseStore) Close() error
```

**约束**：
- `LookupIP` 必须返回覆盖该 addr 的**最具体**网段（最长前缀），与是否存在更粗网段无关——本 roadmap 的核心正确性目标。该 ladder 算法**本身已对重叠正确**，无需依赖"不重叠"前提。
- `LookupCIDR` 必须返回**所有**与 query 相交的网段，含多层祖先（反例：库含 `10.0.0.0/8` + `10.1.0.0/16`，查 `10.1.2.0/24` 必须两条都返回——v1 单次 `Prev()` 会漏 `/8`）。
- base 库 `ReadOnly`，这两个方法不得写库。
- IPv4 ladder 最多 33 次 Get、IPv6 最多 129 次；判定为可接受，但**必须由 acceptance benchmark 验证**（见 §7）。

### 4.4 overlay 存储接口（overlay → 查询编排）

**方向**：overlay 存储 → 查询编排 / 回写方
**形式**：Go 类型 + 方法签名

```
type OverlayMeta struct {
    Source    string    // 来源标识，如 "ipinfo"
    FetchedAt time.Time // 抓取时间
    ExpiresAt time.Time // 过期时间；零值表示永不过期（磁盘编码为 0）
}

type OverlayMetadata struct {
    FormatVersion int       `json:"format_version"` // overlayFormatVersion，初始 = 1
    CreatedAt     time.Time `json:"created_at"`
}

func OpenOverlay(rootDir string) (*OverlayStore, error)
func (o *OverlayStore) Get(addr netip.Addr, now time.Time) (Record, OverlayMeta, bool, error)
  # 精确查 /32 或 /128
  # 过期判定：expired := !meta.ExpiresAt.IsZero() && !now.Before(meta.ExpiresAt)
  #          即 now == ExpiresAt 已过期；命中过期项视为未命中并机会性删除
func (o *OverlayStore) Put(addr netip.Addr, rec Record, meta OverlayMeta) error
func (o *OverlayStore) Close() error
```

**约束**：
- `addr` 必须是合法 IPv4 或真实 IPv6，拒绝 invalid / IPv4-mapped IPv6 / 带 zone 的 IPv6；key 隐含 /32 或 /128。`Record.Network` 不进 value，`Get` 按 key 回填规范 host prefix；`Put` 接受空 Network，非空时必须与 `addr` 表示同一个 host prefix。
- overlay 物理独立于 base 版本目录，base 重建不触碰 overlay。
- `Put` 必须幂等（同 IP 覆盖写）。
- **生命周期锁**：`OpenOverlay` 在任何 `overlay/db` 打开、隔离或重建动作前，非阻塞取得 `overlay/OVERLAY.lock` 独占锁并持有到 `Close`；忙锁返回可判定的 `ErrOverlayLocked`，不得等待、rename 或删除。该外层锁消除“关闭 Pebble 后、rename 前”被另一进程抢先打开的竞态，且不改变 base 现有锁语义。
- **降级**：成功持有生命周期锁后，`OpenOverlay` 在打开既有 DB 或读取 `OverlayMetadata` 阶段发现 metadata 缺失、JSON 损坏、版本不兼容，或 Pebble 明确报告 corruption → 先完整关闭所有已取得的 reader/DB，再把 `overlay/db` rename 到唯一 quarantine sibling 并重建；任一关闭失败、普通权限 / I/O / lock/open 错误都只返回 error，不 rename / 删除。运行期 `Get` / `Put` 的 Pebble corruption 只返回 error，不在活句柄内重建；单条 record value 解码损坏 → 作为 cache miss，可机会性删除。禁用 overlay 后继续 base/live 属 §4.5 integration 责任。
- **TTL 默认值不在此层**：`OverlayStore` 只负责保存与判断给定的 `ExpiresAt`；默认 TTL 由 cli 编排层决定（见 §4.5）。
- 不做容量 / LRU 回收（仅 TTL + 机会性删除）。

### 4.5 cli 编排与回写（查询编排 → cli）

**方向**：查询编排 → cli
**形式**：Go 类型 + 方法签名 + 候选选择纯函数

```
App 分别持有（不再聚合单一 Store 句柄）：
  type App struct {
      // ...
      ipdbBase       *ipdb.BaseStore
      ipdbOverlay    *ipdb.OverlayStore
      ipdbBaseErr    error
      ipdbOverlayErr error
  }

三来源候选模型：
  type CandidateOrigin uint8
  const (
      CandidateBase CandidateOrigin = iota   // Source 展示 "ipdb"
      CandidateOverlay                        // Source 展示 "ipinfo"
      CandidateLive                           // Source 展示 "ipinfo"
  )
  type IPCandidate struct {
      Origin    CandidateOrigin
      Match     ipdb.Match
      Source    string    // 用户展示来源："ipdb" 或 "ipinfo"
      FetchedAt time.Time // 仅 overlay/live 有意义
  }

  // 纯函数：只从已取得的候选中按 priority 选择
  func selectCandidate(priority settings.DataSourcePriority, candidates []IPCandidate) (IPCandidate, bool)

默认 TTL（在 integration 层）：
  const defaultOverlayTTL = 24 * time.Hour
```

**约束**：
- `selectCandidate` 是**纯函数**：只负责从"已经取得的候选"中选择，**不**发起 ipinfo HTTP、**不**决定 short-circuit、**不**写 overlay、**不**输出 warning。
- 同步回写：live 查询成功后由编排代码 `overlay.Put(...)`，失败仅 warning、不影响本次返回；**即使本次因 priority 最终选了 base，只要 live 请求已发生也写 overlay**（保持"缓存在线结果"语义）。
- **删除 / 废弃旧 `WriteRecord`**——任何路径都不得再向 base keyspace 写单 IP。
- 默认 TTL 由 integration 设置：`ExpiresAt = now.Add(defaultOverlayTTL)`；建议**注入 clock** 便于 TTL 测试。
- 组件级降级矩阵：

  | 状态 | 行为 |
  |---|---|
  | base v2 正常 + overlay 正常 | 两者都可用 |
  | base 为 v1 | 忽略 base、提示重建（`ErrLegacyFormat`），overlay / live 仍可用 |
  | base 缺 capability | 忽略 base、提示重建（`ErrIncompleteSchema`），overlay / live 仍可用 |
  | base 被另一进程锁定 | base 不可用，overlay / live 继续 |
  | overlay 被锁定 | 跳过缓存（不读不写），base / live 继续 |
  | overlay metadata 损坏 | 隔离或禁用 overlay，base / live 继续 |
  | 无 base | overlay / live 仍可用于单 IP |

- warning 一律走 cli stderr，不污染 JSON / 文本 stdout 协议。
- 合并仍遵循现有 `data_source_priority` 语义，**不**顺带统一在线查询语义（独立 feat）。

### 4.6 共享数据结构 / 状态

```
磁盘布局 v2（~/.geoprism/ipdb/）：
ipdb/
├── CURRENT                          # 内容是当前激活 base 的 buildID（沿用）
├── versions/
│   ├── .staging-{buildID}/db        # 构建中：验证 + 关库后 rename 为正式目录再切 CURRENT
│   └── {buildID}/db                 # base 库（v2 格式，不可变）
└── overlay/
    ├── OVERLAY.lock                 # 非阻塞独占生命周期锁；Open 到 Close 持有
    ├── db                            # overlay 缓存（独立 Pebble，跨 base 版本存活）
    └── quarantine-{utc}-{suffix}    # metadata/DB corruption 隔离证据；不自动清理

base 打开：pebble.Options{ ReadOnly: true, Logger: silentLogger{} }

Metadata（backend/ipdb/types.go）：
  FormatVersion  从 1 升到 2（仅在 ipdb-v2-query 收口时切）
  SchemaFeatures uint32（新增，标识 primary / cidr 索引能力）

构建不变量（base-build / query 验收）：
  primaryCount == cidrCount == Metadata.RowCount   # primary 与 cidr 同一 batch 原子写

overlay 独立元数据：OverlayMetadata{ FormatVersion=1, CreatedAt }
```

**锁失败降级总则**：本 roadmap 不解决多进程共享同一 Pebble 目录。任一存储组件因 lock 无法打开时，该组件独立降级为不可用，不影响其它组件——base lock 失败时仍可用 overlay / live，overlay lock 失败时仍可用 base / live。

## 5. 子 feature 清单

1. **ipdb-v2-schema** — v2 primary/cidr/overlay key codec + base value v2 / overlay value v1 两套独立 codec + `SchemaFeatures` 定义；拍板重复 prefix 与 CIDR 零长度 value；保持 v1 公开行为不变、不改 `currentFormatVersion`
   - 所属模块：模块 A · 编码层
   - 依赖：无
   - 状态：done
   - 对应 feature：2026-06-22-ipdb-v2-schema
   - 备注：契约见 §4.1 / §4.2；纯编码 + 单测，无对外行为变化

2. **ipdb-v2-base-build** — 实现**未激活**的 v2 builder（首版即同 batch 双写 primary+cidr）+ v2 `BaseStore` 的 ReadOnly 打开；允许不同 prefix 重叠、相同 prefix 严格拒绝（`ErrDuplicatePrefix`）；staging 构建后 rename；**不切换公开 `BuildFromCSV` / `OpenCurrentBase` / `currentFormatVersion`**
   - 所属模块：模块 B · base 存储
   - 依赖：ipdb-v2-schema
   - 状态：done
   - 对应 feature：2026-06-22-ipdb-v2-base-build
   - 备注：内部入口 `buildV2FromCSV` / `openBaseV2(rootDir, buildID)`；公开入口仍指向 v1，避免"能构建不能查询"的窗口

3. **ipdb-v2-query** — `BaseStore.LookupIP` 真 LPM ladder + `LookupCIDR` 三段（ancestors+self+descendants）查询 + property test；**收口处原子切换公开 builder/open/query 到 v2、`currentFormatVersion=2`**；v1 → `ErrLegacyFormat`、缺 capability → `ErrIncompleteSchema`
   - 所属模块：模块 B · base 存储
   - 依赖：ipdb-v2-base-build
   - 状态：done
   - 对应 feature：2026-06-22-ipdb-v2-query
   - 备注：**最小闭环**已落地；`OpenCurrentBase` 公开 API 推迟到 integration（query 收口用 `Store` 过渡壳转调，见 §9）；property test 400 轮全绿、benchmark IPv4 7.4μs / IPv6 64μs

4. **ipdb-overlay-store** — 新增独立 overlay 存储（`OpenOverlay` / `Get` / `Put` / `Close`、独立 `OverlayMetadata`/version、TTL、机会性删除、corruption/lock 降级），base 重建不触碰 overlay；本 feature 只提供 backend 能力，不接入 CLI
   - 所属模块：模块 C · overlay 存储
   - 依赖：ipdb-v2-schema
   - 状态：done
   - 对应 feature：`2026-07-15-ipdb-overlay-store`
   - 备注：backend 能力已于 2026-07-15 验收；CLI 读取与 live 结果同步 `OverlayStore.Put` 统一归第 5 条；两条联合落地后正式取代 issue `2026-06-20-ipdb-writeback-breaks-lpm` 的止血

5. **ipdb-lookup-integration** — `App` 分别持有 base/overlay 并各自降级；引入 `IPCandidate` 三来源纯函数选择；live 成功后同步调用 `OverlayStore.Put`（失败仅 warning）；删除 `WriteRecord`；回写 architecture
   - 所属模块：模块 D · 查询编排 + 迁移
   - 依赖：ipdb-v2-query, ipdb-overlay-store
   - 状态：planned
   - 对应 feature：未启动
   - 备注：保持现有 fetch orchestration，不顺带统一在线查询语义

**最小闭环**：第 3 条 `ipdb-v2-query` 收口时同时把公开 `BuildFromCSV` → v2 builder、`OpenCurrent` → 内部转调 v2 BaseStore 真查询（`OpenCurrentBase` 公开 API 推迟到 integration，收口用 `Store` 过渡壳）、`LookupIP/LookupCIDR` → v2 查询、`currentFormatVersion` → 2 原子切换。届时可 `ipdb build` 出 v2 库，并演示"对一个被大网段覆盖、同时存在更具体重叠记录的 IP，单 IP 查询返回正确最长前缀"以及"CIDR 查询返回全部相交网段（含多层祖先）"——本 roadmap 的核心正确性目标，端到端可验证。

## 6. 排期思路

按"先底座、再未激活构建、再原子切换正确性、最后接线"推进：

- 先做 `ipdb-v2-schema`：纯编码层、无行为变化、可独立单测，是 B/C 共同地基；不先定死它，后面会各自发明 key 布局导致不一致。
- `ipdb-v2-base-build` 做**未激活**的构建/打开能力（内部入口），首版即双写完整双索引——避免"先只写 primary 的半成品 v2"导致 CIDR 不可用。
- `ipdb-v2-query` 作最小闭环：实现真 LPM + 正确 CIDR，并在**收口处一次性原子切换**公开入口与 `currentFormatVersion`。这一步之前，对用户而言行为与 v1 完全一致。
- `ipdb-overlay-store` 只依赖 schema，可与 base 系并行推进。
- `ipdb-lookup-integration` 收口，依赖 query 与 overlay 都就绪。

**卡点**：
- **切换原子性**：必须保证不出现"能构建 v2 但当前程序不能查询 v2"的中间态——所以 base-build 用内部入口、query 阶段才统一切换。
- **重复 prefix 策略**：已拍板 `reject duplicate`（见决策记录，建议另走 `cs-decide` 沉淀）。
- **format 升级即重建**：选择"明确报错 + 提示重建"而非数据级迁移——CSV 是数据源真相、重建即正确，迁移代码反而是额外维护负担；不保留 v1 数据级兼容读路径。

## 7. Roadmap 级验收门槛

不单设第六个 feature 收容验收事项（避免变成"所有未完成事项的收容箱"、让前面 feature 在缺测试时仍被标完成）。验收责任分配到各 feature，仅"跨 v1/v2 体积与端到端兼容矩阵"留作 roadmap 最终验收：

| 验收内容 | 所属 feature |
|---|---|
| v2 codec 异常输入（错 version / unknown flags / 截断 uvarint / 多余尾部字节）、capability 位 | `ipdb-v2-schema` |
| staging、重复 prefix（`ErrDuplicatePrefix`）、双索引同 batch 原子性、`primaryCount==cidrCount==RowCount`、ReadOnly 写失败 | `ipdb-v2-base-build` |
| LPM/CIDR property test（暴力 oracle）、性能 benchmark（IPv4/IPv6 冷热缓存 p50/p95，1/10/50 个 IP 批量）、v1 重建提示、缺 capability 拒绝打开 | `ipdb-v2-query` |
| TTL（`now==ExpiresAt` 过期 / 零值=0 永不过期）、metadata 损坏隔离、lock 冲突返回错误且目录不变、base 重建后 overlay 持久化、注入 clock | `ipdb-overlay-store` |
| 三来源候选选择纯函数、同步 `OverlayStore.Put`、组件级降级矩阵、CLI 行为与 architecture 回写 | `ipdb-lookup-integration` |
| v1/v2 数据库体积、构建时间、端到端兼容矩阵 | roadmap 最终验收 |

property test 覆盖项（写进 `ipdb-v2-query` checklist）：随机 prefix 集 + 暴力 oracle（单 IP 取 `Bits()` 最大的包含 prefix、CIDR 取全部相交）；边界 `/0`、`/32`、`/128`、同起始 `/8`/`/16`/`/24`、多父 prefix 同时覆盖、完全相同 prefix 重复输入（应 reject）。

## 8. 观察项

- `architecture/ip-lookup.md` §2/§4 仍是**止血前的旧描述**（`maybeWriteBack` 异步回写、"CIDR 查询要回看前一条记录"、`WriteRecord`、单索引 `0x04/0x06`）；§7 已记录 v2 base 与独立 overlay backend 的当前真相，完整主流程重写仍由 `ipdb-lookup-integration` 收口。
- 与 issue `2026-06-20-ipdb-writeback-breaks-lpm` 的关系：`ipdb-overlay-store` + `ipdb-lookup-integration` 落地后正式取代第一层止血；届时在该 issue fix-note 追加"已被 A′ 取代"标注。
- 审计意见 #2（在线查询语义统一 + `WriteBackQueue` 生命周期 + `App.Close` 等待）是独立 feat，依赖本 roadmap §4.4/§4.5 的 overlay 接口；建议第 4、5 条落地后再启动。
- overlay 缓存的清理 / 浏览命令（如 `ipdb overlay clear`）、容量回收策略暂不做，后续若需要另开 feature 或 req。
- 重复 prefix `reject duplicate`、CIDR 索引零长度 value 两项已拍板，建议各走一次 `cs-decide` 沉淀为 decision，避免后续维护者重新讨论。
- `ipdb-v2-lpm-items.yaml` 本次由旧 5 条（`ipdb-v2-codec` / `ipdb-v2-base-lpm` / `ipdb-v2-cidr-index` / `ipdb-overlay-store` / `ipdb-lookup-integration`）重排为新 5 条；旧条目均为 `planned`、无 feature 启动，重排映射见 §9 变更日志（未保留 dropped 墓碑，历史以变更日志 + git 记录）。

## 9. 变更日志

- **2026-06-22**：基于审计意见的重大修订（roadmap 仍处 `active`、无已启动 feature，无受影响的 in-progress/done 条目）。

  **feature 重排（旧 → 新）**：
  - `ipdb-v2-codec` → `ipdb-v2-schema`（v1/v2 codec 并存、不改版本常量、base/overlay value 拆两套独立协议）
  - `ipdb-v2-base-lpm` + `ipdb-v2-cidr-index` → 合并为 `ipdb-v2-base-build`（首版即双写完整双索引、未激活）+ `ipdb-v2-query`（真 LPM + 正确 CIDR + 原子切换、最小闭环）
  - `ipdb-overlay-store`、`ipdb-lookup-integration` slug 保留，契约升级

  **接口契约变化**：
  - §4.1 新增 `SchemaFeatures` capability；CIDR 二级索引 value 改为**零长度**（canonical value 只在 primary）。
  - §4.2 base value v2 与 overlay value v1 拆为**两套独立协议**，去掉 `flagOverlayMeta`；decode 返回的 `Record.Network` 为空由 Store 回填；`expiresAtUnix==0` 表示永不过期。
  - §4.3 `LookupCIDR` 由"单次向前回看"改为 **ancestors（primary 精确 Get）+ self + descendants（cidr 区间扫描）** 三段合并 + `ErrCorruptIndex`；`OpenCurrentBase` ReadOnly 打开、`ErrLegacyFormat` / `ErrIncompleteSchema`。
  - §4.4 overlay 新增独立 `OverlayMetadata`/version、TTL 边界（`now==ExpiresAt` 过期）、机会性删除、corruption/lock 降级。
  - §4.5 `OpenCurrent` 拆为 `OpenCurrentBase` / `OpenOverlay`；`App` 分别持有并各自降级；新增 `IPCandidate` 三来源纯函数选择；同步 `OverlayStore.Put`；删除 `WriteRecord`；默认 TTL 在 integration 层。
  - §4.6 staging 目录原子构建、`primaryCount==cidrCount==RowCount` 不变量、base `ReadOnly` 打开。

  **决策拍板**：重复 prefix = `reject duplicate`（删除 builder 重叠 reject、改为允许不同 prefix 重叠 + 相同 prefix 拒绝）；CIDR 索引 = 零长度 value；不新增第六个 feature，改为 §7 Roadmap 级验收门槛表。

  **受影响的已启动 feature**：无（全部 planned）。

- **2026-06-22（实施顺序调整，ipdb-v2-query design 阶段拍板）**：

  **接口契约变化**：
  - §4.3 `OpenCurrentBase` 公开 API 的实际落地从 `ipdb-v2-query` **推迟到 `ipdb-lookup-integration`**。`ipdb-v2-query` 收口时用 `Store` 过渡壳：`OpenCurrent` 内部转调 v2 BaseStore 真查询（真 LPM + 三段 CIDR），capability/v1 拒绝（`ErrLegacyFormat`/`ErrIncompleteSchema`）落在 `OpenCurrent` 而非新公开 API。`OpenCurrentBase` 签名保留为"收口后系统能力"描述，integration 阶段拆 `Store`、`App` 改持 `*BaseStore`/`*OverlayStore` 时按需落地。
  - §4.5 `App` 双字段改造同步推迟到 integration。query 收口阶段 cli 5 个 `*ipdb.Store` 调用点签名零 diff。

  **理由**：让 query 收口聚焦"真 LPM + 正确 CIDR + v1 拒绝"核心正确性，不提前做 integration 的 App 接线 + 删 `WriteRecord` + 引入 overlay 字段（依赖未就绪）。过渡壳代价是 `Store` 暂时包一层 delegation，integration 时清除。

  **受影响的已启动 feature**：`ipdb-v2-query`（design 阶段，本次调整的发起方）。

- **2026-07-15（ipdb-overlay-store 职责边界澄清）**：

  **feature 边界变化**：
  - `ipdb-overlay-store` 只实现模块 C 的 backend 存储能力：`OpenOverlay` / `Get` / `Put` / `Close`、独立 metadata/version、TTL、机会性删除和 corruption/lock 降级；不修改 `App`，不接入 CLI 查询或在线回写路径。
  - `ipdb-lookup-integration` 统一承担 `App` 的 base/overlay 双句柄生命周期、overlay 读取、live 成功后的同步 `OverlayStore.Put`、三来源候选选择和旧 `WriteRecord` 删除。
  - 活跃契约统一使用真实方法名 `OverlayStore.Put`；overlay 增加独立、非阻塞的 `OVERLAY.lock` 生命周期锁，确保 lock 冲突和 quarantine 竞态都不会破坏现有目录。API 签名与 feature DAG 不变。

  **理由**：原第 4、5 条都写了“同步回写 overlay”，会迫使第 4 条提前实现第 5 条的 App 接线，或引入随后即删除的过渡层。按存储能力与 CLI 编排分开后，两条 feature 可独立验收且依赖关系不变。

  **受影响的已启动 / 已完成 feature**：无。前三条 done feature 的 codec/base/query 契约均不变；`ipdb-overlay-store` 此前仍为 planned。

- **2026-07-15（ipdb-overlay-store 验收完成）**：

  **落地结果**：模块 C 的独立 overlay backend 已完成并验收，roadmap item 改为 `done`。CLI/App 接线、默认 TTL、三来源选择、live 同步回写和旧 `WriteRecord` 删除仍完整保留在 `ipdb-lookup-integration`，feature DAG 与用户可见行为不变。
