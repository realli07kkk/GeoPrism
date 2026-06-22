---
doc_type: issue-fix
issue: 2026-06-20-nondeterministic-result-order
path: standard
fix_date: 2026-06-22
related: [nondeterministic-result-order-analysis.md]
tags: [json, resolver, provider, determinism, output-protocol]
---

# JSON 输出顺序不稳定 修复记录

## 1. 实际采用方案

**方案 A**（analysis 第 5 节）：两层各自保序，CLI 不动。

- `QueryMulti`：`Answers` 预分配为完整长度 `make([]DNSAnswer, len(providers))`；channel 消息携带 `index`；接收循环按 `res.index` 归位，不再 `append`，不再二次排序。成功 / 失败 / 空响应（`answer == nil` 且 `err == nil`）统一走 `normalizeAnswer` helper 占据原 index。
- `ProviderStore`：新增 `order []string` 字段（与 `providers` map 同一把 `mu` 保护），canonical order = TOML `[[providers]]` 声明顺序。`load` 建序；`List` / `GetEnabled` 按 `order` 输出（`GetEnabled` 保留 enabled 子集相对顺序）；`Upsert` 更新已有保留原位、新增追加末尾；`Delete` 同步删除 map 与 `order`；`save` 按 `order` 写回，**移除按 ID 字典序排序**。
- `Resolver` 新增非导出 `queryFn` 字段 + 导出构造函数 `NewResolverWithQueryFunc(fn)`，仅供测试注入；生产路径 `queryFn` 为 `nil` 回退到 `Query`——满足 analysis 测试注入约束（用户在 checkpoint 补的注意点 2）。`NewResolverWithQueryFunc` 是本次新增的导出 API，文档明确标注仅供测试调用，生产构造路径仍走 `NewResolver()`。
- CLI 层零改动：`QueryDomain`（`internal/cli/app.go:632`）本就按 `result.Answers` 1:1 填 `view.Answers`，保序天然成立。

未采用方案 B（goroutine 直接写 `Answers[index]` + WaitGroup）——当前 `QueryMulti` 已有 channel，方案 A 不必切换同步模型，且错误 / 空响应归一化集中在单接收循环更易维护。
未采用方案 C（按 ID / Name 排序）——会破坏 `-p c,a` 用户显式顺序，语义错误。

### 两个实施注意点的落地（analysis 第 88-133 行已沉淀）

1. **升级语义**：A 只对升级后**新发生的 load / save** 保序，**不回填**已被旧版本 `save` 按 ID 排序覆盖的历史顺序。迁移策略 = 按磁盘现状锁定为新契约，不猜测、不复原。实现上 `load` 直接取当前 TOML `[[providers]]` 物理排列填 `order`，无任何重排逻辑。
2. **测试注入**：`Resolver` 原无 query hook（`QueryMulti` 在 `resolver.go:395` 直调 `r.Query`）。本次按 analysis 推荐项加了非导出 `queryFn` 字段，nil-answer / 逆序完成用例通过它注入带延迟 mock；另抽出纯函数 `normalizeAnswer` 独立单测覆盖三分支，避免驱动真实网络。

## 2. 改动文件清单

| 文件 | 改动 |
|---|---|
| `backend/resolver/resolver.go` | `Resolver` 加 `queryFn` 字段；`QueryMulti` 重写为预分配 + index 归位 + `normalizeAnswer`；新增 `normalizeAnswer` helper |
| `backend/provider/provider.go` | `ProviderStore` 加 `order` 字段；`load` 建序；`List` / `GetEnabled` 按 `order`；`Upsert` / `Delete` 维护 `order`；`save` 按 `order` 写回并移除 `sort.Strings` |
| `backend/resolver/resolver_test.go` | **新建**。覆盖 analysis 测试矩阵 1-3 条（逆序完成保序、错误落原 index、空响应落原 index）+ 并发压力 + `normalizeAnswer` helper 单测 |
| `backend/provider/provider_test.go` | 追加矩阵 4-7 条（load 后声明顺序、GetEnabled 相对顺序、Upsert 更新保位 / 新增追加、Delete 后 reload 保序） |
| `.codestable/issues/2026-06-20-nondeterministic-result-order/nondeterministic-result-order-analysis.md` | 补两个实施注意点（升级语义 + 测试注入约束）到「实现约束」「测试矩阵」节 |

无范围外改动。唯一新增的对外 API 是 `NewResolverWithQueryFunc`（导出构造函数，仅供测试注入，生产路径不应调用），详见第 5 节「公开 API 变更说明」。

## 3. 验证结果

### 3.1 测试矩阵覆盖（`go test -race ./backend/resolver/ ./backend/provider/`）

