# 多 Provider DNS 查询子系统

本文覆盖 `geoprism query` / `geoprism <domain>`、`geoprism providers`、`geoprism test`、反向 PTR、NS 服务器信息，以及它们共用的 Provider 配置与结果保序契约。整体分层见 [architecture.md](architecture.md)。

<a id="scope"></a>
## 1. 能力与边界

**用户故事**

- 一条命令并行问多个公共 DNS，并排对比 A / AAAA / CNAME / TXT / NS / MX / SOA / PTR 结果，交叉验证是否存在投毒或劫持。
- 迁移到 DoH / DoT 之前，先测各 Provider 的连通性与延迟。
- 查完域名后自动追加该域名真实 zone 的权威 NS 列表及其 IP 归属。
- 给一个 IP，反向查 PTR。

**为什么需要**：不同公共 DNS 对同一域名的回答可能不一致；DoH / DoT 迁移有连通性风险；用户需要在本地一次性看到全貌。

**怎么解决**

- Provider 以 `providers.toml` 声明，查询时并行发起、结果按声明顺序并排展示。
- `-p/--provider` 按名称过滤子集；`test` 子命令探测连通性。
- 主查询后自动沿 DNS label 向上发现真实 zone，并列出 NS 的 A / AAAA 与离线库归属。
- `-x/--ptr` 把 IP 转成 `in-addr.arpa` / `ip6.arpa` 后查 PTR。

**边界（明确不做）**

