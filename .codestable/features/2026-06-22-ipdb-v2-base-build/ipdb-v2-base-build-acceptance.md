# ipdb-v2-base-build 验收报告

> 阶段：阶段 3（验收闭环）
> 验收日期：2026-06-22
> 关联方案 doc：`.codestable/features/2026-06-22-ipdb-v2-base-build/ipdb-v2-base-build-design.md`

## 1. 接口契约核对

对照方案第 2.1 节名词层逐一核查（全部一致）：

**接口示例逐项核对**：
- [x] `buildV2FromCSV(rootDir, opts)`（`builder_v2.go:38`）：不同 prefix 重叠 `10.0.0.0/8`+`/16`+`10.1.0.0/16` → 构建成功 3 primary+3 cidr → `TestBuildV2AllowsDistinctOverlap` 实测一致；完全相同 prefix → `ErrDuplicatePrefix`+行号 → `TestBuildV2RejectsDuplicatePrefix` 实测一致。
- [x] `openBaseV2(rootDir, buildID)`（`store_v2.go:33`，CR-001 修复后签名）：内部拼 `rootDir/versions/{buildID}/db` ReadOnly 打开 → `*BaseStore{rootDir,buildID,dbDirPath,metadata}` → `TestOpenBaseV2Success` 实测字段填充 + metadata 一致；`db.Set` → error → `TestOpenBaseV2IsReadOnly` 一致。

**名词层"现状 → 变化"逐项核对**：
- [x] `formatVersionV2 byte=2`（`types.go:13`）：新增，`currentFormatVersion` 仍 1（grep 确认）。
- [x] `stagingDirPrefix=".staging-"`（`types.go:22`）：新增。
- [x] `ErrDuplicatePrefix`（`builder_v2.go:23`）：新增 sentinel，置于 builder_v2.go（design 示例"二选一"已涵盖）。
- [x] `BaseStore{rootDir,buildID,dbDirPath,db,metadata}`（`store_v2.go:15`）：全字段，与 design 名词层一致（CR-001 修复后补齐 rootDir/buildID）。
- [x] `writeV2Records`（`builder_v2.go:123`）：design 未列的内部拆分 helper（非对外概念，不入第 0 节术语表），抽出以隔离 step3 staging 改动。
- [x] v1 `BuildFromCSV`/`Store`/`OpenCurrent`/`WriteRecord`/`currentFormatVersion`/全部 codec：零改动（`git diff` 空确认）。

**流程图核对**（第 2.2 节 mermaid）：
- [x] 图中节点 `mkdir .staging`（`builder_v2.go:58-65`）/ 双写 primary+cidr（`:233-240`）/ 乱序·重复 reject（`:198-207`）/ commit（`:255-262`）/ rename（`:106`）/ writeCurrentVersion（`:113`）/ cleanupOldVersions（`:118`）均有代码落点（grep 确认）。

无偏差。

## 2. 行为与决策核对

对照方案第 1 节 + 第 2.2 节：

**需求摘要逐项验证**：
- [x] 同 batch 双写 primary+cidr、cidr 零长度 value → `TestBuildV2FromCSVDualIndex`（primaryCount==cidrCount==RowCount，cidr value 0 字节）。
- [x] staging 原子构建（关库 rename 切 CURRENT）→ `TestBuildV2StagingSuccess`。
- [x] 相同 prefix reject、允许不同 prefix 重叠 → `TestBuildV2RejectsDuplicatePrefix` + `TestBuildV2AllowsDistinctOverlap`。
- [x] `BaseStore` ReadOnly 打开读 metadata → `TestOpenBaseV2Success` + `TestOpenBaseV2IsReadOnly`。
- [x] FormatVersion=2 + SchemaFeatures=primary|cidr → `TestBuildV2FromCSVDualIndex` 断言 meta。

**明确不做逐项核对**（第 3 节反向核对项 18-22，grep 确认）：
- [x] `currentFormatVersion` 仍 `byte = 1`（`types.go:10`）。
- [x] v1 `builder.go`/`store.go`/`internal/` 零 diff；公开 `BuildFromCSV`/`OpenCurrent`/`WriteRecord` 签名零 diff。
- [x] `BaseStore` 无 `LookupIP`/`LookupCIDR`（grep 空）。
- [x] 无 `ErrLegacyFormat`/`ErrIncompleteSchema`/`ErrCorruptIndex` 定义/使用（仅 store_v2.go 注释说明归属，非定义/使用）。
- [x] 无 overlay 实现（归 ipdb-overlay-store）。