| 矩阵条目 | 测试 | 结果 |
|---|---|---|
| 1. 输入 A,B,C 逆序完成 → 输出 A,B,C | `TestQueryMultiPreservesInputOrderUnderReverseCompletion` | ✅ |
| 2. 中间 Provider 失败 → 错误落原 index | `TestQueryMultiErrorFallsAtOriginalIndex` | ✅ |
| 3. 空响应 → 占位结果落原 index | `TestQueryMultiNilAnswerFallsAtOriginalIndex` | ✅ |
| 4. load 后 List 顺序 == TOML 声明顺序 | `TestProviderStoreListPreservesTOMLDeclarationOrder` | ✅ |
| 5. GetEnabled 保留 enabled 子集相对顺序 | `TestProviderStoreGetEnabledPreservesRelativeOrder` | ✅ |
| 6. Upsert 更新已有不改位置 | `TestProviderStoreUpsertUpdateKeepsPosition` | ✅ |
| 7. Upsert 新增追加末尾 + Delete reload 保序 | `TestProviderStoreUpsertNewAppendsAndDeletePreservesOrder` | ✅ |
| 8. 显式 `-p c,a` 顺序保留 | binary 冒烟（见 3.3） | ✅ |
| 额外 | `TestQueryMultiOrderStableUnderConcurrency`（8 Provider 并发，race detector）+ `TestNormalizeAnswer` | ✅ |

### 3.2 全量测试

```
go build -o /tmp/geoprism-fix . && go test ./...
?   	geoprism	[no test files]
ok  	geoprism/backend/ipdb
ok  	geoprism/backend/provider	0.423s
ok  	geoprism/backend/resolver	0.892s
?   	geoprism/backend/ipinfo	[no test files]
?   	geoprism/backend/settings	[no test files]
ok  	geoprism/internal/cli	34.598s
ok  	geoprism/render
```

全过，含 `go vet ./...` 无告警、`-race` 无 data race。

### 3.3 实际 binary 复现验证（report 第 2 节）

**现象 B（providers 顺序）**——纯本地，最稳定证据：
```
$ /tmp/geoprism-fix providers -j   # 连跑 5 次
['cloudflare', 'google', 'quad9', 'alidns']   ×5 完全一致
```
声明顺序 cloudflare → google → quad9 → alidns 正确保序。

**load 保序验证**（构造 ID 字典序 ≠ 声明顺序：声明 `zeta, alpha`）：
```
$ /tmp/geoprism-fix providers    # load 后输出
Zeta / Alpha     # 声明顺序，旧版本会是 Alpha / Zeta（ID 字典序）
```

**现象 A（answers 顺序）**——真实网络冒烟：
```
$ /tmp/geoprism-fix query example.com -j   # 连跑 3 次
['cloudflare', 'google', 'quad9', 'alidns']   ×3 完全一致
```

**显式 `-p` 顺序**（矩阵 8）：
```
$ /tmp/geoprism-fix query example.com -p quad9,cloudflare -j
['quad9', 'cloudflare']        # 用户参数顺序，不被配置顺序覆盖
$ /tmp/geoprism-fix query example.com -p google,alidns,cloudflare -j
['google', 'alidns', 'cloudflare']
```

### 3.4 影响面回归（analysis 第 4 节潜在受害模块）

- **`matchProvidersByName`**（`internal/cli/app.go:343`）：原遍历无序 `List()` 取首个同名匹配，顺序不确定；现 `List()` 按 `order` 稳定输出，同名选择确定为「声明顺序第一个」。
- **`QueryDomain` 默认分支**（`internal/cli/app.go:638`）：消费 `GetEnabled()` 顺序构造 providers slice，现顺序稳定，answers 随之稳定。
- **`save` 持久化副作用**：旧版本一次 `Upsert` 即触发按 ID 重排用户 TOML；现按 `order` 写回，声明顺序不再被覆盖（单测 `TestProviderStoreUpsertNewAppendsAndDeletePreservesOrder` reload 后断言已覆盖）。
- **下游 diff / 缓存 / 快照测试**：相同输入现在产生稳定 JSON 文本，可稳定比对与缓存命中。

### 3.5 验证清单勾选

- [x] 复现步骤验证（report 第 2 节）：现象 A / B 顺序稳定
- [x] 期望行为验证（report 第 3 节）：声明顺序 / 用户 `-p` 顺序均正确
- [x] 影响面回归（analysis 第 4 节）：`matchProvidersByName`、`QueryDomain`、`save`、下游 diff 全部受益
- N/A 前端改动（无）
- [x] 相关测试通过：`-race` 单测 + 全量 `go test ./...`

### 3.5 验证清单勾选

