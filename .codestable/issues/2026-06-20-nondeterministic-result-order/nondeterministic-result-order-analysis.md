---
doc_type: issue-analysis
issue: 2026-06-20-nondeterministic-result-order
status: confirmed
root_cause_type: concurrency
related: [nondeterministic-result-order-report.md]
tags: [json, resolver, provider, determinism, output-protocol]
---

# JSON 输出顺序不稳定 根因分析

## 1. 问题定位

| 关键位置 | 说明 |
|---|---|
| `backend/resolver/resolver.go:381` | `result.Answers = make([]DNSAnswer, 0, len(providers))` —— 以 0 长度预分配，靠 `append` 累加 |
| `backend/resolver/resolver.go:411-423` | 收集循环 `res := <-ch` 后 `append`，顺序 = goroutine 完成（响应快慢）顺序，乱序源 1 |
| `backend/provider/provider.go:155-163` | `List` 直接 `for _, p := range s.providers`（遍历 map），无稳定顺序，乱序源 2 |
| `backend/provider/provider.go:200-210` | `GetEnabled` 同样遍历 map，乱序源 2 |
| `backend/provider/provider.go:129-152` | `save` 把 map 的 key 收集后 `sort.Strings(ids)` 按 ID 排序写回 TOML —— 会永久改写用户原始声明顺序 |
| `backend/provider/provider.go:97-126` | `load` 把 `[[providers]]` 解码进 map，丢弃了 TOML 声明顺序 |
| `internal/cli/app.go:632` | `QueryDomain` 默认分支用 `GetEnabled()` 的顺序构造 `providers` slice |
| `internal/cli/app.go:684-696` | `QueryDomain` 按 `result.Answers` 顺序 1:1 填 `view.Answers` —— 已保序，无需改 |
| `internal/cli/app.go:337-351` | `matchProvidersByName` 遍历 `List()` 取首个 EqualFold 匹配 —— List 乱序时同名选择不确定 |

## 2. 失败路径还原

**正常路径（期望）**：相同输入 → providers slice 顺序固定 → 各 Provider 并行查询 → 结果按 providers slice 的位置归位 → `answers[]` 顺序稳定且等于输入顺序。

**失败路径（实际）**：
- 链路 A（answers）：`QueryDomain` 取 `GetEnabled()` →（map 遍历）providers slice 顺序已随机 → `QueryMulti` 又按 goroutine 完成顺序 `append` → `answers[]` 二次乱序。
- 链路 B（providers / test）：`runProviders` / `runTest --all` 直接用 `List()` →（map 遍历）顺序随机。

**分叉点**：
- `backend/resolver/resolver.go:411-423` —— `append` 用完成顺序而非输入顺序。
- `backend/provider/provider.go:155 / :200` —— map 遍历无序，且源头 `load` 没保留 TOML 声明顺序。

两个分叉点相互独立，**只修一个仍会乱**（修了 QueryMulti，providers slice 本身还乱；修了 provider 顺序，QueryMulti 仍按完成顺序打散）。

## 3. 根因

**根因类型**：concurrency（QueryMulti）+ 设计缺陷 / map 迭代无序（ProviderStore）—— 两个独立根因。

**根因描述**：
1. `QueryMulti` 为并行查询用 channel 收集结果，但收集循环按"谁先到先 append"组装数组，把输入顺序丢了——这是并发完成顺序泄漏到输出。
2. `ProviderStore` 用 `map[string]Provider` 作唯一存储，`load` 时丢弃了 TOML 的 `[[providers]]` 声明顺序，`List` / `GetEnabled` 遍历 map 自然无序；`save` 又按 ID 排序写回，进一步把用户原始顺序覆盖掉。

**是否有多个根因**：是。主：上述两条独立乱序源，必须同时修。次：`save` 按 ID 排序会在任何一次 `Upsert` / `Delete` 后永久改写用户的 TOML 声明顺序，是采用"声明顺序为 canonical"方案时的连带必修点。

## 4. 影响面