- 只查询、不做劫持判定；不写任何 DNS 缓存。
- 连通性测试只报通 / 不通与延迟，不报丢包率、TLS 细节。
- 纯 CLI，无常驻服务、无 Web UI。
- 只读 `providers.toml`；旧的 `providers.json` 不读、不迁移（见 [architecture.md §5](architecture.md#boundaries)）。

<a id="entry"></a>
## 2. 入口与编排

`internal/cli/main.go` `Main`：

1. 无参数 → 打印 usage 退出 0。
2. `NewApp` 初始化（见 [architecture.md §3](architecture.md#state)）。
3. 剥离**开头连续**的 `-j/--json`（全局 flag）；若紧随其后的已知命令不支持 JSON（目前只有 `ipdb`），打印警告并忽略 `-j`。
4. 任意位置的 `-x/--ptr` 被剥离并记为 `hasReverseFlag`。
5. 首个参数不是已知命令（`query` / `ipdb` / `providers` / `test` / `help`）时按内容分发：`netip.ParseAddr` 成功 → 单 IP；`netip.ParsePrefix` 成功 → CIDR（输入会被 `Masked()` 规范化）；否则 → 域名查询。
6. 快捷反向查询 `geoprism -x <ip>` 等价于 `query <ip> -x`。

`internal/cli/app.go` `runQuery`：

- `flag.NewFlagSet("query", flag.ExitOnError)`；`-t/--type`（默认 A）、`-p/--provider`（逗号分隔名称）、`--timeout`（默认 5000ms）、`-j/--json`、`-x/--ptr`。
- `reorderArgs` 把 flag 挪到位置参数前，因此 `geoprism example.com -t AAAA` 与 `geoprism -t AAAA example.com` 等价（D-02）。
- `-x` 时校验输入为合法 IP，转成反向域名并把记录类型强制为 PTR。
- `-p` 走 `matchProvidersByName`（大小写不敏感，按 `Name` 匹配，取首个命中）；一个都没匹配到则报错退出 1。
- 调 `QueryDomain` 后，先 `printIPDBWarning`（stderr），再按输出模式写 `QueryResultView`；文本模式依次渲染查询表、`IP 匹配详情`、`NS 服务器信息`。

`QueryDomain`：

- `ProviderIDs` 为空 → 取 `GetEnabled()` 全部启用 Provider；否则按 ID 逐个 `Get`。
- 无可用 Provider → 返回 `no providers available`。
- `Timeout <= 0` → 5000ms。
- 调 `resolver.QueryMulti`，把 `resolver.DNSAnswer` 映射成带 `provider_name` 的 `QueryAnswer`（顺序一一对应）。
- `collectIPMatches` 收集 A / AAAA 记录的离线匹配（见 [ip-lookup.md §4](ip-lookup.md#ip-matches)）。
- **始终**调 `queryNSInfo`（即使主查询全部失败）。

<a id="providers"></a>
## 3. Provider 配置（`providers.toml`）

文件：`~/.geoprism/config/providers.toml`，`[[providers]]` 数组表。默认模板内嵌于 `backend/provider/defaults/providers.toml`（Cloudflare / Google / Quad9 / AliDNS，均 `doh`、443、5000ms、enabled）。

| 字段 | 含义 | 校验（`validateProvider`） |
|---|---|---|
| `id` | 唯一标识，JSON `provider_id` | 非空；同文件内不得重复 |
| `name` | 展示名，`-p` 按它匹配 | 非空（**唯一性未校验**，见 §10） |
| `protocol` | `doh` / `dot` / `dns` | 三者之一 |
| `endpoint` | DoH URL | `doh` 时非空 |
| `server_name` | DoT SNI / DoT·DNS 地址 | `dot`、`dns` 时非空 |
| `port` | 端口 | `> 0` |
| `timeout_ms` | 超时 | `> 0` |
| `enabled` | 是否参与默认查询 | — |
| `tags` | 标签 | — |

`provider.ProviderStore` 语义（D-06）：

- `load` 按 TOML 声明顺序填充 `providers map` 与 `order []string`；文本字段 `TrimSpace`；TOML 含未知字段（`Undecoded()` 非空）→ 加载失败。
- `List` / `GetEnabled` 按 `order` 输出；`save` 按 `order` 写回，**不再**按 ID 排序（旧版 `sort.Strings(ids)` 会永久改写用户声明顺序，已移除）。
- `Upsert`：已存在保留原位，新增追加末尾；`Delete` 同步从 `order` 移除。
- 升级语义：只对升级后新 load / save 保序，不回填历史已被打乱的顺序。
- 文件缺失 → 写默认模板；此时若同目录存在 `providers.json`，向 stderr 输出"请手动迁移"警告，不读取其内容。

<a id="query"></a>
## 4. 查询执行（`backend/resolver`）

- 记录类型：A / AAAA / CNAME / TXT / NS / MX / SOA / PTR（`RecordType*`）；未知类型回落为 A（`getRecordTypeCode`）。
- 三种协议共用 `*DNSAnswer`：
  - `QueryDoH`：GET `{endpoint}?dns={base64url(msg)}`，`Accept: application/dns-message`；非 200 → `RCodeName="HTTP <code>"`、`Error=body`、`Success=false`（不返回 Go error）。
  - `QueryDoT`：每次查询新建 TLS 连接（`InsecureSkipVerify=false`），2 字节长度前缀 + `io.ReadFull`，用完即关。
  - `QueryDNS`：`miekg/dns` UDP 客户端；交换失败 → `RCodeName="ERROR"`、`Success=false`。
- 超时：`Query` 以 `DNSQuery.Timeout`（ms）为准，0 → 5s；HTTP 客户端整体 10s。
- `Success` 定义为 `RCode == NOERROR`；`RCodeName` 映射 NOERROR / FORMERR / SERVFAIL / NXDOMAIN / NOTIMP / REFUSED / `UNKNOWN(n)`；`TTL` 取首条 answer 的 TTL。
- 答案 `Data` 去掉末尾点（`stripTrailingDot`）。

**并行与顺序契约（`QueryMulti`，D-06）**

- `Answers` 预分配 `len(providers)`，每个 goroutine 带着自己的 index 回传，结果写到 `Answers[index]`。
- `normalizeAnswer` 把三种情况统一成占位结果并**占据原 index**：Go error → `Success=false, RCodeName="ERROR", Error=err`；`answer == nil && err == nil` → `Error="查询返回空响应"`；正常 → 原样。
- 不过滤、不排序、不挪位。`QueryMulti` 自身不做超时聚合，`TotalTime` 为最慢 Provider 的墙钟时间。
- `queryFn` 字段仅供测试注入（`NewResolverWithQueryFunc`），生产路径为 nil。

<a id="ns"></a>
## 5. NS 服务器信息

`queryNSInfo` 不猜 apex，而是沿 label 向上发现真实 zone（D-07）：

1. `buildNSZoneCandidates("a.b.example.com")` → `[a.b.example.com, b.example.com, example.com]`（去掉 TLD 本身）。
2. `discoverNSZone` 对所有候选**并行**查 NS；按候选顺序（最具体优先）取第一个能提取出 NS 名称的候选作为 `ResolvedZone`。
3. `extractNSNames` 汇总所有 Provider 的 NS 记录，去重并按名称排序。
4. `queryNSIPs` 对每个 NS 名称并行查 A 与 AAAA（`querySingleNS`），合并所有成功 Provider 的 IP 并去重；若离线库可用，对每个 IP 做 `ipdbStore.LookupIP` 填充归属；IP 列表按"先 A 后 AAAA，再按字符串"排序。
5. 离线库句柄在串行路径上一次性 `ensureIPDBStore` 后传入，避免并发 data race。

错误选择（给用户一个确定、可解释的错误）：

- 单次查询 `selectNSQueryError`：NXDOMAIN > SERVFAIL > REFUSED > 其它 ERROR（去重排序取首）> "未找到 NS 记录"。
- 整条探测链 `selectNSDiscoveryError`：只要任一候选出现 **NOERROR 且无记录**，就判为"未找到可用 NS zone"（中间层不存在 NS，而不是原始域名不存在）；否则取最后一个确定性错误。
- 单个 NS 无 IP 时 `selectNSError` 同优先级，兜底"未找到 IP 地址"。

<a id="ptr"></a>
## 6. 反向 PTR

`geoprism -x <ip>` / `geoprism query <ip> -x`：`internal/cli/reverse.go` `ipToReverseName` 把 IPv4 转为 `d.c.b.a.in-addr.arpa`，IPv6 展开为 32 个十六进制数字倒序后接 `.ip6.arpa`，然后以 PTR 类型走普通 `QueryDomain` 流程。

<a id="test"></a>
## 7. 连通性测试（`geoprism test`）

- `--all` 遍历 `providerStore.List()`（含未启用）；否则按名称匹配单个。
- `TestProvider` → `resolver.TestConnection`：固定查 `google.com` A，超时取 Provider 自身 `timeout_ms`（0 → 5s）；返回 `(success, message, rttMs)`，message 为 `OK` / `RCodeName` / 错误文本。
- CLI 结果 `providerTestResult`：`provider_name`、`success`、`latency_ms`、`message`；文本状态 `OK` / `FAIL`（探测失败）/ `ERROR`（执行错误，如 Provider 不存在）。

<a id="render"></a>
## 8. 渲染与 JSON

- 文本：TTY 用 lipgloss 表格（`render/style.go` `isTTY`），非 TTY 用 tabwriter；列：Provider / 状态 / 延迟 / 记录 / TTL，末尾"总耗时"。失败行状态显示 `ERROR`，记录列显示错误文本。
- `providers` JSON：`[]ProviderView`（`id`、`name`、`protocol`、`endpoint`、`server_name`、`port`、`timeout`、`enabled`、`tags`），按声明顺序。
- `test` JSON：`[]providerTestResult`。
- `query` JSON（`QueryResultView`）：

| 字段 | 说明 |
|---|---|
| `domain` / `record_type` / `total_time_ms` | 查询参数与总耗时 |
| `answers[]` | 与 Provider 顺序一一对应：`provider_id`、`provider_name`、`rcode`、`rcode_name`、`answers[]`（`type`/`name`/`ttl`/`data`）、`ttl`、`rtt_ms`、`error`（omitempty）、`success` |
| `ip_matches[]` | 见 [ip-lookup.md §4](ip-lookup.md#ip-matches) |
| `ns_info` | `query_domain`、`resolved_zone`（omitempty）、`servers[]`（`name`、`ips[]`：`ip`/`record_type`/`matched` + 归属字段、`query_time_ms`、`error` omitempty）、`query_time_ms`、`available`、`error`（omitempty） |

- JSON 模式下错误输出到 stderr：`{"error":"..."}`。

<a id="anchors"></a>
## 9. 代码锚点

| 关注点 | 位置 |
|---|---|
| 路由 / usage | `internal/cli/main.go` `Main` / `usage` / `parseIPInput` |
| query 编排 | `internal/cli/app.go` `runQuery` / `reorderArgs` / `matchProvidersByName` / `QueryDomain` / `extractRecordData` |
| providers / test | `internal/cli/app.go` `runProviders` / `runTest` / `TestProvider` / `providerTestResult` |
| NS 探测 | `internal/cli/ns_info.go` `queryNSInfo` / `discoverNSZone` / `buildNSZoneCandidates` / `queryNSIPs` / `selectNSQueryError` / `selectNSDiscoveryError` / `selectNSError` |
| 反向域名 | `internal/cli/reverse.go` `ipToReverseName` |
| 解析器 | `backend/resolver/resolver.go` `QueryMulti` / `normalizeAnswer` / `Query` / `QueryDoH` / `QueryDoT` / `QueryDNS` / `TestConnection` |
| Provider 存储 | `backend/provider/provider.go` `ProviderStore` / `validateProvider` |
| 渲染 | `render/query.go` `WriteQueryResult`；`render/provider.go` `WriteProviders` / `WriteTestResults`；`render/ns_info.go` `WriteNSInfo` |

<a id="tests"></a>
## 10. 测试

- `backend/resolver/resolver_test.go`：`TestQueryMultiPreservesInputOrderUnderReverseCompletion`、`TestQueryMultiErrorFallsAtOriginalIndex`、`TestQueryMultiNilAnswerFallsAtOriginalIndex`、`TestQueryMultiOrderStableUnderConcurrency`、`TestNormalizeAnswer`。
- `backend/provider/provider_test.go`：默认模板写入、合法 TOML、Upsert/Delete 持久化、非法配置报错、忽略 legacy JSON、`List` / `GetEnabled` 保序、Upsert 保位 / 追加。
- `internal/cli/cli_test.go`：help、providers/query/test JSON、命令路由、flag 不被吞、`-j` 不支持警告、错误 JSON 协议。
- `internal/cli/provider_order_test.go` `TestQueryDomainExplicitProviderOrderPreserved`。
- `internal/cli/ns_info_test.go`：NSInfo 进 JSON / 文本、NXDOMAIN 错误、NS 顺序确定、`selectNSQueryError` / `selectNSDiscoveryError`、`buildNSZoneCandidates`。

<a id="open"></a>
## 11. 已知约束与未决事项

- **DoT 不复用连接**：每次查询新建 TLS 连接，延迟偏高；当前接受，未计划改。
- **连通性探测固定查 `google.com`**：在不可达网络下会误判为不通；未做成可配置。
- **Provider `name` 唯一性未校验**（`load` 只校验 `id` 重复）：同名时 `-p` 匹配到第一个；待决定是否在 `validateProvider` 层拒绝。
- **JSON 协议无 `schema_version`**：当前字段集即为契约，版本化是独立需求，未启动。
- NS 探测策略（沿 label 向上、最具体优先）此前只写在架构文档里，现正式记录为 [D-07](decisions.md#d-07)。
