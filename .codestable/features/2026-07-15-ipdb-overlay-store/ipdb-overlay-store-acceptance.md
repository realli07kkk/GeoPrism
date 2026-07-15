---
doc_type: feature-acceptance
feature: 2026-07-15-ipdb-overlay-store
status: passed
summary: ipdb-overlay-store backend-only 能力验收通过；独立 OverlayStore、TTL、自愈 quarantine、单 owner 生命周期锁、durable Put、并发与 base 物理隔离均落地，CLI 接线继续留给 integration
tags: [ipdb, overlay, pebble, ttl, quarantine, lock, acceptance]
---

# ipdb-overlay-store 验收报告

> 阶段：阶段 3（验收闭环）
> 验收日期：2026-07-15
> 关联方案 doc：`.codestable/features/2026-07-15-ipdb-overlay-store/ipdb-overlay-store-design.md`

## 1. 接口契约核对

对照方案第 2.1 节名词层逐一核查。

**接口示例逐项核对**：

- [x] `OpenOverlay(rootDir) (*OverlayStore, error)`：首次创建、兼容库重开、忙锁 fail-fast 和损坏库重建均在 `backend/ipdb/overlay.go` 落地；对应 `TestOpenOverlay*` 测试组通过。
- [x] `OverlayStore.Put(addr, record, meta) error`：校验 addr/Network/保留时间值，使用 `pebble.Sync` 完整覆盖同 IP；`TestOverlayPutGetRoundTripIPv4AndIPv6`、`TestOverlayPutLastWriteWins`、`TestOverlayMetadataAndPutUseSynchronousWrites` 通过。
- [x] `OverlayStore.Get(addr, now) (Record, OverlayMeta, bool, error)`：point Get、Network `/32`/`/128` 回填、显式 now TTL、零值 miss 与坏 value 自愈均与示例一致。
- [x] `OverlayStore.Close() error`：DB 先于生命周期锁关闭，合并两侧错误，nil/重复 Close 安全，关闭后 Get/Put 返回 `ErrOverlayClosed`。
- [x] `ErrOverlayLocked` / `ErrOverlayClosed`：均为稳定 sentinel，可由 `errors.Is` 判定。

**名词层“现状 → 变化”逐项核对**：

- [x] 新增库级 `OverlayMetadata{FormatVersion, CreatedAt}`，与记录级 `OverlayMeta{Source,FetchedAt,ExpiresAt}` 分离；grep 未发现混用。
- [x] 新增 `OverlayStore`，未聚合进过渡 `Store`，也未修改 `BaseStore` / `OpenCurrent` / `WriteRecord` 公共结构。
- [x] 复用 schema feature 已有 overlay key v2/value v1 codec；value 不保存 `Record.Network`，由 Get 据 key 回填。
- [x] 跨平台锁新增 package-private `tryAcquireFileLock`；现有 `acquireFileLock` 调用签名与阻塞语义不变。

**流程图核对**：Open 取锁→首次创建/既有库校验→兼容返回/无损报错/quarantine 重建，Get 复制 value→关 closer→解码/TTL/删除，Put durable Set，Close DB→lock；所有节点均有生产代码和自动化证据。

**结论：无接口偏差。**

## 2. 行为与决策核对

对照方案第 1 节、第 2.2 节与第 2.3 节。

**需求摘要逐项验证**：

- [x] IPv4/IPv6 跨关闭重开 round-trip，7 个业务字段、OverlayMeta 与 UTC 秒精度保持。
- [x] TTL before/equal/after/zero 边界确定；等于 ExpiresAt 即过期，清理失败返回 zero miss + error。
- [x] 单条截断/错误版本 value 只删除自身，不拖垮其它记录。
- [x] metadata/Open corruption 保留旧证据后重建；普通 I/O、close、rename 和运行期 corruption 不误隔离。
- [x] 同句柄 Get/Put/Close 串行，跨进程只允许单 owner，锁冲突不等待。
- [x] base 连续重建期间及 overlay 重开后，overlay 数据保持可用。

**明确不做与反向核对**：

- [x] `internal/cli` / `App` 无实现 diff；没有 overlay 查询、live 回写、三来源选择或 CLI 输出变化。
- [x] 没有默认 TTL、`data_source_priority` 改动、异步队列或 warning 编排。
- [x] 没有修改 `Store` / `BaseStore` / `OpenCurrent` / `WriteRecord`，base keyspace 不含 overlay key。
- [x] 没有 CIDR/LPM overlay API、清理命令、配置、定时器、容量上限或 LRU。
- [x] 实现阶段未修改 architecture；验收阶段仅按 CodeStable 归并当前 backend 状态，没有把 CLI 三来源流程写成已生效，integration 边界不变。

