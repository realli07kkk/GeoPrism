---
doc_type: architecture
slug: dns-query
scope: 多 Provider DNS 查询子系统（域名解析、Provider 管理、连通性测试、NS zone 探测、反向 PTR）
summary: 编排层把请求拆成 Provider 列表交给 resolver 并行查询；NS 信息走独立 zone 探测链
status: current
last_reviewed: 2026-06-20
tags: [dns, resolver, provider, ns]
depends_on:
  - ARCHITECTURE
implements:
  - multi-provider-dns-query
---

# 多 Provider DNS 查询子系统

## 0. 术语

- **记录类型（RecordType）**：A / AAAA / CNAME / TXT / NS / MX / SOA / PTR 八种，枚举见 `backend/resolver/resolver.go:30`。
- **RCode**：DNS 响应码（NOERROR / NXDOMAIN / SERVFAIL / REFUSED 等），映射见 `backend/resolver/resolver.go:120`。
- **NS zone 候选链**：查询 `a.b.example.com` 的 NS 时，会沿 label 向上生成 `[a.b.example.com, b.example.com, example.com]` 依次试，取第一个返回 NS 记录的作为真实 zone。见 `internal/cli/ns_info.go:230`。
- **反向查询**：`-x` 把 IP 转成 `in-addr.arpa` / `ip6.arpa` 域名再查 PTR。见 `internal/cli/reverse.go:12`。

## 1. 定位与受众

- 覆盖 `query` 子命令、域名快捷查询、反向 PTR、`providers` / `test` 子命令、以及 query 后附带的 NS 信息查询。
- `feature-design` 改 DNS 查询流程、加记录类型、改 NS 探测策略时读本档定位；`issue-analyze` 排查"某域名查不出来 / NS 信息缺失"时读它理解探测链。

## 2. 结构与交互

```mermaid
graph TD
    A["runQuery<br/>(internal/cli/app.go:160)"] --> B["QueryDomain<br/>(app.go:627)"]
    B --> C["providerStore.GetEnabled / Get<br/>筛 Provider 列表"]
    B --> D["resolver.QueryMulti<br/>并行查询"]
    B --> E["collectIPMatches<br/>（跨子系统，见 ip-lookup.md）"]
    B --> F["queryNSInfo<br/>（ns_info.go:153）"]
    F --> G["discoverNSZone<br/>zone 候选链并行 NS 查询"]
    F --> H["queryNSIPs<br/>并行查每个 NS 的 A/AAAA"]
    D -.结果.--> I["QueryResultView<br/>(app.go:582)"]
    I --> J["render.WriteQueryResult<br/>TTY / 纯文本双协议"]
```

**为什么这么分**：

- **`resolver.QueryMulti` 只管"并发发查询、收原始 DNSAnswer"**——不关心 Provider 名字、不关心 NS、不做合并。它是个纯粹的 DNS 协议执行器，三个协议（DoH / DoT / DNS-UDP）共用一套 `*DNSAnswer` 输出格式（`backend/resolver/resolver.go:376`）。
- **编排层 `App.QueryDomain` 负责把 resolver 的输出翻译成 View**：补 Provider 名字、收集 IP 匹配、查 NS 信息（`internal/cli/app.go:627`）。View 是给 render 用的 DTO，实现 `render.*Source` 接口（见 `render/query.go:12`）。
- **NS 信息是 query 的副产品但走独立子流程**：`queryNSInfo` 不依赖主查询结果，自己重新发 NS 查询（`internal/cli/ns_info.go:153`）。这样即使主查询失败（A 记录 NXDOMAIN），NS 信息仍可能有效。

**契约**：

- **Provider 选择**：`-p` 传名字走 `matchProvidersByName`（`internal/cli/app.go:337`），按 `Name` 字段大小写不敏感匹配；不传则用所有 `Enabled=true` 的 Provider。
- **并发模型**：`QueryMulti` 对每个 Provider 起一个 goroutine，用 buffered channel 收集（`backend/resolver/resolver.go:391`）。结果顺序不保证——由 channel 收集顺序决定。
- **超时**：默认 5000ms，`--timeout` 可覆盖；DoT 用 `conn.SetDeadline`、DoH 用 `context.WithTimeout`、DNS-UDP 用 `dns.Client.Timeout`（各自在 resolver.go 内）。

## 3. 数据与状态

**核心类型**：

| 类型 | 定义 | 含义 |
|---|---|---|
| `resolver.ProviderInfo` | `backend/resolver/resolver.go:17` | resolver 用的 Provider 投影（从 `provider.Provider` 转换来） |
| `resolver.DNSAnswer` | `backend/resolver/resolver.go:49` | 单个 Provider 的原始响应（含 RCode / TTL / RTT） |
| `cli.QueryAnswer` | `internal/cli/app.go:529` | View 层响应，多了 Provider 名字 |
| `cli.QueryResultView` | `internal/cli/app.go:582` | 整次查询的对外视图，含 Answers / IPMatches / NSInfo |
| `cli.NSInfoView` | `internal/cli/ns_info.go:30` | NS 信息视图 |

