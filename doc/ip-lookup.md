# 离线 IP / CIDR 查询子系统

本文覆盖 `geoprism <ip>`、`geoprism <cidr>`、域名结果里的 `IP 匹配详情`、ipinfo 在线合并、`data_source_priority` 与 `Source` 列。ipdb 内部（编码、构建、LPM、overlay、锁）见 [ipdb-v2.md](ipdb-v2.md)。

<a id="scope"></a>
## 1. 能力与边界

**用户故事**

- 把 IP 地理库导入本地，断网也能查 IP 归属。
- 给一个 CIDR，一次看到与它相交的所有离线网段记录。
- 本地没有时回退 ipinfo 在线查询，结果标注来源；可配置哪个数据源优先。

**为什么需要**：在线查询依赖网络与 token，且不适合批量；离线库 + 在线兜底能同时满足断网与覆盖率。

**怎么解决**

- `geoprism ipdb build --csv <绝对路径>` 把 ipinfo Lite CSV 构建为本地 Pebble 库。
- 单 IP：先查 ipdb（最长前缀匹配），再按优先级与 ipinfo 结果合并，`Source` 标 `ipdb` / `ipinfo`。
- CIDR：直接查 ipdb 返回所有相交网段；ipdb 完全未命中才以 CIDR 基地址回退单 IP 流程。
- `settings.toml` 的 `data_source_priority` 决定单 IP 合并时谁优先。

**边界**

- 只查归属（国家 / 大洲 / ASN），不做威胁情报。
- 库不自动更新；更新 = 重新 `ipdb build`。
- ipinfo 依赖 `ipinfo_token`；token 为空时在线查询整体关闭。
- 同一次运行内同一 IP 只调一次 ipinfo。
- `data_source_priority` 只影响单 IP 合并，不影响 CIDR 主路径（D-05）。
- 无浏览 / 编辑离线库的界面。

<a id="status"></a>
## 2. 现状摘要（2026-11 核对代码）