**关键决策与流程级约束**：

- [x] 独立目录与独立 metadata/version；base 构建不扫描 overlay。
- [x] 外层 `OVERLAY.lock` 覆盖 Open、metadata 校验、quarantine、重建直到 Close；仅明确 busy code 映射 locked sentinel。
- [x] 只有 Open/metadata 阶段确认的损坏自动 quarantine；所有 reader/DB 成功关闭后才 rename。
- [x] quarantine 名称 Windows-safe 且以 `Lstat` 避免 dangling symlink 占位冲突；历史证据不自动清理。
- [x] Get/metadata 均在 closer 有效期内复制自有 bytes，关闭后才解析。
- [x] Put 使用 durable Sync、同 IP last-write-wins，不补默认 TTL。
- [x] Close DB→lock 顺序、错误上下文和幂等语义一致。

**挂载点反向核对**：

- [x] M1：`backend/ipdb` 公共契约仅 `OpenOverlay` 与 `OverlayStore.Get/Put/Close`。
- [x] M2：磁盘挂载仅 `ipdb/overlay/OVERLAY.lock`、`db`、quarantine sibling 与库内 `OverlayMetadata`。
- [x] 反向 grep：生产代码中 `OpenOverlay` / `OverlayStore` 无 CLI 或其它 package 调用；`tryAcquireFileLock` 只由 overlay 使用，未发现清单外挂载点。
- [x] 拔除沙盘：移除 `overlay.go`、三平台非阻塞锁入口及 `ipdb/overlay/` 数据后，本 feature 完整消失；既有 schema codec 可独立保留。

**结论：无未处理偏差。**

## 3. 验收场景核对

对照方案第 3 节 15 个关键场景：

- [x] **S1 首次 Open/metadata**：`TestOpenOverlayCreatesMetadataAndReopens`、`TestOpenOverlayMetadataWriteFailureClosesDBBeforeLock`。
- [x] **S2 IPv4/IPv6 跨重开**：`TestOverlayPutGetRoundTripIPv4AndIPv6`。
- [x] **S3 last-write-wins**：`TestOverlayPutLastWriteWins`。
- [x] **S4 TTL 与删除失败**：`TestOverlayGetTTLBoundaries`、`TestOverlayCleanupFailureReturnsZeroMissAndError`。
- [x] **S5 截断/错误版本 value 自愈**：`TestOverlayGetDeletesCorruptValueWithoutAffectingOtherRecords`。
- [x] **S6 metadata/Open corruption 与 fresh DB**：`TestOpenOverlayQuarantinesInvalidMetadata`、`TestOpenOverlayQuarantinesPebbleCorruptionDuringOpenOrMetadataRead`、唯一名与 dangling symlink 用例。
- [x] **S7 普通错误不隔离**：`TestOpenOverlayDoesNotQuarantineOrdinaryOrCloseErrors`、fresh rebuild/metadata 失败证据保留用例、runtime corruption 用例。
- [x] **S8 单 owner fail-fast 与释放后重开**：进程内锁测试和 `TestOpenOverlayLockAcrossProcessesAndProcessExit`。
- [x] **S9 锁住的损坏库不隔离**：`TestOpenOverlayDoesNotQuarantineLockedCorruptMetadata`。
- [x] **S10 Close/并发**：重复 Close/closed sentinel、Get↔Put、Get↔Close、Put↔Close 的确定性 barrier 测试。
- [x] **S11 not found 与非法 Get**：`TestOverlayGetNotFoundReturnsZeroMiss`、存储调用计数断言。
- [x] **S12 非法 Put 不写/不覆盖**：invalid/mapped/zoned、Network mismatch、Unix 0 保留值及 sentinel 保持测试。
- [x] **S13 durable Put**：同步写断言 + Put→Close→reopen 仍可读。
- [x] **S14 base 重建隔离**：`TestBaseRebuildDoesNotTouchOpenOverlay` 连续两次构建与重开。
- [x] **S15 支持平台与锁语义**：macOS 实测；Linux/Windows 交叉构建；base 全量测试保持；Unix/Windows 源码仅映射明确 busy code。

**独立质量证据（非缓存）**：

