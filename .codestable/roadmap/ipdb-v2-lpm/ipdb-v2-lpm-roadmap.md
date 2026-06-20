---
doc_type: roadmap
slug: ipdb-v2-lpm
status: active
created: 2026-06-20
last_reviewed: 2026-06-20
tags: [ipdb, lpm, pebble, storage, cidr, overlay]
related_requirements: [offline-ip-lookup]
related_architecture: [ip-lookup]
---

# IPDB v2：真正的最长前缀匹配存储重构

## 1. 背景

当前离线库（format version 1）的 Pebble key 只编码网段的 masked 起始地址，prefix length 存在 value 里；单 IP 查询靠"SeekGE 后回看一条前驱再判 Contains"近似最长前缀匹配（LPM）。这套近似算法成立的唯一前提是 **基础库网段互不重叠且有序**——`BuildFromCSV` 在导入阶段强制了这一点（`backend/ipdb/builder.go:162-167`）。

但 ipinfo 在线回写会把单 IP 的 /32（IPv6 /128）写进同一个 keyspace（`internal/cli/ip_merge.go:82` → `backend/ipdb/store.go:85`），打破了"不重叠"前提，导致：

- 被大网段覆盖的 IP，因为最近前驱变成不包含它的 /32 而返回 MISS；
- 回写 /32 与已有网段起始地址相同时直接覆盖原网段记录（永久，重建前不可逆）。

该正确性问题已由 issue `2026-06-20-ipdb-writeback-breaks-lpm` 记录并止血。本 roadmap 做根治：把存储格式升级到 v2，用真正的 prefix-key 结构实现 LPM，并把"不可变基础库"与"运行期在线缓存"物理分离。这是 M3"更新功能"（增量 / 远程刷新库）的前置条件——M3 需要一个能安全承载多来源、可重叠记录的存储层。

## 2. 范围与明确不做

### 本 roadmap 覆盖

- v2 key/value 编码：primary LPM 索引（按 prefix length 组织）+ CIDR 二级索引（按起始地址组织）+ 去掉 value 里的 prefixLen
- 单 IP 查询改为真正的 LPM（逐前缀长度精确查找），重叠记录下结果正确
- CIDR 查询改用二级索引做区间扫描
- base / overlay 物理分离：base 为 CSV 构建的不可变库，overlay 为 ipinfo 单 IP 缓存（带 source / 抓取时间 / 过期时间元数据）
- 回写目标从"写进 base keyspace"改为"写进 overlay"
- format version 升级与 v1 旧库的识别与处理

### 明确不做

- **在线查询语义统一 + 回写生命周期队列**（审计意见 #2：`ipdb-first` 短路、`--offline` / `--verify-online` / `--no-writeback` flag、`WriteBackQueue` + `App.Close` 等待）——这是独立 feat，消费本 roadmap 暴露的 overlay 接口契约，不在此实现。
- **M3 更新功能本身**（增量更新、远程下载 / 校验库、版本比对）——本 roadmap 是其前置存储能力，不实现更新逻辑。
- **`data_source_priority` 策略语义改动**——保持现状（默认 ipdb-first，CIDR 不受控）；只调整底层存储，不改对外优先级行为。
- **overlay 缓存的可视化 / 清理命令**（`ipdb overlay clear` 之类）——记观察项，留待后续。

## 3. 模块拆分（概设）

```
IPDB v2
├── 编码层 codec-v2       ：v2 的 key/value 编解码，定义 primary 与 cidr 两套 key 布局
├── base 存储             ：CSV 构建的不可变库；真 LPM 单 IP 查询 + CIDR 二级索引区间扫描
├── overlay 存储          ：运行期 ipinfo 单 IP 缓存，带 TTL，物理独立于 base
└── 查询编排 + 迁移        ：Store 聚合 base+overlay 对外暴露查询/回写入口；v1 旧库识别与处理
```

### 模块 A · 编码层 codec-v2
- **职责**：定义 v2 的 key/value 二进制布局并提供编解码函数。primary 索引按 `[kind+family][prefixLen][maskedAddr]` 组织（支撑逐前缀长度精确查找），CIDR 二级索引按 `[kind+family][maskedAddr][prefixLen]` 组织（支撑按起始地址区间扫描）。value 去掉 prefixLen（已进 key），可选携带 overlay 元数据。不负责任何查询 / 存储逻辑。
- **承载的子 feature**：`ipdb-v2-codec`
- **触碰的现有代码**：重写 `backend/ipdb/codec.go`、`backend/ipdb/types.go` 常量（新增 kind 字节、`currentFormatVersion=2`）