**关键决策落地**：
- [x] #1 `formatVersionV2` 独立常量、`currentFormatVersion` 不动 → `types.go:13`。
- [x] D1 `openBaseV2` 仅 ReadOnly+读 metadata+`FormatVersion==2` sanity（非 sentinel）、不做 capability 拒绝/v1 识别 → `store_v2.go:61-64`（`fmt.Errorf` 非 sentinel）。
- [x] D2 完整 staging→rename→切 CURRENT → `buildV2FromCSV:58-118`。
- [x] D4 保留乱序 reject、删 overlap reject、新增 duplicate reject（比较完整 prefix）→ `writeV2Records:198-207`。
- [x] D5 复用 CSV 解析/family 统计/`prefixLastAddr`/`writeCurrentVersion`/`cleanupOldVersions`、不抽 v1/v2 共用层 → builder_v2.go 直接调 v1 helper。

**编排层"现状 → 变化"逐项核对**：
- [x] 单 `batch.Set` → 同 batch 双 `Set`（`writeV2Records:233-240`）。
- [x] overlap reject 删除、改 duplicate reject（`:198-207`）。
- [x] 构建目标 `.staging-{buildID}` 关库后 rename（`buildV2FromCSV:58-106`）。
- [x] meta 写 FormatVersion=2 + SchemaFeatures（`writeV2Records:261-272`）。

**流程级约束核对**：
- [x] 双索引原子性：同一行 primary+cidr 两个 Set 之间不判 commit（`:233-247`）→ `TestBuildV2DualWriteNotSplitByBatch`（commitSize=2 计数仍相等）。
- [x] staging 原子性：错误路径 `success`/`cleanupDir` defer 清理（`buildV2FromCSV:78-90`）→ `TestBuildV2StagingCleanupOnFailure`。
- [x] CURRENT 切换在关库+rename 之后（`:100-118`）。
- [x] base ReadOnly（`store_v2.go:36-39`）→ `TestOpenBaseV2IsReadOnly`。
- [x] family 统计 RowCount=IPv4Count+IPv6Count=primaryCount=cidrCount（`writeV2Records:248-253`）。

**挂载点反向核对（可卸载性）**——对照第 2.3 节"无用户/系统可见挂载点"：
- [x] 清单声明无挂载点 → 与代码一致。
- [x] **反向核查（grep）**：`buildV2FromCSV`/`openBaseV2`/`BaseStore`/`ErrDuplicatePrefix`/`formatVersionV2`/`writeV2Records`/`v2BatchCommitSize`/`stagingDirPrefix` 的全部非测试引用**均落在 `backend/ipdb/` 内部**（builder_v2.go/store_v2.go/types.go），零 `internal/cli`、零 `main.go` 引用 → 无清单外挂入点。
- [x] **拔除沙盘推演**：删除 `builder_v2.go`+`store_v2.go`+`builder_v2_test.go`+`store_v2_test.go` + types.go 三个新增常量/var（`formatVersionV2`/`stagingDirPrefix`）→ feature 完全消失；v1 文件零改动、cli 零引用 → 无残留。

无偏差。

## 3. 验收场景核对

对照方案第 3 节关键场景清单（1-22），逐条可观察证据（证据类型：单测）：

| # | 场景 | 证据 | 结果 |
|---|---|---|---|
| 1 | metadata FormatVersion/counts/SchemaFeatures | `TestBuildV2FromCSVDualIndex` | 通过 |
| 2 | 双索引同 batch 原子性 + cidr 零长度 | 同上 + `countV2Keys` | 通过 |
| 3 | primary↔cidr 同源 + value 解码 + Network 空 | `TestBuildV2PrimaryCidrSameSource` | 通过 |
| 4 | 单 IP 行 /32 /128 | `TestBuildV2SingleIPRows` | 通过 |
| 5 | openBaseV2 字段填充 + 读 metadata + Close 幂等 | `TestOpenBaseV2Success` | 通过 |
| 6(C) | commitSize 边界双写不拆 | `TestBuildV2DualWriteNotSplitByBatch` | 通过 |
| 7 | 不同 prefix 重叠成功 | `TestBuildV2AllowsDistinctOverlap` | 通过 |
| 8 | 同起始不同 prefixLen | 同上（/8+/16） | 通过 |
| 9 | /0 /32 /128 入库 | `TestBuildV2BoundaryPrefixes` | 通过 |
| 10 | 重复 prefix + 行号 | `TestBuildV2RejectsDuplicatePrefix` | 通过 |
| 11 | 乱序 reject | `TestBuildV2RejectsOutOfOrder` | 通过 |
| 12 | 表头/相对路径/host bits | `TestBuildV2RejectsBadInputs`(3 subtests) | 通过 |
| 13(A) | IPv4-mapped IPv6 拒绝传播 | `TestBuildV2RejectsIPv4MappedIPv6` | 通过 |
| 14(B) | staging 失败清理 | `TestBuildV2StagingCleanupOnFailure` | 通过 |
| 15 | ReadOnly 写失败 | `TestOpenBaseV2IsReadOnly` | 通过 |
| 16(D) | 空库拒绝 | `TestOpenBaseV2RejectsEmptyDir` | 通过 |
| 17(D) | v1 库拒绝 | `TestOpenBaseV2RejectsV1Format` | 通过 |
| 18-22 | 反向核对项 | 见第 2 节 grep/diff 证据 | 通过 |