- **影响范围**：所有多 Provider 的输出——`query`（`answers[]`）、`providers`、`test --all` 的 JSON 与文本输出顺序都受影响。
- **潜在受害模块**：`matchProvidersByName`（同名 Provider 选择不确定）；任何基于 JSON 输出做 diff / 缓存 / 快照测试的下游（AI agent、脚本）。
- **数据完整性风险**：查询结果数据本身不丢不错，仅排列顺序不稳定；但 `save` 按 ID 重排会**持久改写**用户的 `providers.toml` 声明顺序（一次 Upsert 即触发），属于对用户配置文件的非预期改写。
- **严重程度复核**：维持 **P2**。无运行时崩溃 / 数据损坏，但与项目"JSON 面向 AI agent 消费"的定位直接冲突，且 `save` 重排是对用户文件的副作用，建议不要再降级。

## 5. 修复方案

### 方案 A：QueryMulti 按输入 index 填充 + ProviderStore 引入 order 切片（推荐）

- **做什么**：
  - `QueryMulti`：`result.Answers` 预分配为 `make([]DNSAnswer, len(providers))`；channel 结果携带 `index`；收集循环按 `res.index` 写入对应位置，不再 `append`、不再二次排序。
  - `ProviderStore`：新增 `order []string` 字段；`load` 按 TOML `[[providers]]` 出现顺序填 `order`；`List` / `GetEnabled` 遍历 `order` 取值；`Upsert` 已存在的保留原位、新增追加末尾；`Delete` 同步从 map 与 `order` 删除；`save` 按 `order` 写回 TOML（去掉按 ID 排序）。
- **优点**：根因最直接；聚合状态仍只由单个接收循环修改，未来给结果加统计字段不易引入竞态；canonical order 用 TOML 声明顺序，用户可见、可控、符合直觉；cli 层无需改（`QueryDomain` 已 1:1 保序）。
- **缺点 / 风险**：`save` 行为变更——首次升级后用户 TOML 会从"按 ID 排序"变为"按声明顺序"，属一次性可接受变化；需保证 `order` 与 `providers` map 在所有写路径上同步，避免出现 order 残留已删 ID。
- **影响面**：`backend/resolver/resolver.go`（QueryMulti）、`backend/provider/provider.go`（结构体 + load/List/GetEnabled/Upsert/Delete/save）。不动 `internal/cli/app.go` 的顺序逻辑。

### 方案 B：goroutine 直接写 `result.Answers[index]`

- **做什么**：预分配数组后，每个 goroutine 直接写自己的 `result.Answers[i]`，省掉 channel。
- **优点**：代码更短。
- **缺点 / 风险**：多个 goroutine 并发写同一 slice 的不同元素虽对当前结构安全，但聚合状态（如将来要累加的统计字段）一旦出现就需要额外同步；保守性不如方案 A 的单接收循环。
- **影响面**：同 A 的 resolver 部分。

### 方案 C：查询后 / List 按 ID 或 Name 排序

- **做什么**：在 `QueryMulti` 结束后对 `answers` 按 provider ID 排序；`List` 按 ID 排序。
- **优点**：实现简单，天然稳定。
- **缺点 / 风险**：UUID 字典序对用户毫无意义；Name 可重复、改名会变序；且无法表达 `-p cloudflare,google` 这种"用户指定顺序"——会被全局排序覆盖。语义错误。
- **影响面**：resolver + provider。

### 推荐方案

**推荐方案 A**，理由：根因最直接（两层各自保序）、副作用最少（聚合写集中、cli 不动）、canonical order 选 TOML 声明顺序对用户最直觉且能正确表达 `-p` 显式顺序。方案 B 在并发写安全上不如 A 保守，方案 C 顺序语义错误。

---

## 实现约束（fix 阶段硬约束）

**QueryMulti**：
- `Answers` 必须预分配为完整长度（`make([]DNSAnswer, len(providers))`）。
- 不再使用 `append`。
- 成功、失败、空响应（`answer == nil`）都必须占据原 Provider 对应 index 位置，不得过滤或挪到末尾。
- 查询完成顺序不影响结果数组顺序。
- `QueryMulti` 不自行排序，只忠实保留传入 `providers` slice 的顺序。

**ProviderStore**：
- canonical order = TOML `[[providers]]` 声明顺序。
- `List` / `GetEnabled` 按 `order` 输出（`GetEnabled` 保留 enabled 子集的相对顺序）。
- `Upsert` 更新已有 → 保留原位置；新增 → 追加末尾。
- `Delete` → map 与 `order` 同时删除。
- `save` 按 `order` 写回，**移除按 ID 排序**。