### 模块 B · base 存储
- **职责**：从 CSV 构建 v2 不可变 base 库（同时写 primary 与 cidr 两套索引）；单 IP 查询用 primary 索引做真正的 LPM；CIDR 查询用二级索引做区间扫描。base 在运行期只读，永不被回写改动。
- **承载的子 feature**：`ipdb-v2-base-lpm`、`ipdb-v2-cidr-index`
- **触碰的现有代码**：`backend/ipdb/builder.go`（写双索引）、`backend/ipdb/store.go`（`LookupIP` / `LookupCIDR` 改写）

### 模块 C · overlay 存储
- **职责**：运行期 ipinfo 单 IP 结果缓存，独立 Pebble keyspace；只存 /32、/128；每条带 source / 抓取时间 / 过期时间；按 IP 精确查找，过期视为未命中。永不被 CSV 构建触碰。
- **承载的子 feature**：`ipdb-overlay-store`
- **触碰的现有代码**：新增 `backend/ipdb/overlay.go`；`internal/cli/ip_merge.go` 回写改写入 overlay

### 模块 D · 查询编排 + 迁移
- **职责**：`Store` 聚合 base + overlay，对 cli 暴露统一的 base 查询、overlay 查询、overlay 写入入口；打开库时按 `Metadata.FormatVersion` 识别 v1/v2，v1 给出明确重建提示（不做数据级自动迁移，CSV 是数据源真相，重建即正确）。
- **承载的子 feature**：`ipdb-lookup-integration`
- **触碰的现有代码**：`backend/ipdb/store.go`（`OpenCurrent` / `Store` 结构 / `Close`）、`internal/cli/ip_lookup.go`、`internal/cli/ip_match.go`、`internal/cli/app.go`

## 4. 模块间接口契约 / 共享协议（架构层详设）

> 以下为 feature-design 的硬约束输入。要改先回 `cs-roadmap update`。

### 4.1 v2 key 布局（codec-v2 → base / overlay）

**方向**：编码层 → base 存储、overlay 存储
**形式**：Pebble key 字节协议 + Go 函数签名

```
kind 字节（高 4 位区分用途，低保留；与 v1 的 0x04/0x06 不冲突，便于版本识别）：
  meta            = 0x00          # 沿用，value 为 JSON Metadata
  primaryV4       = 0x14
  primaryV6       = 0x16
  cidrV4          = 0x24
  cidrV6          = 0x26
  overlayV4       = 0x34
  overlayV6       = 0x36

primary key（LPM 主索引）：[kind][prefixLen:1B][maskedAddr: 4B|16B]
cidr key（二级索引）   ：[kind][maskedAddr: 4B|16B][prefixLen:1B]
overlay key            ：[kind][addr: 4B|16B]        # 只有 /32、/128，无需 prefixLen

函数签名（backend/ipdb/codec.go）：
  func encodePrimaryKey(p netip.Prefix) ([]byte, error)
  func decodePrimaryKey(key []byte) (netip.Prefix, error)
  func encodeCIDRKey(p netip.Prefix) ([]byte, error)
  func decodeCIDRKey(key []byte) (netip.Prefix, error)
  func encodeOverlayKey(a netip.Addr) ([]byte, error)
  func decodeOverlayKey(key []byte) (netip.Addr, error)
```

**约束**：
- `encodePrimaryKey` / `encodeCIDRKey` 入参必须已 Masked（调用方保证），否则返回 error。
- prefixLen 单字节，取值 0–32（IPv4）/ 0–128（IPv6），越界返回 error。
- primary 与 cidr 两套 key 对同一网段必须可互相还原出同一个 `netip.Prefix`。

### 4.2 v2 value 布局（codec-v2 ↔ base / overlay）

**方向**：编码层 ↔ base / overlay 存储
**形式**：value 字节协议 + 函数签名

```
value = [version:1B=2][flags:1B] + 7×(uvarint 长度前缀 + UTF-8 字段)
        + （flags & flagOverlayMeta 时）overlay 元数据段

7 字段顺序固定（与 v1 一致，去掉 prefixLen，prefixLen 已进 key）：
  Country, CountryCode, Continent, ContinentCode, ASN, ASName, ASDomain

flagOverlayMeta = 0x01
overlay 元数据段 = [sourceLen uvarint][source bytes]
                   [fetchedAtUnix: int64 BE][expiresAtUnix: int64 BE]

函数签名：
  func encodeRecordValue(rec Record) ([]byte, error)                       # base 用，flags=0
  func encodeOverlayValue(rec Record, meta OverlayMeta) ([]byte, error)    # overlay 用，置 flagOverlayMeta
  func decodeRecordValue(value []byte) (Record, error)                     # 忽略 overlay 段
  func decodeOverlayValue(value []byte) (Record, OverlayMeta, error)
```

