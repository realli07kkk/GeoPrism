# 架构总入口

本文是 GeoPrism 的架构总览：分层、依赖方向、数据与状态、跨子系统的架构决定与硬边界。子系统细节见 [dns-query.md](dns-query.md)、[ip-lookup.md](ip-lookup.md)、[ipdb-v2.md](ipdb-v2.md)；决策原因见 [decisions.md](decisions.md)。

<a id="scope"></a>
## 1. 定位

GeoPrism 是单进程、跑完即退的本地 CLI（纯 Go，主要在 macOS 使用，也交叉编译到 linux / windows），没有守护进程、没有 Web UI、没有后台回写队列。两条能力主线：

- **多 Provider DNS 查询**：并行向多个公共 DNS（DoH / DoT / UDP）查询并按 Provider 并排对比，附带 NS 服务器信息与反向 PTR。
- **离线 IP / CIDR 查询**：把 ipinfo Lite CSV 构建成本地 Pebble 库，断网做单 IP 最长前缀匹配（LPM）或 CIDR 相交检索；本地未命中时回退 ipinfo 在线查询。

<a id="layers"></a>
## 2. 分层与依赖方向

```text
main.go                      薄入口：只调用 internal/cli.Main(os.Args[1:])
  └── internal/cli           编排层：路由、flag 归一、App 生命周期、IP/CIDR/NS 编排、视图 DTO
        ├── backend/provider  providers.toml 读写与校验（TOML 声明顺序 = canonical 顺序）
        ├── backend/resolver  DoH / DoT / UDP 查询、并行 QueryMulti、归一化、连通性测试
        ├── backend/ipdb      离线库构建（builder）、v2 编码（codec）、查询（store_v2）、overlay、文件锁
        ├── backend/ipinfo    ipinfo Lite HTTP 客户端
        ├── backend/settings  settings.toml（ipinfo_token、data_source_priority）
        └── render            展示层：TTY（lipgloss）/ 非 TTY（tabwriter）双协议、统一 JSON 输出
```

依赖规则：

- `main.go` → `internal/cli` → `backend/*` / `render`，单向。
- `backend/*` 不得反向 import `internal/cli`。
- `backend/ipinfo` 依赖 `backend/ipdb` 的 `Record` 类型（`Response.ToRecord`），这是能力层内部唯一的横向依赖。
- `render` 只依赖调用方传入的 `*Source` 接口（`QueryResultSource`、`IPLookupSource`、`CIDRLookupSource` 等），不 import `internal/cli`；`internal/cli` 依赖 `render` 的具体函数与 `OutputMode`。

<a id="state"></a>
## 3. 数据与状态

| 位置 | 内容 | 读写方 |
|---|---|---|
| `~/.geoprism/config/providers.toml` | `[[providers]]` 数组表；缺失时写默认模板（Cloudflare / Google / Quad9 / AliDNS，均 DoH） | `provider.ProviderStore`（load / save）；CLI 对它只读 |
| `~/.geoprism/config/settings.toml` | `[settings]`：`ipinfo_token`、`data_source_priority` | `settings.SettingsStore`（只读）；缺失时写默认模板 |
| `~/.geoprism/ipdb/CURRENT` | 当前激活 base 的 buildID | builder 写、`ipdb.OpenCurrent` 读 |
| `~/.geoprism/ipdb/versions/{buildID}/db` | 不可变 v2 base 库（Pebble，ReadOnly 打开） | builder 创建；`ipdb.BaseStore` 读 |
| `~/.geoprism/ipdb/versions/.staging-*/` | 构建中间目录 | builder 独占 |
| `~/.geoprism/ipdb/BUILD.lock`、`VERSIONS.lock` | 构建串行化 / 版本生命周期锁 | builder、reader |
| `~/.geoprism/ipdb/overlay/` | 独立单 IP 缓存（`OverlayStore`，**已实现但尚未接入 CLI**） | `ipdb.OverlayStore` |

路径由 `internal/cli/paths.go` 统一拼装（`getUserHomeDir` → `~/.geoprism` → `config/` 与 `ipdb/`）。

运行期状态只在 `App` 内（provider 句柄、resolver、懒加载的 `*ipdb.Store`、settings、ipinfo 客户端），进程退出即释放。

离线库初始化语义（`App.ensureIPDBStore`）：首次需要时才 `OpenCurrent`；失败**不中断主流程**，只记录一条 warning（缺库 → "未找到可用的离线 IP 库，跳过 IP 匹配"；其它错误 → "加载离线 IP 库失败，跳过 IP 匹配: …"），在 IP / CIDR / query 输出结果前经 stderr 打印一次（`printIPDBWarning`），不污染 stdout 协议。

<a id="decisions"></a>
## 4. 跨子系统的架构决定