| 项 | 状态 |
|---|---|
| base 库格式 | v2（`FormatVersion=2`，primary + cidr 双索引），`OpenCurrent` ReadOnly 打开 |
| 旧 v1 库 | `OpenCurrent` 返回 `ErrLegacyFormat` → CLI 以 warning 提示重建，不中断 DNS 查询 |
| 在线回写 | **已停用**。历史上 ipinfo 结果会异步写回 base，破坏 LPM 不变量（见 [history.md](history.md#writeback)）；现 `ip_merge.go` 只剩合并逻辑，`Store.WriteRecord` 显式返回只读错误（D-01） |
| overlay 缓存 | `ipdb.OverlayStore` 已实现并有完整测试，但**尚未接入 `App` / CLI**；CLI 目前不读也不写 overlay |
| 待接的 integration | `ipdb-lookup-integration` 状态 planned，见 §10 |

<a id="single-ip"></a>
## 3. 单 IP 查询（`geoprism <ip>`）

`internal/cli/ip_lookup.go` `LookupIP(ip)`：

1. `net.ParseIP` 校验，非法 → `IP 格式非法`。
2. **Step 1 ipdb**：`ensureIPDBStore()` 非空则 `ipdbStore.LookupIP(ip)`（真 LPM，见 [ipdb-v2.md §9](ipdb-v2.md#lookup)）；出错直接返回 error。
3. **Step 2 ipinfo**：`hasIPInfoLookup()` 为真则 `lookupIPInfoSync(ip)`（同步 HTTP，ctx 5s；失败只写 `log`，返回 nil）。
4. 既无 ipdb 又无 ipinfo 结果 → `ipdbLookupError()`：缺库 → `未找到可用的离线 IP 库，请先执行 geoprism ipdb build --csv <absolute-path>`；加载失败 → `加载离线 IP 库失败: …`；其它 → `离线 IP 库不可用`。
5. **Step 3 合并**：`mergeIPInfo`（§5）。
6. **Step 4 视图**：`newIPLookupView` + `Source`。

`runIPLookup`：`flag.ContinueOnError` + `io.Discard`，解析前先用 `hasJSONFlag` 探测 `-j` 以保证错误也走 JSON 协议；只接受恰好 1 个参数；输出前 `printIPDBWarning`（stderr）。

**JSON（`IPLookupView`）**：`ip`、`matched`、`network`、`country`、`country_code`、`continent`、`continent_code`、`asn`、`as_name`、`as_domain`、`source`（omitempty，`ipdb` / `ipinfo`）。未命中时只有 `ip` 与 `matched=false`。

<a id="ip-matches"></a>
## 4. 域名结果的 IP 匹配详情（`collectIPMatches`）

`internal/cli/ip_match.go`，在 `QueryDomain` 内调用：

- 只处理 `success=true` 的 Provider，且只提取 A / AAAA 记录里能被 `net.ParseIP` 解析的 `data`。
- 既无 ipdb 也无 ipinfo → 返回 nil（不渲染该表）。
- `seen` map 缓存本次运行内每个 IP 的 `(match, source)`，同一 IP 跨 Provider 复用，**ipinfo 只调一次**。
- 单个 IP 的匹配逻辑：ipdb 命中 → `source=ipdb`；`needIPInfo = hasIPInfo && (!matched || ipinfo-first)` 为真时调 ipinfo，有结果则整体替换为 ipinfo 记录、`source=ipinfo`。
- ipdb 查询出错 → 设置 warning"离线 IP 匹配失败，已跳过后续匹配"并**返回已收集的部分**（软失败，不让 DNS 主流程失败）。
- 不回写（`ip_lookup.go` / `ip_match.go` 注释明确说明停用原因）。

**JSON（`IPMatchView`）**：`provider`、`record_type`、`ip`、`matched`、`source`（omitempty）+ 归属字段（`network`、`country`…）。

<a id="cidr"></a>
## 5. CIDR 查询（`geoprism <cidr>`）

`internal/cli/cidr_lookup.go` `LookupCIDR(cidr)`：

1. `netip.ParsePrefix` 并 `Masked()` 规范化。
2. ipdb 可用则 `ipdbStore.LookupCIDR`（三段查询，见 [ipdb-v2.md §9](ipdb-v2.md#lookup)）。
3. 有记录 → `Matched=true`，每条 `Source="ipdb"`，**直接返回**（不看优先级、不调 ipinfo）。
4. 无记录或 ipdb 不可用 → 以 `queryPrefix.Addr()` 调 `LookupIP`，结果放进 `Fallback`。

**JSON（`CIDRLookupView`）**：`query_cidr`、`matched`、`match_count`、`matches[]`（`source` + 归属字段）、`fallback`（可选，`IPLookupView`）。文本渲染见 `render/cidrlookup.go`。

<a id="merge"></a>
## 6. 数据源优先级与来源标识（`mergeIPInfo`）

`internal/cli/ip_merge.go`，输入 `(ip, ipdbMatch, ipinfoResp)`，输出 `(Match, source)`：

| 条件 | 结果 | `source` |
|---|---|---|
| ipinfo 无结果，ipdb 命中 | ipdb 记录 | `ipdb` |
| ipinfo 无结果，ipdb 未命中 | 空 | `none` |
| `ipinfo-first` 且 ipinfo 有结果 | ipinfo 记录 | `ipinfo` |
| `ipdb-first`，ipdb 命中 | ipdb 记录 | `ipdb` |
| `ipdb-first`，ipdb 未命中，ipinfo 有结果 | ipinfo 记录 | `ipinfo` |

`lookupIPInfoSync`：优先用测试注入的 `App.ipinfoLookup`；否则 `ipinfoClient.LookupIP(ctx 5s)`，出错 `log.Printf` 后返回 nil。

<a id="ipinfo"></a>
## 7. ipinfo 客户端（`backend/ipinfo`）

- `Client.LookupIP(ctx, ip)`：GET `https://api.ipinfo.io/lite/{ip}?token={token}`，HTTP 客户端整体 10s；非 200 → `ipinfo 返回状态码 N`；JSON 解析到 `Response`（`ip`、`country`、`country_code`、`continent`、`continent_code`、`asn`、`as_name`、`as_domain`）。
- `Response.ToRecord()`：转 `ipdb.Record`，`Network` 固定为 `{ip}/32` 或 `{ip}/128`（按是否含 `:` 判断 IPv6）。
- 这是 `backend/ipinfo` → `backend/ipdb` 的唯一类型依赖（见 [architecture.md §2](architecture.md#layers)）。
- token 来自 `settings.toml`；`NewApp` 仅在 `IsIPInfoEnabled()` 为真时创建客户端。

<a id="settings"></a>
## 8. 应用配置（`settings.toml`）

文件 `~/.geoprism/config/settings.toml`，表 `[settings]`：

| 字段 | 默认 | 说明 |
|---|---|---|
| `ipinfo_token` | 空 | 空 → 不启用 ipinfo 在线查询 |
| `data_source_priority` | `ipdb-first` | `ipdb-first` / `ipinfo-first`；空值回落 `ipdb-first` |

- 文件缺失 → 写默认模板（`backend/settings/defaults/settings.toml`）。
- 未知字段 → 加载失败。`NewApp` 对加载失败**只打 warning 不退出**，此时 `settings == nil`：ipinfo 关闭、优先级按 `ipdb-first`。
- `SettingsStore` 对 nil 接收者安全（`DataSourcePriority()` 返回默认）。

<a id="anchors"></a>
## 9. 代码锚点

| 关注点 | 位置 |
|---|---|
| 单 IP | `internal/cli/ip_lookup.go` `LookupIP` / `runIPLookup` / `ipdbLookupError` / `newIPLookupView` |
| 域名 IP 匹配 | `internal/cli/ip_match.go` `collectIPMatches` / `extractAnswerIPs` / `printIPDBWarning` |
| CIDR | `internal/cli/cidr_lookup.go` `LookupCIDR` / `runCIDRLookup` |
| 合并 / ipinfo 调用 | `internal/cli/ip_merge.go` `mergeIPInfo` / `lookupIPInfoSync` |
| ipinfo 客户端 | `backend/ipinfo/client.go` `Client.LookupIP` / `Response.ToRecord` |
| 配置 | `backend/settings/settings.go` `NewSettingsStore` / `DataSourcePriority` / `IsIPInfoEnabled` |
| 离线库懒加载 | `internal/cli/app.go` `ensureIPDBStore` / `recordIPDBInitError` |
| 渲染 | `render/iplookup.go` `WriteIPLookup`；`render/ipmatch.go` `WriteIPMatches`；`render/cidrlookup.go` `WriteCIDRLookup` |

<a id="tests"></a>
## 10. 测试

- `internal/cli/ip_lookup_test.go`：`TestAppLookupIP`、无库软失败。
- `internal/cli/ip_match_test.go`：`TestExtractAnswerIPs`、`TestWriteIPMatches`。
- `internal/cli/cidr_lookup_test.go`：`TestAppLookupCIDR`。
- `internal/cli/issue_writeback_fix_test.go`：`TestIssueWritebackDisabled_NoBaseMutation`（确保查询路径不改 base）、`TestCLIV1RebuildHint`（v1 库降级提示）。
- `internal/cli/cli_test.go`：IP / CIDR 的 JSON 与文本输出、无库时的 JSON / 文本、flag 错误走 JSON、v1 库优雅降级。
- `render/iplookup_test.go`、`render/cidrlookup_test.go`。

<a id="open"></a>
## 11. 未决事项：`ipdb-lookup-integration`（planned）

目标是把已落地的 base v2 + overlay 正式接入 CLI，取代 §2 的"回写全部停用"止血状态。依赖 `ipdb-v2-query`（done）与 `ipdb-overlay-store`（done）。设计契约（尚未实现，实现时可调整但需保留语义）：

**App 双句柄、各自降级**

```go
type App struct {
    // ...
    ipdbBase       *ipdb.BaseStore
    ipdbOverlay    *ipdb.OverlayStore
    ipdbBaseErr    error
    ipdbOverlayErr error
}
```

**三来源候选模型 + 纯函数选择**

```go
type CandidateOrigin uint8
const (
    CandidateBase    CandidateOrigin = iota // Source 展示 "ipdb"
    CandidateOverlay                        // Source 展示 "ipinfo"
    CandidateLive                           // Source 展示 "ipinfo"
)
type IPCandidate struct {
    Origin    CandidateOrigin
    Match     ipdb.Match
    Source    string    // "ipdb" 或 "ipinfo"
    FetchedAt time.Time // 仅 overlay / live 有意义
}
func selectCandidate(priority settings.DataSourcePriority, candidates []IPCandidate) (IPCandidate, bool)

const defaultOverlayTTL = 24 * time.Hour
```

约束：

- `selectCandidate` 是纯函数：只从已取得的候选中选择，不发 HTTP、不决定 short-circuit、不写 overlay、不输出 warning。
- 同步回写：live 查询成功后由编排代码 `OverlayStore.Put(...)`（`ExpiresAt = now + defaultOverlayTTL`，建议注入 clock）；失败仅 warning；**即使最终选了 base，只要 live 请求已发生也写 overlay**。
- 删除 / 废弃 `Store.WriteRecord`；任何路径不得再向 base 写单 IP。
- 拆掉 `Store` 过渡壳（[ipdb-v2.md §8](ipdb-v2.md#open)），cli 改持 `*BaseStore` / `*OverlayStore`。
- 合并仍遵循现有 `data_source_priority` 语义，不顺带统一在线查询语义。

组件级降级矩阵：

| 状态 | 行为 |
|---|---|
| base v2 正常 + overlay 正常 | 两者都可用 |
| base 为 v1 | 忽略 base、提示重建（`ErrLegacyFormat`），overlay / live 仍可用 |
| base 缺 capability | 忽略 base、提示重建（`ErrIncompleteSchema`），overlay / live 仍可用 |
| base 被另一进程锁定 | base 不可用，overlay / live 继续 |
| overlay 被锁定（`ErrOverlayLocked`） | 跳过缓存（不读不写），base / live 继续 |
| overlay metadata 损坏 | 隔离或禁用 overlay，base / live 继续 |
| 无 base | overlay / live 仍可用于单 IP |

warning 一律走 cli stderr。落地后需同步：回写本文件 §2/§3/§4 与 [architecture.md](architecture.md) §3，并在 [history.md](history.md#writeback) 标注"已被 A′ 取代"。

**相关但独立的后续项（未启动）**

- 在线查询语义统一 + `WriteBackQueue` 生命周期 + `App.Close` 等待在途请求（审计意见，依赖上面的 overlay 接口）。
- overlay 缓存清理 / 浏览命令（如 `ipdb overlay clear`）与容量回收策略。
- Provider `name` 唯一性校验、JSON `schema_version`（见 [dns-query.md §11](dns-query.md#open)）。