**升级语义（兼容性承诺，fix 阶段必须遵守）**：A 只能从升级那一刻起对**新发生的 load / save** 保序，**无法恢复**已经被旧版本 `save` 按 ID 排序覆盖掉的历史原始顺序。即：用户首次升级后的 `providers.toml`，其 `order` 取自"当前磁盘文件里 `[[providers]]` 的物理排列"（很可能已经是 ID 字典序），而非用户最初声明的顺序。"升级后变为声明顺序"应理解为**新的持久化契约**——从这一刻起不再二次重排——而不是自动复原历史顺序。迁移策略上不回填、不猜测，按现状锁定即可。

## 顺序语义矩阵（对外契约）

| 调用方式 | Provider 顺序 |
|---|---|
| 默认域名查询 | ProviderStore 配置（声明）顺序，仅保留 enabled |
| `query -p a,b,c` | 用户参数顺序 a → b → c |
| `providers` | 配置顺序 |
| `test --all` | 配置顺序 |
| `test <name>` | 单项，无顺序问题 |
| `QueryMulti` | 严格保留传入 slice 顺序 |

职责划分：ProviderStore 决定默认顺序；CLI 决定显式选择顺序；QueryMulti 只负责忠实保序。

## 测试矩阵（fix 阶段，避开真实网络）

通过注入可控的 `Query` / mock resolver 让 Provider **逆序完成**来验证保序，而非"多跑几次看着没乱"。断言只比较 `provider_id` / `provider_name` 序列，不比较整段 JSON（`rtt_ms` / `total_time_ms` 会变）。

**注入约束（fix 阶段必须解决）**：当前 `Resolver` 没有可替换的 `Query` hook——`QueryMulti` 在 `backend/resolver/resolver.go:395` 直接调用 `r.Query(...)` 具体方法，外部测试无法插入"带延迟的 mock"。`WaitGroup` + 直接写 `result[i]` 的并发写模式在本项目已有先例（`internal/cli/ns_info.go:371 queryNSIPs`），但本次 fix 采用方案 A 不切换同步模型。为满足测试矩阵第 1-3 条，fix 阶段二选一：
- **优先**：给 `Resolver` 增加一个**仅供内部注入**的 query function 字段（如 `queryFn func(...) (...)`），生产路径默认为 nil → 回退到现有 `r.Query`；测试设为可控延迟函数。改动局部、不破坏 userspace。
- **次选**：通过自定义 `http.RoundTripper` 返回带延迟的 DoH 响应驱动真实 `QueryDoH`，但**只能覆盖 DoH 协议**且无法表达 `nil` answer（需走 hook）。

nil-answer 用例（第 3 条）必须走 query hook 路径——RoundTripper 方案表达不了"返回 nil 且无 err"。若抽出一个**纯粹的结果归一化 helper**（把 `[]resultChan` 按 index 归位为 `[]DNSAnswer`）则该 helper 可独立单测覆盖第 1-3 条，是最干净的解耦点。

1. `QueryMulti` 输入 A、B、C，完成顺序 C、B、A → 输出仍为 A、B、C。
2. 中间 Provider 失败 → 错误结果仍落在原 index。
3. 空响应（`answer == nil`）→ 仍落在原 index（按实现约束补占位错误结果）。
4. `ProviderStore.load` 后 `List` 顺序 == TOML 声明顺序。
5. `GetEnabled` 过滤后保持剩余 Provider 的相对（声明）顺序。
6. `Upsert` 更新已有 Provider 不改变其位置。
7. `Upsert` 新增 Provider 出现在末尾；`Delete` 后 save→reload 剩余顺序不变。
8. 显式 `-p c,a` → 输出为 c、a，不被配置顺序覆盖。

## 范围说明

- 本 issue 只解决"顺序确定性"。
- 关联但**不在本 issue 范围**：Provider Name 唯一性强制校验（采用 canonical order 后，同名选择已变为"取声明顺序第一个"的确定行为；是否进一步在 `load` 阶段拒绝重名 Name 由独立改动决定）；JSON 顶层 `schema_version` 信封与 DNS 记录结构化字段（属"稳定 JSON 协议 v1"新能力，走独立 feat）。