**所有权**：Provider 列表归 `ProviderStore`（`backend/provider/provider.go:44`），读写有 `sync.RWMutex` 保护——虽然 CLI 当前只读，但 Upsert/Delete 方法已暴露给未来扩展。

**无持久化**：DNS 查询结果只在内存，不落盘。

## 4. 关键决策

- **Provider 配置只认 TOML**——旧的 `providers.json` 不读不迁移，只警告（`backend/provider/provider.go:86`）。理由：避免双格式维护成本。
- **NS zone 探测沿 label 向上，不猜 apex**——避免对公共后缀（如 `co.uk`）误判。见 `internal/cli/ns_info.go:182` 的注释。
- **NS 查询失败时按 RCode 优先级选错误信息**——NXDOMAIN > SERVFAIL/REFUSED > 其他，让用户看到最有信息量的那条（`internal/cli/ns_info.go:260`）。
- **render 双协议（TTY 增强表 / 非 TTY 纯文本）**——脚本管道场景必须拿到稳定文本，交互场景要好看；用 `isatty` 检测自动切换（`render/style.go:30`）。

> 以上尚未单独落档为 `decision`。`TODO: 后续用 cs-decide 把 NS 探测策略 沉淀为 decision`。

## 5. 代码锚点

| 位置 | 一行说明 |
|---|---|
| `internal/cli/app.go:160` | `runQuery`：解析 `-t/-p/-x/-j` 等 flag，调 QueryDomain |
| `internal/cli/app.go:627` | `QueryDomain`：Provider 筛选 → QueryMulti → 组装 View |
| `internal/cli/app.go:496` | `TestProvider`：连通性测试，固定查 `google.com` A |
| `internal/cli/app.go:256` | `runTest`：`--all` 或按名字测，输出 OK/FAIL/ERROR |
| `internal/cli/ns_info.go:153` | `queryNSInfo`：NS 信息主入口 |
| `internal/cli/ns_info.go:182` | `discoverNSZone`：zone 候选链并行探测 |
| `internal/cli/ns_info.go:230` | `buildNSZoneCandidates`：拆 label 生成候选 |
| `internal/cli/ns_info.go:371` | `queryNSIPs`：并行查每个 NS 的 A/AAAA |
| `internal/cli/reverse.go:12` | `ipToReverseName`：IP → arpa 域名 |
| `backend/resolver/resolver.go:162` | `QueryDoH`：DoH 查询（base64url GET） |
| `backend/resolver/resolver.go:235` | `QueryDoT`：DoT 查询（TLS + 长度前缀） |
| `backend/resolver/resolver.go:312` | `QueryDNS`：原生 UDP 查询 |
| `backend/resolver/resolver.go:356` | `Query`：按 protocol 分流到上面三个 |
| `backend/resolver/resolver.go:376` | `QueryMulti`：并发 fan-out + 收集 |
| `backend/resolver/resolver.go:430` | `TestConnection`：连通性测试的底层实现 |
| `backend/provider/provider.go:55` | `NewProviderStore`：加载 / 首次写默认 TOML |
| `render/query.go:12` | `QueryAnswerSource` 接口：View 要实现的方法集 |
| `render/style.go:30` | `isTTY`：输出模式切换的检测点 |

## 6. 已知约束 / 边界情况

- **DoT 每次查询新建 TLS 连接**——`tls.Dial` 不复用（`backend/resolver/resolver.go:248`），高并发下握手成本会累积；当前是 CLI 单次查询场景，可接受。
- **连通性测试固定查 `google.com` A 记录**——不参数化（`backend/resolver/resolver.go:443`），假设 google.com 全球可达。
- **NS 探测并发发 N 个 NS 查询**——`buildNSZoneCandidates` 生成几个候选就起几个 goroutine（`internal/cli/ns_info.go:194`），长域名下 goroutine 数 = label 数 - 1。
- **`reorderArgs` 把 flag 移到位置参数前**——让 `example.com -t AAAA` 和 `-t AAAA example.com` 等价（`internal/cli/app.go:307`）；改解析逻辑时注意别破坏这个约定。
- **`-j` 在 `query` 是子命令 flag，在快捷查询是全局 flag**——两条路径都要识别，见 `internal/cli/main.go:107`。

## 7. 相关文档

- 上层：[ARCHITECTURE.md](./ARCHITECTURE.md)
- 承载需求：[多 Provider DNS 查询](../requirements/multi-provider-dns-query.md)
- 配套子系统：[ip-lookup.md](./ip-lookup.md)（query 流程会调用 `collectIPMatches` 补 IP 匹配详情）
