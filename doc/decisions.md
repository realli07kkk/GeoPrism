# 已确认决策及原因

本文集中记录 GeoPrism 已确认的架构与实现决策（ADR）。要推翻其中任何一条，先读"理由"与"替代方案"，再改代码并更新本文。编号 `D-xx` 被其它文档引用。

格式：日期 / 状态 / 背景 / 决定 / 理由 / 否决的替代方案 / 后果 / 代码落点。

---

<a id="d-01"></a>
## D-01 base 只读，在线回写走独立 overlay

- **日期**：2026-06-20　**状态**：已落地（base 只读）；overlay 接入 CLI 仍待 `ipdb-lookup-integration`
- **背景**：v1 base 以"网段不重叠"为前提做近似 LPM；ipinfo 结果异步回写 `/32`、`/128` 进 base keyspace 后，同起始地址的 `/32` 遮蔽 `/24`（假 MISS），且 key 不含 prefixLen 导致 `/24` 被永久覆盖（见 [history.md](history.md#writeback)）。
- **决定**：ipdb base keyspace **运行期永久只读**；任何在线回写只能进物理独立的 overlay（`ipdb/overlay/db`），与 base 版本目录分离；CIDR 查询只查 base；废弃 `WriteRecord` 的写语义。
- **理由**：根因是"运行期写绕过了构建期不变量"，让写入路径物理消失是最直接的修复；overlay 承接"缓存在线结果"功能不丢失；overlay 损坏不影响 base，故障边界清晰；为 v2 真 LPM 铺路。
- **否决**：
  - 写入前 guard（校验不重叠）——治标，仍允许污染 base，且每次写入都要扫描。
  - 查询回溯（`SeekGE+Prev` 多回看几条）——复杂度上升，仍无法恢复已被覆盖的记录。
- **后果**：第一层止血 = 删除 `maybeWriteBack` / `writeIPInfoRecord`，所有回写停用；`Store.WriteRecord` 显式返回只读错误；v1 存量污染库需重建；overlay 接入 CLI 是独立工作。
- **代码落点**：`backend/ipdb/store.go` `Store.WriteRecord`；`backend/ipdb/overlay.go`；`internal/cli/ip_lookup.go` `LookupIP` 注释；`internal/cli/ip_merge.go`。

<a id="d-02"></a>
## D-02 标准库 flag，不引入 CLI 框架

- **日期**：项目初期　**状态**：现行
- **决定**：`os.Args` + `flag.NewFlagSet` 实现子命令与快捷查询；`reorderArgs` 把 flag 挪到位置参数前，使 `geoprism example.com -t AAAA` 与 `geoprism -t AAAA example.com` 等价。
- **理由**：子命令数量少（5 个），无动态补全等高级需求；零第三方 CLI 依赖，与"纯 Go、易交叉编译"一致；显式路由代码更易被 Agent 读懂。
- **后果**：flag 解析细节需自己维护（如 `hasJSONFlag` 在 `Parse` 前探测以保证错误走 JSON 协议）；新增子命令要同时改 `knownCommands` / `usage` / `Main` 的 `switch`。
- **代码落点**：`internal/cli/main.go` `Main`；`internal/cli/app.go` `runQuery` / `reorderArgs`。

<a id="d-03"></a>
## D-03 选 Pebble 而非 SQLite

- **日期**：项目初期（2026-11 补录为正式决策）　**状态**：现行
- **决定**：离线 IP 库用 `github.com/cockroachdb/pebble/v2`（LSM KV），不用 SQLite / CGO 方案。
- **理由**：CIDR 查询需要按起始地址做区间扫描，LSM 的有序 key 迭代天然适配；纯 Go、`CGO_ENABLED=0` 即可交叉编译到 linux / darwin / windows（`build.sh` / `build.ps1`）；无需嵌入 SQL 层。
- **后果**：需要自己管 key 编码、metadata、版本目录与文件锁（见 [ipdb-v2.md](ipdb-v2.md)）；Pebble 日志需用 `silentLogger` 屏蔽以免污染 CLI 输出。
- **代码落点**：`backend/ipdb/*`；`backend/ipdb/logger.go`。

<a id="d-04"></a>
## D-04 render 双协议

- **日期**：项目初期　**状态**：现行
- **决定**：TTY 下用 lipgloss 增强表格；非 TTY 保持纯文本 tabwriter 表格；JSON 由 `render.WriteJSON` 统一输出。`render` 只依赖 `*Source` 接口，不 import `internal/cli`。
- **理由**：非 TTY 输出是重定向 / 管道 / 脚本的协议，必须稳定；TTY 增强不影响脚本；接口化便于 render 独立测试。
- **后果**：新增输出列要同时改 TTY 与非 TTY 两条路径；非 TTY 列 / 分隔符变更属破坏性变更。
- **代码落点**：`render/style.go` `isTTY`；`render/output.go`；`render/query.go` `WriteQueryResult`。

<a id="d-05"></a>
## D-05 CIDR 查询固定 ipdb 优先

- **日期**：项目初期　**状态**：现行
- **决定**：`geoprism <cidr>` 直接查 ipdb 返回所有相交网段，**忽略** `data_source_priority`；只有 ipdb 完全未命中（或不可用）时，才以 CIDR 基地址走一次单 IP 查询作为 `fallback`。
- **理由**：CIDR 是"看一片 IP 的离线归属"，批量在线查询不现实也不必要；基地址回退保留了"至少给一个答案"的 ipinfo 兜底。
- **后果**：`data_source_priority` 只影响单 IP 合并；文档 / 用户预期需明确这一点。
- **代码落点**：`internal/cli/cidr_lookup.go` `LookupCIDR`。

<a id="d-06"></a>
## D-06 查询结果与 Provider 列表保序

- **日期**：2026-06-20　**状态**：已落地
- **背景**：`QueryMulti` 用 `append` 收集 goroutine 结果，`providers` / `test --all` 遍历 map，`ProviderStore.save` 还 `sort.Strings(ids)` 永久改写声明顺序——JSON 输出每次运行顺序不同，与"JSON 面向 AI agent / 脚本"冲突（见 [history.md](history.md#order)）。
- **决定**：
  - `QueryMulti` 预分配 `Answers[len(providers)]`，按 index 归位；成功 / 失败 / 空响应都占据原 index（`normalizeAnswer`）；不过滤、不排序。
  - `ProviderStore` 增加 `order []string`（= TOML `[[providers]]` 声明顺序）作为 canonical 顺序；`List` / `GetEnabled` / `save` 都按它；`Upsert` 已存在保位、新增追加；`Delete` 同步移除。
  - 升级语义：只对升级后新 load / save 保序，不回填历史已被打乱的顺序。
- **理由**：用户在 TOML 里的声明顺序就是期望的展示顺序；index 归位是零排序成本的确定性方案。
- **否决**：完成后按某键排序（需要额外排序键，且与声明顺序不一致）；继续按 ID 排序（已被证明会永久覆盖用户顺序）。
- **后果**：`Provider.name` 同名时 `-p` 取第一个（唯一性未校验，见 [dns-query.md §11](dns-query.md#open)）；`ns_info.go` `queryNSIPs` 也采用 index 写法。
- **代码落点**：`backend/resolver/resolver.go` `QueryMulti` / `normalizeAnswer`；`backend/provider/provider.go` `ProviderStore`。

<a id="d-07"></a>
## D-07 NS zone 沿 label 向上探测，最具体优先

- **日期**：项目初期（2026-11 补录为正式决策）　**状态**：现行
- **决定**：不猜 apex。对 `a.b.example.com` 生成候选 `[a.b.example.com, b.example.com, example.com]`（不含 TLD），并行查 NS，按候选顺序取第一个能提取出 NS 名称的作为 `resolved_zone`。错误优先级 NXDOMAIN > SERVFAIL > REFUSED > 其它；只要任一候选 NOERROR 且无记录，整条链判为"未找到可用 NS zone"。
- **理由**：`www.example.com` 这类名字的 zone apex 无法预先得知，向上逐级探测是唯一可靠办法；并行降低延迟；"NOERROR 无记录"说明中间层不是 zone 而非域名不存在。
- **后果**：每次域名查询额外发起 `候选数 + 2×NS数` 次 Provider 查询；接受的成本。
- **代码落点**：`internal/cli/ns_info.go` `discoverNSZone` / `buildNSZoneCandidates` / `selectNSDiscoveryError`。

<a id="d-08"></a>
## D-08 v2 构建对重复 prefix 严格拒绝

- **日期**：2026-06-22　**状态**：已落地
- **背景**：v2 放开"不同 prefix 可重叠"后，同 family 内 `Masked()` 完全相同的 prefix 会得到**相同的 primary key**，`Set` 直接覆盖前一条记录，且不可追溯。
- **决定**：构建时检测完整 primary key 重复，直接失败 `ErrDuplicatePrefix`；错误含规范 prefix、重复行号、首次出现行号。**不同 prefix 重叠允许**（`10.0.0.0/8` + `10.1.0.0/16`）。保留"family 内起始地址非递减"输入契约，但它只是构建期契约，不再是查询正确性前提。重复比较的是完整 prefix（含 prefixLen），不是地址。
- **理由**：静默覆盖等于数据丢失；CSV 是数据真相，重复即上游数据问题，应在构建期暴露；行号信息可直接定位。
- **否决**：first-wins / last-wins（隐藏问题）；key 加 source 区分（同一网段多来源属业务决策，不该由存储层默默承担）；静默去重（同上）。
- **后果**：重复数据必须在上游 CSV 清理后才能构建。
- **代码落点**：`backend/ipdb/builder_v2.go` `writeV2Records` / `ErrDuplicatePrefix`。

<a id="d-09"></a>
## D-09 CIDR 二级索引用零长度 value

- **日期**：2026-06-22　**状态**：已落地
- **背景**：CIDR 查询按起始地址扫二级索引，索引条目需要决定 value 存什么。
- **决定**：cidr key 的 value 为**零长度**；canonical `Record` 只存在 primary；`LookupCIDR` 从 cidr key 解出 prefix，再 `encodePrimaryKeyV2` 回查 primary 取值；cidr 有、primary 无 → `ErrCorruptIndex`（不静默跳过）；primary 与 cidr 同一 batch 原子写；不变量 `primaryCount == cidrCount == Metadata.RowCount`；预留 `SchemaFeatureCIDRInlineValue` 以备未来内联。
- **理由**：避免双份 Record 的存储与一致性成本；Pebble 对空 value 开销最小；CIDR 查询不是热路径，回查代价可接受。
- **否决**：value 存 primary key 指针（与零长度等价但多存储）；value 复制整份 Record（双倍体积 + 双写不一致风险）。
- **后果**：CIDR 查询 = 一次索引扫描 + N 次 point Get；索引不一致必须显式报错。
- **代码落点**：`backend/ipdb/store_v2.go` `fetchRecordByPrefix` / `ErrCorruptIndex`；`backend/ipdb/builder_v2.go` 双写。

<a id="d-10"></a>
## D-10 codec 拒绝 IPv4-mapped IPv6

- **日期**：2026-06-22　**状态**：已落地
- **背景**：`netip.Addr.Is6()` 对 `::ffff:x.x.x.x`（`Is4In6()==true`）也返回 true；若接受，它会被编码为 V6 key，而 `LookupIP(v4addr)` 走 V4 ladder 永远查不到。
- **决定**：所有 codec 入口（encode 与 decode）一律拒绝 `Is4In6()`；`validatePrefixV2` 与 `decodePrefixV2` 对称；overlay key 同样拒绝。严格拒绝优先于规范化。
- **理由**：family 二义无法在存储层可靠消除；数据源（ipinfo Lite CSV）天然不产生 `::ffff`；严格拒绝让问题在构建期暴露而非查询期静默 MISS。
- **否决**：decode 接受并 `Unmap()` 规范化——掩盖上游数据问题，且 encode / decode 不对称。
- **后果**：CSV 含 `::ffff:` 会构建失败；overlay `Put` 同样拒绝。
- **代码落点**：`backend/ipdb/codec.go` `validatePrefixV2` / `decodePrefixV2` / `encodeOverlayKeyV2`。

<a id="d-11"></a>
## D-11 解码对称校验 + `uint64→int` 上界检查，损坏数据返回 error 不 panic

- **日期**：2026-06-22　**状态**：已落地
- **背景**：schema feature 的 CR-001 / CR-002：decode key 缺 `IsValid` / family 上限 / masked 校验，坏 key 被当成功；`binary.Uvarint` 返回 `uint64`，`int(fieldLen)` 溢出为负后边界检查失真，坏 value 导致 panic。
- **决定**：
  - 所有 `uint64` / `uint32 → int` 转换前先做上界检查：`addLenOffset(offset, fieldLen, valueLen)` 用 `uint64` 比较确保不越过 value 末尾。
  - decoder 对任何损坏数据返回 error，不得 panic。
  - decode 必须重新校验 encode 时的**全部**协议不变量（`decodePrefixV2` ↔ `validatePrefixV2` 对称；key 长度精确；value `flags==0` 且无多余尾部）。
- **理由**：测试全绿不等于守住边界；只看 encode 会漏掉 decode；stdlib 越界不报错（`PrefixFrom` 返回 invalid Prefix，`Uvarint` 不报溢出）。
- **否决**：转 `int64` 中转（仍可能溢出）；"信任合法数据"（外部字节一律不可信）。
- **后果**：适用于任何外部字节 decode；负例矩阵进入 `codec_v2_test.go`。
- **代码落点**：`backend/ipdb/codec.go` `decodeRecordFields` / `addLenOffset` / `decodePrefixV2`。

<a id="d-12"></a>
## D-12 格式升级即重建，不做数据级迁移

- **日期**：2026-06-22（roadmap 卡点）　**状态**：现行
- **决定**：`FormatVersion` 升级时不写 v1 → v2 迁移代码，不保留 v1 读路径；`OpenCurrent` 读到 `FormatVersion != 2` 返回 `ErrLegacyFormat`，CLI 提示 `ipdb build` 重建。
- **理由**：CSV 是数据真相，重建即正确；迁移代码是额外维护负担且难以验证；避免长期背着 v1 兼容分支。
- **后果**：升级后首次运行会看到一条重建提示（stderr），IP 匹配在重建前跳过；DNS 查询不受影响。
- **代码落点**：`backend/ipdb/store.go` `OpenCurrent`；`internal/cli/ip_lookup.go` `ipdbLookupError`；`internal/cli/app.go` `recordIPDBInitError`。

<a id="d-13"></a>
## D-13 v2 切换原子性与 `Store` 过渡壳

- **日期**：2026-06-22　**状态**：已落地（过渡壳待 integration 拆除）
- **决定**：`ipdb-v2-base-build` 只提供内部入口（不激活）；`ipdb-v2-query` 一次性原子切换公开入口：`BuildFromCSV → buildV2FromCSV`、`OpenCurrent` 内部 probe 判版后转 `BaseStore`、`currentFormatVersion → 2`。引入 `Store{base *BaseStore}` 过渡壳，保持 cli 调用点类型与方法签名零 diff；`OpenCurrentBase` 公开 API 推迟到 integration。
- **理由**：不允许出现"能构建 v2 但当前程序不能查询 v2"的中间态；过渡壳把 cli 改动面压到最小，降低收口风险。
- **后果**：`Store.WriteRecord` 保留签名但只读拒绝；`Store` 上的 `rootDir` / `buildID` 是镜像字段，integration 时清理。
- **代码落点**：`backend/ipdb/builder.go` `BuildFromCSV`；`backend/ipdb/store.go` `OpenCurrent` / `Store`；`backend/ipdb/types.go` `currentFormatVersion`。

<a id="d-14"></a>
## D-14 并发构建的锁序与 staging 隔离

- **日期**：2026-07-10　**状态**：已落地
- **背景**：并发 `ipdb build` 共用固定 staging 目录与 `CURRENT.tmp`，且 GC 无协调，可能互相删除在用目录、留下悬空 `CURRENT`（见 [history.md](history.md#concurrent-build)）。
- **决定**：
  - `BUILD.lock` 独占覆盖"清理 staging → 构建 → 发布 → 回收"全程；锁序固定 `BUILD(EX) → VERSIONS(EX)`。
  - `VERSIONS.lock`：reader（`OpenCurrent` → `Close`）共享；发布与回收独占，等待旧 reader 退出并阻止新 reader 进入。
  - staging 用 `os.MkdirTemp` 独占目录；`CURRENT` 用 `CreateTemp + Sync + Rename`；发布后重读校验 `CURRENT` 再有界回收；默认 BuildID 在锁内分配并带数字后缀。
  - Unix `flock` 对 `EINTR` 重试；Windows `LockFileEx`；其它平台显式报错。
- **理由**：固定路径在并发下必然冲突；GC 必须与 reader 协调否则删掉在用版本；临时文件 + rename 是 `CURRENT` 原子切换的标准做法。
- **否决**：单一锁（reader 会被整个构建期阻塞）；不做 GC（磁盘无限增长）。
- **后果**：调用方若持有 `OpenCurrent` 的 `Store`，必须先 `Close` 再 `BuildFromCSV`，否则发布阶段会等待；新旧二进制混跑没有锁协议；NFS / SMB 上 `flock` 无保证；`rootDir` 需可写。
- **代码落点**：`backend/ipdb/builder_v2.go` `buildV2FromCSV`；`backend/ipdb/store.go` `OpenCurrent`；`backend/ipdb/file_lock_*.go`。