- `GOTOOLCHAIN=local go test -mod=readonly -count=1 ./...`：通过，`backend/ipdb` 28.274s、`internal/cli` 25.396s。
- `GOTOOLCHAIN=local go test -mod=readonly -race -count=1 ./backend/ipdb`：通过，46.298s。
- `GOTOOLCHAIN=local go vet -mod=readonly ./...`：通过。

## 4. 术语一致性

- `base`：始终表示 CSV 构建、版本化、运行期只读的 v2 库；本 feature 未改变其公共结构。
- `overlay` / `OverlayStore`：只表示独立单 IP 缓存 backend；没有被写成已接入 CLI 的来源。
- `OverlayMeta` / `OverlayMetadata`：记录级与库级含义、类型名和存储位置一致。
- 生命周期锁 / quarantine / 机会性删除：代码、测试、roadmap 与架构归并用词一致。
- 禁用旧概念名 `PutOverlay`：生产代码与当前活跃 roadmap 无命中，统一为 `OverlayStore.Put`。

**结论：术语一致，无残留命名偏差。**

## 5. 架构归并

对照方案第 4 节，采用“记录 backend 当前真相、不提前重写 CLI 主流程”的最小归并：

- [x] `architecture/ARCHITECTURE.md`：新增“IPDB v2 与 overlay backend 当前状态”，记录 base/overlay 物理分离、公共接口、TTL、生命周期锁、quarantine 与磁盘布局，并明确尚未接入 App/CLI。
- [x] `architecture/ip-lookup.md`：在当前状态覆盖节追加 OverlayStore 已落地与运行期回写仍停用的事实；§1–§6 旧主流程继续标注待 integration 全面重写。
- [x] `attention.md`：现有测试门禁和 ipdb/base keyspace 约束已覆盖本 feature，没有新的“每个 feature 都会撞一次”规则需要写入。

这次 architecture diff 属 acceptance 归并产物，不是实现阶段扩大范围；没有改写用户可见编排或占用 `ipdb-lookup-integration` 的职责。

## 6. requirement 回写

方案 frontmatter 指向 `requirements/offline-ip-lookup.md`，其状态为 `current`。

**判定：requirement 未变，无需更新。** 本 feature 只提供后续 integration 消费的 backend 存储能力，不改变当前用户故事、CLI 行为、来源优先级或回写可用性；提前把 overlay 写成用户已可感能力反而会失真。用户视角的能力边界由 `ipdb-lookup-integration` 落地后再刷新。

## 7. roadmap 回写

方案 frontmatter：`roadmap: ipdb-v2-lpm`、`roadmap_item: ipdb-overlay-store`。

- [x] `ipdb-v2-lpm-items.yaml`：确认原状态 `in-progress` 且 feature 匹配，现已更新为 `done`，notes 写入自动化证据与 integration 边界。
- [x] `ipdb-v2-lpm-roadmap.md`：第 4 条同步更新为 `done`，观察项和变更日志记录 backend 已验收、CLI 接线仍在第 5 条。
- [x] 两份 roadmap 文档 YAML/frontmatter 校验通过。

## 8. attention.md 候选盘点

**无候选。** 本次没有暴露新的通用编译命令、服务启动、凭证或仓库路径陷阱；必须执行的 Go 质量门、IPv4-mapped IPv6 判定和 base 禁止运行期写入均已存在于 `attention.md`。Windows kernel 运行时锁覆盖属于 CI 矩阵改进，不是 attention 启动必读规则。

## 9. 遗留

**后续 feature 已有归属**：

- `ipdb-lookup-integration`：App 持有 base/overlay 双句柄、overlay 查询、默认 TTL、三来源选择、live 同步 `OverlayStore.Put`、降级 warning 与删除旧 `WriteRecord`。
- overlay clear/浏览、主动容量/LRU、quarantine 自动清理仍明确不做；有需求时另开 feature。
- `backend/ipdb` 若长期继续膨胀，可单独设计 codec/base/overlay package 重划；不阻塞当前验收。

**已知限制 / 环境证据边界**：

- Windows 锁语义通过源码映射、官方 API 语义和交叉构建核对，未在真实 Windows kernel 上执行跨进程冲突测试；建议 CI 补 Windows job。
- 本机工具链高于 `go.mod` 的 Go 1.23；代码使用的 `errors.Join`、`clear`、`sync.Mutex.TryLock` 均不晚于 Go 1.23，仍建议 CI 用 Go 1.23 复核。
- Solaris 构建受既有 Pebble v2.1.4 平台兼容问题阻断，非本次 diff 引入，本报告不宣称 Solaris 已支持。

**验收结论：通过。**