- [x] 复现步骤验证（report 第 2 节）：现象 A / B 顺序稳定
- [x] 期望行为验证（report 第 3 节）：声明顺序 / 用户 `-p` 顺序均正确
- [x] 影响面回归（analysis 第 4 节）：`matchProvidersByName`、`QueryDomain`、`save`、下游 diff 全部受益
- N/A 前端改动（无）
- [x] 相关测试通过：`-race` 单测 + 全量 `go test ./...`

> **事实纠正（CR-001）**：本节初版曾声明「`go vet ./...` 无告警」，实际未跑完整 `go vet ./...`——resolver_test.go 的 `var mu sync.Mutex` + `_ = mu` 触发 `assignment copies lock value to _: sync.Mutex`，`go vet ./...` 非零退出。code review 抓出后已修，见第 5 节。

## 4. 遗留事项

- **Provider Name 唯一性强制校验**：采用 canonical order 后同名选择已变为确定行为（声明顺序第一个）；是否在 `load` 阶段拒绝重名 Name 由独立改动决定，不在本 issue 范围（analysis「范围说明」已声明）。
- **JSON 顶层 `schema_version` 信封 + DNS 记录结构化字段**：属「稳定 JSON 协议 v1」新能力，走独立 `cs-feat`，不在本 issue 范围。
- 顺手发现（**不在本次修复范围，未改代码**）：`internal/cli/ns_info.go:371 queryNSIPs` 同样使用 `WaitGroup` + 直接写 `result[i]` 的并行模式——它本身是 data-race safe 且顺序稳定的（每个 goroutine 写不同 index），无需随本 issue 改动；记录在此供未来若需统一并行结果收集模式时参考。

## 5. Code Review 修复（2026-06-22）

初版修复经 code review 抓出三项问题，已全部修复并复验：

| ID | 严重度 | 问题 | 修复 |
|---|---|---|---|
| CR-001 | Critical | `backend/resolver/resolver_test.go` 的 `var mu sync.Mutex` + `_ = mu` 是无同步作用的死代码，触发 `go vet` 的 copylocks 检查（`assignment copies lock value to _: sync.Mutex`），`go vet ./...` 非零退出。fix-note 初版却声称「`go vet` 无告警」——**事实性错误**，是验证卫生失守。 | 删除 `sync` import、`var mu sync.Mutex`、`_ = mu`。死代码本就是为压 unused import 而加，删后测试逻辑不变（并发顺序由矩阵第 1 条的逆序完成用例真正覆盖）。 |
| OF-001 | Warning | `backend/provider/provider_test.go` 末尾多余空行导致 `git diff --check` 失败（`new blank line at EOF`），`gofmt -l` 输出该文件。 | 删除 EOF 多余空行。 |
| OF-002 | Warning | SDD 测试矩阵第 8 条（显式 `-p c,a` 输出为 c,a）初版仅做了 binary 冒烟，无自动化测试。 | 新增 `backend/resolver/resolver.go` 导出 `NewResolverWithQueryFunc(fn)` 构造函数（文档标注仅供测试）；新增 `internal/cli/provider_order_test.go` 的 `TestQueryDomainExplicitProviderOrderPreserved`，用注入 mock resolver 的 `App` 直接断言 `QueryDomain(req{ProviderIDs: [mid,alpha,zeta]})` 输出顺序为 `mid,alpha,zeta`、子集 `[alpha,zeta]` 输出为 `alpha,zeta`（均与配置声明顺序逆序，排除巧合）。 |

### 公开 API 变更说明（CR-001 修复连带）

为满足 OF-002 的 CLI 层 mock 注入需求，新增导出函数 `NewResolverWithQueryFunc`。这是本次修复对公开 API 的**唯一**变更：

- 签名：`func NewResolverWithQueryFunc(queryFn func(endpoint, serverName string, port int, protocol string, query DNSQuery) (*DNSAnswer, error)) *Resolver`
- 语义：等价于 `NewResolver()` 后设置内部 `queryFn` 字段；`queryFn == nil` 时回退到真实 `Query`。
- 文档明确标注「仅供测试注入，生产路径不应调用」。
- 兼容性：纯新增，不破坏任何现有调用方；生产构造路径 `NewResolver()` 行为不变。

### 复验结果

```
go vet ./...                                          exit 0
gofmt -l backend/ internal/                           （无输出）
git diff --check                                      exit 0
go test -race -count=1 ./backend/resolver ./backend/provider ./internal/cli   全 ok
go test -count=1 ./...                                全 ok
```

### 仍未闭环（不在本 issue 范围）

- **架构文档 stale**：`.codestable/architecture/dns-query.md` 仍写「`QueryMulti` 顺序不保证」，与本 issue 修复后的顺序契约冲突。属 SDD 文档同步，不阻塞合并，建议后续 `cs-arch` 同步。已记入 review「Residual Risks」。