**约束**：
- `Record.Network` 由 key 的 prefix 还原后回填，不进 value。
- decode 遇到 `version != 2` 返回明确错误（供 D 模块识别 v1）。
- base value 必须 `flags=0`；overlay value 必须置 `flagOverlayMeta`。

### 4.3 base 存储查询接口（base → 查询编排）

**方向**：base 存储 → 查询编排 / cli
**形式**：Go 方法签名

```
func (s *BaseStore) LookupIP(addr netip.Addr) (rec Record, matched bool, err error)
  # 真正的 LPM：for L := maxBits(addr); L >= 0; L-- {
  #     key := encodePrimaryKey(PrefixFrom(addr, L).Masked())
  #     if v, closer, err := db.Get(key); err == nil { 命中即返回（最长前缀） }
  # }
  # 全程未命中返回 matched=false

func (s *BaseStore) LookupCIDR(query netip.Prefix) ([]Record, error)
  # 用 cidr 二级索引按起始地址区间扫描；保留一次向前回看以捕获覆盖查询起点的大网段
```

**约束**：
- `LookupIP` 必须返回覆盖该 addr 的**最具体**网段（最长前缀），与是否存在更粗网段无关——这是本 roadmap 的核心正确性目标。
- base 库只读，这两个方法不得写库。
- IPv4 ladder 最多 33 次 Get，IPv6 最多 129 次；可接受（单次 CLI 查询）。

### 4.4 overlay 存储接口（overlay → 查询编排）

**方向**：overlay 存储 → 查询编排 / 回写方
**形式**：Go 类型 + 方法签名

```
type OverlayMeta struct {
    Source    string    // 来源标识，如 "ipinfo"
    FetchedAt time.Time // 抓取时间
    ExpiresAt time.Time // 过期时间；零值表示永不过期
}

func OpenOverlay(rootDir string) (*OverlayStore, error)
func (o *OverlayStore) Get(addr netip.Addr, now time.Time) (Record, OverlayMeta, bool, error)
  # 精确查 /32 或 /128；ExpiresAt 非零且 now 晚于 ExpiresAt 视为未命中
func (o *OverlayStore) Put(addr netip.Addr, rec Record, meta OverlayMeta) error
func (o *OverlayStore) Close() error
```

**约束**：
- overlay 只接受单 IP（隐含 /32 或 /128）；传入其它 prefix 由调用方负责，overlay 不存网段。
- overlay 物理独立于 base 版本目录，base 重建不触碰 overlay；过期由 TTL 兜底。
- `Put` 必须幂等（同 IP 覆盖写）。

### 4.5 Store 聚合与回写入口（查询编排 → cli）

**方向**：查询编排 → cli
**形式**：Go 方法签名

```
func OpenCurrent(rootDir string) (*Store, error)   # 内部打开 base(BaseStore) + overlay(OverlayStore)
func (s *Store) LookupIP(ip string) (Match, error)       # 走 base LPM；Match.Source 不设或置 base
func (s *Store) LookupOverlay(ip string) (Match, bool, error)  # overlay 命中（未过期）
func (s *Store) PutOverlay(ip string, rec Record, meta OverlayMeta) error  # 取代旧 WriteRecord
func (s *Store) LookupCIDR(cidr string) ([]Record, error)
func (s *Store) Close() error                            # 关 base + overlay
```

**约束**：
- **删除 / 废弃旧 `WriteRecord`**——任何路径都不得再向 base keyspace 写单 IP。回写一律走 `PutOverlay`。
- base / overlay 的合并策略（先 overlay 还是先 base、Source 标记）**留在 cli 的 `mergeIPInfo` 层**，本 roadmap 不改其策略语义；Store 只各自暴露查询能力。
- `OpenCurrent` 读到 `Metadata.FormatVersion == 1` 时返回新错误 `ErrLegacyFormat`，cli 据此提示"离线库为旧格式，请重新执行 `geoprism ipdb build --csv <path>` 生成 v2 库"。不做 v1→v2 数据级自动迁移。

### 4.6 共享数据结构 / 状态

```
磁盘布局 v2（~/.geoprism/ipdb/）：
ipdb/
├── CURRENT                       # 内容是当前激活 base 的 buildID（沿用）
├── versions/{buildID}/db/        # base 库（v2 格式，不可变）
└── overlay/db/                   # overlay 缓存（独立 Pebble，跨 base 版本存活）

Metadata.FormatVersion 从 1 升到 2（backend/ipdb/types.go）。
```

## 5. 子 feature 清单