`go test -count=1 ./backend/ipdb/...` 全绿；全仓 `go test ./...` 回归绿（cli 含 v1 路径）。无前端改动（CLI 库层，无 UI）。

## 4. 术语一致性

对照方案第 0 节术语表 grep 代码：

- `buildV2FromCSV` / `openBaseV2` / `BaseStore` / `ErrDuplicatePrefix` / `formatVersionV2` / `stagingDirPrefix`：代码命中全部一致，与术语表定义吻合 ✓
- v2 kind 字节 `keyKind*`、`SchemaFeatures*`、value 协议常量：沿用 schema feature 定义，未重复定义 ✓
- 防冲突：`grep -rn "buildV2FromCSV\|openBaseV2\|BaseStore\|ErrDuplicatePrefix\|formatVersionV2"` 无 v1/历史 feature 同名冲突 ✓
- 命名延续既有 `*_v2` 约定（`builder_v2.go`/`store_v2.go`/`*_v2_test.go`），与 schema feature 的 `codec_v2_test.go`/`types_v2_test.go` 一致 ✓

无不一致。

## 5. 架构归并

对照方案第 4 节："改动局限在 `backend/ipdb` 内部，二入口无生产调用方，无系统级可见变化。"

- [x] `architecture/ip-lookup.md`：**不需要归并**（理由：第 2 节挂载点反向核对已 grep 证实 `buildV2FromCSV`/`openBaseV2`/`BaseStore` 零生产调用方，公开 `ipdb build` / 查询 / cli 路径全程走 v1，系统对外行为零变化）。
- [x] `architecture/ARCHITECTURE.md`：**不需要**（无新增系统级模块对外可见）。
- [x] 与 roadmap §8 观察项一致：`ip-lookup.md` 的 v2/staging/ReadOnly/双索引描述统一留给 `ipdb-lookup-integration` 的 `cs-feat-accept` 收口回写（届时 v2 已切公开入口、系统级可见）。

判据复核：没读过 design 的人打开 `ip-lookup.md` 看到的仍是 v1 真相——这是正确的，因为运行时行为确实仍是 v1（v2 未激活）。提前写 v2 反而会让 arch 与实际公开行为不符。

## 6. requirement 回写

方案 frontmatter `requirement: offline-ip-lookup`（current req）。

- [x] `requirement` 指向 current req 且本次**未改用户视角**（v2 未激活、无对外行为变化、CLI/JSON 协议零变更）→ **req-offline-ip-lookup 未变，无需更新**。

本 feature 是存储层内部地基，用户可感能力（离线 IP/CIDR 查询）的边界与用户故事均未变；真正改变用户可见正确性（真 LPM / 正确 CIDR）的是后续 `ipdb-v2-query`，届时由其 accept 评估 req 更新。

## 7. roadmap 回写

方案 frontmatter `roadmap: ipdb-v2-lpm` / `roadmap_item: ipdb-v2-base-build`。

- [x] 核对 items.yaml 对应条目当前 `status: in-progress` + `feature: 2026-06-22-ipdb-v2-base-build` ✓（design 阶段已回写）。
- [x] 改 `status: done`，`validate-yaml.py` 校验通过。
- [x] 同步主文档 `ipdb-v2-lpm-roadmap.md` 第 5 节子 feature 清单第 2 条状态 `planned → done`、对应 feature 目录名补上。

依赖链：`ipdb-v2-base-build` done 后，`ipdb-v2-query`（依赖它）前置满足，可启动。

## 8. attention.md 候选盘点

回看本次实现，盘点"每个 feature 都会再撞一次"的环境/工具/工作流类信息：

- [x] **无候选**：本 feature 未暴露需要补入 attention.md 的内容。
  - staging 原子构建、双索引同 batch、ReadOnly 打开属本 feature 特定实现，非跨 feature 通用约束；
  - `netip` 越界 / IPv4-mapped 的坑 attention.md 已有（schema feature 沉淀）；
  - gofmt 对带注释 const 块对齐敏感属通用 Go 知识，不入项目 attention。

## 9. 遗留

- **顺手发现**：`render/style.go` gofmt 不规范（预存文件，非本次改动）→ 不在本次范围，记录待后续 issue。
- **已知限制**：v2 builder/open 为**未激活内部入口**，无生产路径——设计如此（切换原子性，避免"能构建不能查询"窗口），由 `ipdb-v2-query` 收口激活。
- **SDD silent residual**（来自 implement review 的 residual risk）：`BuildID` 合法字符集 / 路径分隔符 / 重复 buildID 处理策略未在 SDD 定义；当前不阻塞（仅内部入口、测试用固定 buildID），建议在 `ipdb-v2-query` 把 builder 接公开入口前补 SDD 决策与测试。
- **roadmap 级验收门槛**：v1/v2 数据库体积、构建时间、端到端兼容矩阵留作 roadmap 最终验收（roadmap §7），非本 feature 范围。
