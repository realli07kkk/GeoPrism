---
doc_type: architecture
slug: cli-entry
scope: CLI 路由入口、App 生命周期、子命令分发
summary: internal/cli 是程序唯一入口，负责解析命令行参数、初始化各后端模块、将请求路由到对应子命令
status: current
last_reviewed: 2026-04-24
tags: [cli, entry, routing]
depends_on: [backend-resolver, backend-provider, backend-ipdb, backend-ipinfo, backend-settings, render-output]
implements: [domain-query, ip-geolocation, ptr-reverse-lookup, provider-config, output-formats]
---

## 1. 定位与受众

本 doc 描述 GeoPrism 的 CLI 层——命令行参数解析、子命令路由、App 生命周期管理。读者是后续做 feature-design（需要知道如何加新命令）和 issue-analyze（需要理解请求流向）的人。

读完能：理解从 `os.Args` 到业务方法调用的完整路径，知道在哪加新命令、如何对接后端模块。

## 2. 结构与交互

### 入口与路由

`main.go:Main` 是唯一入口。流程分三步：

1. **全局 flag 剥离**：先提取前导 `-j`/`--json` 和任意位置的 `-x`/`--ptr`
2. **智能分发**：第一个非 flag 参数若不是已知子命令，按内容自动判断——合法 IP 走 IP 查询、合法 CIDR 走 CIDR 查询、其余走域名查询
3. **子命令路由**：已知命令（`query`/`ipdb`/`providers`/`test`/`help`）传给对应的 `run*` 方法

解析后的 flag 会通过 `reorderArgs` (`app.go:307`) 重排，保证 `example.com -t AAAA` 和 `-t AAAA example.com` 等价。

### App 结构体

`app.go:19` 的 `App` 是请求生命周期的中央协调器，持有所有后端模块的引用：

- `providerStore` → `backend/provider`
- `resolver` → `backend/resolver`
- `ipdbStore` → `backend/ipdb`（按需初始化，`ensureIPDBStore`）
- `settings` → `backend/settings`
- `ipinfoClient` → `backend/ipinfo`（token 为空时为 nil）
- `outputMode` → `render` 输出模式控制

### 子命令对应关系

| 子命令/输入 | 入口方法 | 调用的后端 |
|---|---|---|
| `<domain>` 或 `query <domain>` | `runQuery` / `QueryDomain` | resolver, provider, ipdb, ipinfo |
| `<ip>` | `runIPLookup` / `LookupIP` | ipdb, ipinfo |
| `<cidr>` | `runCIDRLookup` / `LookupCIDR` | ipdb, ipinfo |
| `providers` | `runProviders` / `ListProviders` | provider |
| `test` | `runTest` / `TestProvider` | provider, resolver |
| `ipdb build` | `runIPDB` | ipdb |

### IP 匹配合并

DNS 查询结果中提取的 IP 会通过 `ip_merge.go:collectIPMatches` 去重后逐条匹配离线库或 ipinfo。`ipinfoLookup` 的去重逻辑保证同一个 IP 在多 Provider 结果中出现时只调用一次 ipinfo API。

## 3. 数据与状态

- **QueryRequest** (`app.go:521`)：查询请求 → `Domain` + `RecordType` + `ProviderIDs` + `Timeout`
- **QueryResultView** (`app.go:582`)：查询结果视图，聚合 DNS 应答 + IP 匹配 + NS 信息
- **IPLookupView** (`ip_lookup.go:19`)：单 IP 查询结果，含 `Source` 字段标识 ipdb/ipinfo
- **CIDRLookupView** (`cidr_lookup.go:15`)：CIDR 查询结果，含 `Fallback` 回退
- **ProviderHealth** (`app.go:389`)：Provider 连通性测试结果

App 的 `outputMode` 控制全局输出格式（Text/JSON），各 `run*` 方法在渲染前检查此字段决定调用 `render.WriteJSON` 还是 `render.Write*`。

## 4. 关键决策

暂无已归档的 decision 文档。以下为代码中可观察的隐含决策：

- **按需初始化 IPDB**：`ensureIPDBStore()` 仅在 IP/CIDR 查询或 DNS 结果的 IP 匹配时才打开 Pebble，避免纯域名查询产生不必要的 IO
- **CIDR 查询不依赖 data_source_priority**：CIDR 路径固定先查 ipdb，完全不命中时才以基地址回退到单 IP 查询逻辑
- **参数重排不引入第三方框架**：`reorderArgs` 自实现而非引入 `pflag` 等库，保持零 CLI 框架依赖

## 5. 代码锚点

| 文件 | 关键符号 | 说明 |
|---|---|---|
| `internal/cli/main.go:88` | `Main` | CLI 入口 |
| `internal/cli/app.go:19` | `App` | 应用结构体 |
| `internal/cli/app.go:37` | `NewApp` | 应用初始化 |
| `internal/cli/app.go:160` | `runQuery` | 域名查询子命令 |
| `internal/cli/app.go:230` | `runProviders` | Provider 列表子命令 |
| `internal/cli/app.go:256` | `runTest` | 连通性测试子命令 |
| `internal/cli/app.go:521` | `QueryRequest` | 查询请求类型 |
| `internal/cli/app.go:582` | `QueryResultView` | 查询结果视图 |
| `internal/cli/app.go:627` | `QueryDomain` | 执行域名查询 |
| `internal/cli/ip_lookup.go:19` | `IPLookupView` | 单 IP 查询视图 |
| `internal/cli/ip_lookup.go:89` | `LookupIP` | 单 IP 查询 |
| `internal/cli/ip_lookup.go:131` | `runIPLookup` | IP 查询子命令 |
| `internal/cli/cidr_lookup.go:15` | `CIDRLookupView` | CIDR 查询视图 |
| `internal/cli/cidr_lookup.go:108` | `LookupCIDR` | CIDR 查询 |
| `internal/cli/cidr_lookup.go:149` | `runCIDRLookup` | CIDR 查询子命令 |
| `internal/cli/ns_info.go` | — | NS 信息查询 |
| `internal/cli/reverse.go` | — | PTR 反向查询 |
| `internal/cli/ip_merge.go` | — | IP 结果合并与回写 |
| `internal/cli/ipdb_cmd.go` | — | ipdb build 子命令 |
| `internal/cli/paths.go` | — | 数据目录路径管理 |

## 6. 已知约束 / 边界情况

- 不能引入第三方 CLI 框架——当前纯标准库 + `flag.NewFlagSet`，改动需保持零 CLI 框架依赖
- `App.Close()` 只关闭 ipdbStore，不负责其他资源——未来若新增持有资源的模块需同步改 Close
- 错误信息在 JSON 模式下输出到 stderr 且为 `{"error":"..."}` 格式（`app.go:92`），不可改为 stdout
- 快捷查询路径（非已知命令）的输入判断逻辑在 `main.go:77` 的 `parseIPInput`，新增输入类型需注意不要破坏此判断

## 7. 相关文档

- `backend-resolver.md` — DNS 查询执行
- `backend-provider.md` — Provider 配置管理
- `backend-ipdb.md` — 离线 IP 库
- `backend-ipinfo.md` — ipinfo 在线查询
- `backend-settings.md` — 应用配置
- `render-output.md` — 输出渲染
- 需求：`domain-query`、`ip-geolocation`、`ptr-reverse-lookup`、`provider-config`、`output-formats`