| 决定 | 要点 | 详见 |
|---|---|---|
| D-01 base 只读 | ipdb base keyspace 运行期禁止写入；在线结果只能进独立 overlay；当前所有回写停用 | [decisions.md](decisions.md#d-01) |
| D-02 标准库 flag | `os.Args` + `flag.NewFlagSet`，不引入 CLI 框架；`reorderArgs` 让 flag 与位置参数顺序等价 | [decisions.md](decisions.md#d-02) |
| D-03 Pebble | LSM 便于前缀 / 区间扫描，纯 Go，无 CGO | [decisions.md](decisions.md#d-03) |
| D-04 渲染双协议 | TTY 走 lipgloss 增强；非 TTY 保持纯文本 tabwriter 协议；JSON 由 `render.WriteJSON` 统一 | [decisions.md](decisions.md#d-04) |
| D-05 CIDR 固定 ipdb 优先 | `geoprism <cidr>` 不看 `data_source_priority`；ipdb 完全未命中才以基地址回退单 IP 查询 | [decisions.md](decisions.md#d-05) |
| D-06 结果保序 | `QueryMulti` 按 index 归位；`ProviderStore` 以 TOML 声明顺序为 canonical | [decisions.md](decisions.md#d-06) |
| D-12 格式升级即重建 | 不做 v1 → v2 数据级迁移；旧库报 `ErrLegacyFormat` 并提示重建 | [decisions.md](decisions.md#d-12) |
| D-13 切换原子性 | 公开 `BuildFromCSV` / `OpenCurrent` 一次性切 v2，用 `Store` 过渡壳保持 cli 调用点零 diff | [decisions.md](decisions.md#d-13) |
| D-14 构建锁序 | `BUILD.lock`（独占）→ `VERSIONS.lock`（发布 / 回收独占，reader 共享） | [decisions.md](decisions.md#d-14) |

<a id="boundaries"></a>
## 5. 硬边界

- `backend/*` 不得 import `internal/cli`。
- 旧的 `providers.json` 不读、不迁移；仅在首次写默认模板时检测到旧文件则输出手动迁移警告（`provider.ProviderStore.ensureConfigFile`）。
- 任何路径不得向 ipdb base 写入（`Store.WriteRecord` 显式返回错误，见 D-01）。
- warning / 错误走 stderr；stdout 只承载结果协议（文本表格或 JSON）。JSON 模式下错误格式固定为 `{"error":"..."}`。
- `providers.toml` / `settings.toml` 含未知字段时加载失败（TOML `Undecoded()` 非空即报错），避免拼写错误静默失效。
- 非 TTY 文本输出是脚本协议，改列 / 分隔符属破坏性变更；JSON 字段同理。

<a id="anchors"></a>
## 6. 代码锚点

| 关注点 | 位置 |
|---|---|
| CLI 入口与路由 | `main.go`；`internal/cli/main.go` `Main` / `knownCommands` / `parseIPInput` |
| App 装配与资源释放 | `internal/cli/app.go` `NewApp` / `Close` / `ensureIPDBStore` / `recordIPDBInitError`；`internal/cli/ip_match.go` `printIPDBWarning` |
| 域名查询编排 | `internal/cli/app.go` `runQuery` / `QueryDomain` / `reorderArgs` / `matchProvidersByName` |
| Provider 列举与连通性 | `internal/cli/app.go` `runProviders` / `runTest` / `TestProvider` |
| 单 IP / CIDR 查询 | `internal/cli/ip_lookup.go` `LookupIP` / `runIPLookup`；`internal/cli/cidr_lookup.go` `LookupCIDR` / `runCIDRLookup` |
| ipdb + ipinfo 合并 | `internal/cli/ip_merge.go` `mergeIPInfo` / `lookupIPInfoSync` |
| 域名结果的 IP 匹配详情 | `internal/cli/ip_match.go` `collectIPMatches` / `extractAnswerIPs` |
| NS 服务器信息 | `internal/cli/ns_info.go` `queryNSInfo` / `discoverNSZone` / `buildNSZoneCandidates` / `queryNSIPs` |
| 反向 PTR | `internal/cli/reverse.go` `ipToReverseName` |
| ipdb 构建命令 | `internal/cli/ipdb_cmd.go` `runIPDBBuild` |
| 数据目录 | `internal/cli/paths.go` `getConfigDir` / `getIPDBDir` / `ensureConfigDirReady` |
| DNS 解析 | `backend/resolver/resolver.go` `QueryMulti` / `Query` / `QueryDoH` / `QueryDoT` / `QueryDNS` / `TestConnection` / `normalizeAnswer` |
| Provider 存储 | `backend/provider/provider.go` `ProviderStore` `load` / `save` / `List` / `GetEnabled` / `Upsert` / `Delete` |
| 应用配置 | `backend/settings/settings.go` `NewSettingsStore` |
| ipinfo 客户端 | `backend/ipinfo/client.go` `Client.LookupIP` / `Response.ToRecord` |
| ipdb 公开入口 | `backend/ipdb/builder.go` `BuildFromCSV`；`backend/ipdb/store.go` `OpenCurrent` / `Store` |
| ipdb v2 内部 | `backend/ipdb/builder_v2.go` `buildV2FromCSV` / `writeV2Records` / `allocateDefaultBuildID`；`backend/ipdb/store_v2.go` `BaseStore` `LookupIP` / `LookupCIDR`；`backend/ipdb/codec.go`；`backend/ipdb/overlay.go` `OverlayStore`；`backend/ipdb/file_lock_*.go` |
| 渲染 | `render/output.go` `OutputMode` / `WriteJSON`；`render/style.go` `isTTY`；`render/query.go` / `iplookup.go` / `cidrlookup.go` / `ipmatch.go` / `ns_info.go` / `provider.go` |
