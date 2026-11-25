# 历史问题与修复

本文记录三组已修复问题的根因、修复方式、测试证据与遗留项，供排查类似症状或理解"为什么代码长这样"。决策层面的结论见 [decisions.md](decisions.md)。

---

<a id="writeback"></a>
## 1. 在线回写破坏 LPM（2026-06-20，P1，已止血）

**现象**

- A. 漏命中：查询 `1.0.0.10` 触发 ipinfo 回写 `/32` 后，再查 `1.0.0.11` 变成 MISS——前驱（`1.0.0.10/32`）不包含目标地址，v1 `SeekGE+Prev` 近似 LPM 直接判 MISS 不回溯，遮蔽了本应命中的 `1.0.0.0/24`。
- B. 永久覆盖：查询 `1.0.0.0`（触发回写 `/32`）后，`1.0.0.5` 永久 MISS——v1 key 只含起始地址不含 prefixLen，`Set(1.0.0.0/32)` 覆盖了 `1.0.0.0/24` 的 value。

**复现**：构建 v1 库 → 配置 ipinfo token → `geoprism 1.0.0.10` → `geoprism 1.0.0.11`（MISS）；`geoprism 1.0.0.0` → `geoprism 1.0.0.5`（MISS）。

**根因**：`state-pollution` + `data-format` + `missing-guard`。构建期强制的"网段不重叠"不变量被运行期直写绕过；key 不含 prefixLen；查询侧无回溯。

**方案取舍**

| 方案 | 结论 |
|---|---|
| A′：base 只读 + 独立 overlay（物理隔离） | **采纳**，成为 v2 重构的起点（D-01） |
| B：写入前 guard（校验不重叠） | 否决：治标，仍允许污染 base |
| C：查询回溯（多回看几条） | 否决：复杂且无法恢复被覆盖记录 |

**第一层止血（已落地）**

- 删除 `maybeWriteBack` / `recordsDiffer` / `writeIPInfoRecord`，移除单 IP 与域名匹配路径上的回写调用。
- `ensureIPDBStore` 对旧版本库记录 warning，`runIPLookup` / `runCIDRLookup` / `runQuery` 在输出前经 stderr 打印一次，不污染 stdout。
- 改动文件：`internal/cli/ip_merge.go`、`ip_lookup.go`、`ip_match.go`、`app.go`。
- 测试：`internal/cli/issue_writeback_fix_test.go` `TestIssueWritebackDisabled_NoBaseMutation`（查询路径不改 base）、`TestCLIV1RebuildHint`（旧库重建提示）。

**留白与后续**

- 存量已污染的 v1 库需 `ipdb build` 重建；v2 `OpenCurrent` 现对 v1 库直接返回 `ErrLegacyFormat`（D-12）。
- `WriteRecord` 止血时暂留签名，v2 收口后改为显式只读拒绝。
- 正式取代（A′）：base v2 + `OverlayStore` 已落地（backend），CLI 接入 `ipdb-lookup-integration` 仍为 planned（见 [ip-lookup.md §11](ip-lookup.md#open)）。**该项落地后应在此处标注"已被 A′ 取代"。**

---

<a id="order"></a>
## 2. 查询结果与 Provider 列表顺序不确定（2026-06-20，P2，已修复）

**现象**：`geoprism query -j` 的 `answers[]` 顺序随 goroutine 完成时间变化；`geoprism providers` / `test --all` 顺序随 map 遍历变化；旧版 `ProviderStore.save` 还会 `sort.Strings(ids)` 把用户声明顺序永久改写。

**根因**：`QueryMulti` 用 `append` 收集结果；`ProviderStore` 只有 `map[string]Provider`；`save` 按 ID 排序。

**修复（D-06）**

- `QueryMulti` 预分配 `Answers[len(providers)]`，goroutine 带 index 回传，`normalizeAnswer` 让成功 / 失败 / 空响应都占原 index。
- `ProviderStore` 增加 `order []string`（TOML 声明顺序）；`List` / `GetEnabled` / `save` 按它；`Upsert` 保位 / 追加；`Delete` 同步移除。
- 升级语义：只对升级后新 load / save 保序，不回填历史已被打乱的顺序。
- `NewResolverWithQueryFunc` 仅供测试注入。

**测试**：`backend/resolver/resolver_test.go`（逆序完成 / error / nil answer / 并发四个顺序用例 + `normalizeAnswer`）；`backend/provider/provider_test.go`（`List` / `GetEnabled` 保序、Upsert 保位 / 追加 / 删除保序）；`internal/cli/cli_test.go` `TestCLIQueryJSON`；`internal/cli/provider_order_test.go`。二进制冒烟：`providers` 连跑 5 次一致，`-p quad9,cloudflare` 保序。

**代码审查复盘**

- CR-001：一个 `sync.Mutex` 死代码触发 `go vet` copylocks，初版"vet 无告警"为事实错误 → 形成"三件套"硬要求（[development.md §2](development.md#gates)）。
- OF-001：EOF 空行（`git diff --check`）。
- OF-002：分析里的 8 条测试矩阵起初只有部分自动化，后补 `provider_order_test.go`。

**遗留**：Provider `name` 唯一性未校验；JSON 协议无 `schema_version`（独立需求，未启动）。

---

<a id="concurrent-build"></a>
## 3. 并发 `ipdb build` 产生悬空 `CURRENT`（2026-07-10，已修复）

**现象**：多个 `geoprism ipdb build` 同时运行时，可能互相删除对方正在使用的 staging 目录、`CURRENT.tmp` 互相覆盖，或 GC 在 reader 仍打开旧版本时删掉目录，最终 `CURRENT` 指向不存在 / 不完整的版本。

**根因**：固定 staging 路径与固定 `CURRENT.tmp`；GC 与 reader 无协调；默认 BuildID 在锁外分配。

**修复（D-14）**

- `BUILD.lock` 独占覆盖清理 staging → 构建 → 发布 → 回收全程。
- `VERSIONS.lock`：reader 共享（`OpenCurrent` → `Close`），发布 / 回收独占；锁序 `BUILD(EX) → VERSIONS(EX)`。
- `os.MkdirTemp` 独占 staging；`CURRENT` 用 `CreateTemp + Sync + Rename`；发布后重读校验再回收；崩溃遗留 staging 跳过 `CURRENT` 指向的目录。
- 默认 BuildID 在锁内分配，纳秒时间戳 + 数字后缀。
- Unix `flock` 对 `EINTR` 重试；Windows `LockFileEx`；`golang.org/x/sys` 转为直接依赖；新增 `file_lock_unsupported.go` 显式报错。
- 改动文件：`backend/ipdb/file_lock_{unix,windows,unsupported}.go`、`builder.go`、`builder_v2.go`、`store.go`、`store_v2.go`、`types.go` 及测试、`go.mod`；`render/style.go` 仅 gofmt。

**验证**：6 goroutine 并发构建、4 子进程并发构建、reader handoff（`TestCleanupWaitsForCurrentReader`）、进程被杀后锁释放、顺序构建、`.staging-` 目录名、`OpenCurrent` 错误路径不泄漏锁（`TestOpenCurrentErrorReleasesVersionsLock`）、默认 ID 冲突后缀（`TestDefaultBuildIDReallocatesExistingName`）；交叉编译 linux / darwin / windows。

**遗留**：新旧二进制混跑没有锁协议；Windows 未做运行时测试；`rootDir` 必须可写；调用方未 `Close` 的 `Store` 会让 `BuildFromCSV` 在发布阶段等待；NFS / SMB 上 `flock` 无保证。