1. **ipdb-v2-codec** — v2 的 key/value 编解码层（primary / cidr / overlay 三套 key + value v2，含 overlay 元数据段），纯编码 + 单测，无对外行为变化
   - 所属模块：模块 A · 编码层
   - 依赖：无
   - 状态：planned
   - 对应 feature：未启动
   - 备注：契约见 §4.1 / §4.2；落地后即可被 B/C 复用

2. **ipdb-v2-base-lpm** — builder 写 v2 primary 索引 + `BaseStore.LookupIP` 改为真正的 LPM ladder + `OpenCurrent` 识别 v1 旧库给重建提示
   - 所属模块：模块 B · base 存储（含 §4.5 的 v1 识别）
   - 依赖：ipdb-v2-codec
   - 状态：planned
   - 对应 feature：未启动
   - 备注：**最小闭环**

3. **ipdb-v2-cidr-index** — builder 增写 cidr 二级索引 + `BaseStore.LookupCIDR` 改用二级索引区间扫描
   - 所属模块：模块 B · base 存储
   - 依赖：ipdb-v2-base-lpm
   - 状态：planned
   - 对应 feature：未启动
   - 备注：依赖 base 库已是 v2 双写格式

4. **ipdb-overlay-store** — 新增独立 overlay 存储（TTL + 元数据）+ 回写改写入 overlay（`PutOverlay`），废弃向 base 写单 IP
   - 所属模块：模块 C · overlay 存储
   - 依赖：ipdb-v2-codec
   - 状态：planned
   - 对应 feature：未启动
   - 备注：落地后正式取代 issue `2026-06-20-ipdb-writeback-breaks-lpm` 的止血

5. **ipdb-lookup-integration** — `Store` 聚合 base+overlay，cli 各路径切到新接口（LookupIP / LookupOverlay / PutOverlay / Close），移除旧 `WriteRecord` 调用
   - 所属模块：模块 D · 查询编排 + 迁移
   - 依赖：ipdb-v2-base-lpm, ipdb-overlay-store
   - 状态：planned
   - 对应 feature：未启动
   - 备注：合并策略仍复用现有 `mergeIPInfo`，不改优先级语义

**最小闭环**：第 2 条 `ipdb-v2-base-lpm` 做完后，可以 `ipdb build` 出 v2 库，并演示"对一个被大网段覆盖、同时存在更具体重叠记录的 IP，单 IP 查询返回正确的最长前缀匹配结果"——这正是本 roadmap 要根治的正确性目标，端到端可验证。

## 6. 排期思路

按"先底座、再正确性、再分离、最后接线"推进：

- 先做 `ipdb-v2-codec`：它是纯编码层，无行为变化、可独立单测，是 B/C 的共同地基；不先定死它，后面两条会各自发明 key 布局导致不一致。
- 第二条 `ipdb-v2-base-lpm` 作最小闭环：它单独就能交付本 roadmap 的核心价值（真 LPM），且不依赖 overlay 即可验证正确性。
- `ipdb-v2-cidr-index` 紧随其后，因为它要在 base 已是 v2 双写格式之上加二级索引。
- `ipdb-overlay-store` 只依赖 codec，可与 base 系并行推进。
- `ipdb-lookup-integration` 收口，依赖 base-lpm 与 overlay 都就绪，把 cli 各路径切到新接口并移除危险的 base 单 IP 写入。

卡点：format version 升级意味着用户现有 v1 库需重建；本 roadmap 选择"明确报错 + 提示重建"而非数据级迁移，因为 CSV 是数据源真相、重建即正确，迁移代码反而是额外维护负担。需在第 2 条落地前确认这一取舍可接受。

## 7. 观察项

- `architecture/ip-lookup.md` 第 4 节"ipinfo 回写只写 /32/128（进同一库）"与"CIDR 查询要回看前一条记录"的描述将被本 roadmap 改变；落地后由 `cs-feat-accept` 回写 architecture，本 roadmap 不直接改。
- 与 issue `2026-06-20-ipdb-writeback-breaks-lpm` 的关系：该 issue 是止血（阻止回写破坏 base 查询），`ipdb-overlay-store` 落地后其止血逻辑可被正式方案取代；届时在 issue fix-note 标注。
- 审计意见 #2（在线查询语义统一 + `WriteBackQueue` 生命周期 + `App.Close` 等待）是独立 feat，依赖本 roadmap §4.4/§4.5 的 overlay 接口；建议本 roadmap 第 4、5 条落地后再启动该 feat。
- overlay 缓存的清理 / 浏览命令（如 `ipdb overlay clear`、过期项回收策略）暂不做，后续若需要可另开 feature 或 req。
- v2 format 升级是否需要在 `Metadata` 里保留 v1 兼容读路径（而非直接报错重建）——取决于用户对"升级即重建"的接受度，第 2 条 design 阶段需用户拍板。
