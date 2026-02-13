# GeoPrism PRD（macOS 本地运行版，Go + Wails）

## 1. 文档摘要
GeoPrism 是一个仅在本地运行、非 App Store 分发的 macOS 图形化工具。核心能力是对同一域名并行查询多个 DNS 服务商（支持 DoH/DoT），并使用本地 IP 数据库（QQWry、ZX IPv6、ip2region）解析返回 IP 的地理/运营商信息。  
本版本按你的选择落地以下策略：
1. IP 库采用“内置下载助手”。
2. DoH 默认使用 RFC 线格式（`application/dns-message`）。
3. 安装体验目标是“用户双击即可安装可用”，采用签名+公证路径。
4. 出网边界为“查询阶段仅 DNS；库更新阶段仅白名单数据源”。

## 2. 目标与非目标
### 2.1 产品目标
1. 一次输入域名，得到多 DNS 服务商解析结果对比（A/AAAA/CNAME 等）。
2. 自动将解析出的 IP 用本地数据库补充地理与运营商信息。
3. 支持用户自定义 DNS 服务商配置，协议支持 DoH 与 DoT。
4. 所有查询结果可本地保存、筛选、导出（CSV/JSON）。
5. 运行期无遥测、无云端依赖，默认离线友好。

## 3. 用户画像与核心场景
1. 运维/网络工程师：排查 DNS 污染、区域解析不一致。
2. 安全工程师：比对不同解析器返回 IP 的归属差异。
3. 开发者：验证域名切换、CDN 解析策略、IPv4/IPv6 覆盖情况。

核心场景：
1. 快速对比：输入域名后并行查询 5-20 个 DNS 提供商。
2. 深度排查：查看每个应答的 TTL、RCode、延迟、证书/握手异常。
3. 本地情报增强：离线 IP 归属解析，多库交叉展示。

## 4. 范围定义（In/Out）
### 4.1 In Scope
1. DNS Provider 管理：新增/编辑/禁用/测试连通性。
2. DoH/DoT 查询引擎：并发查询、超时控制、重试、结果归一化。
3. IP 数据库管理：下载、导入、版本显示、启用顺序、健康检查。
4. 结果页面：表格、差异高亮、过滤、导出、历史查询。
5. 本地日志与诊断包导出。
6. macOS 打包分发（签名+公证）。

### 4.2 Out Scope
1. DNSSEC 完整校验链 UI（本期只展示 AD 位与基础字段）。
2. DoQ（DNS over QUIC）协议支持。
3. 自动后台定时更新（本期以手动触发更新为主）。

## 5. 功能需求（FR）
### FR-01 域名查询
1. 输入：域名、记录类型（A/AAAA/CNAME/TXT/NS/MX）、是否递归、超时。
2. 输出：按 Provider 分组的标准化响应。
3. 失败响应需区分：超时、TLS 失败、HTTP 错误、DNS RCode。

### FR-02 多 Provider 并行
1. 支持并发查询，默认并发上限 10，可配置。
2. 支持一键重试失败项。
3. 支持延迟排序、结果一致性分组。

### FR-03 DoH/DoT 支持
1. DoH 默认 RFC 线格式 `application/dns-message`。
2. DoT 使用 TLS（853），严格证书校验，支持 SNI 配置。
3. Provider 配置需支持自定义 URL/Host/Port/Headers（DoH）与 SNI（DoT）。

### FR-04 Provider 自定义配置
1. 字段：名称、协议、地址、端口、DoH endpoint、SNI、超时、启用状态。
2. 提供默认模板：Cloudflare、Google、Quad9。
3. 支持导入/导出 Provider 配置（JSON）。

### FR-05 IP 库解析
1. 支持 QQWry（`qqwry.dat`）、ZX IPv6（`ipv6wry.db`）、ip2region（`*.xdb`）。
2. 同一 IP 支持多库并列展示，按“命中优先级”聚合最终展示字段。
3. 若某库不支持对应 IP 版本，需显示“未覆盖”而不是失败。

### FR-06 IP 库下载与更新
1. 内置下载助手，支持白名单数据源配置。
2. 下载后执行完整性校验（大小/哈希），失败回滚旧版本。
3. 更新过程可取消，且不影响正在进行的 DNS 查询。

### FR-07 网络边界控制
1. 查询阶段：仅允许 DNS 请求（DoH/DoT）。
2. 更新阶段：仅允许访问“IP 库下载白名单域名”。
3. 禁用遥测、禁用第三方统计 SDK。

### FR-08 历史与导出
1. 保存最近 N 次查询（默认 200，可配置）。
2. 导出格式：CSV、JSON。
3. 导出内容包含 Provider、记录值、TTL、延迟、IP 归属结果、时间戳。

## 6. 非功能需求（NFR）
1. 性能：10 个 Provider 并行查询，P95 完成时间 ≤ 3 秒（网络正常）。
2. 稳定性：连续 1000 次查询无崩溃；单 Provider 故障不影响整体。
3. 安全：TLS 默认严格校验；本地敏感配置加密存储（系统 Keychain）。
4. 可维护性：DNS 协议层与 IP 解析层采用插件化接口。
5. 可用性：离线状态下可查看历史与本地 IP 解析（无查询能力）。
6. 分发体验：签名+公证，减少 Gatekeeper 阻断。

## 7. 信息架构与页面
1. 查询页：输入域名、记录类型、Provider 选择、结果表格、差异视图。
2. Provider 管理页：列表、编辑弹窗、连通性测试、导入导出。
3. IP 库管理页：库状态、版本、路径、下载更新、启用顺序。
4. 历史页：检索、复查、导出。
5. 设置页：网络边界、超时并发、日志级别、数据目录。

## 8. 技术架构
1. 框架：Wails v2（Go backend + Web frontend）。
2. 前端：TypeScript（建议 React/Vue 任一稳定模板，默认 `react-ts`）。
3. 后端模块：
- `resolver`: DoH/DoT 查询与归一化。
- `provider`: Provider 配置管理。
- `ipdb`: 多库适配器与统一查询接口。
- `updater`: 白名单下载、校验、切换版本。
- `storage`: 本地配置/历史/日志。
4. 数据目录（macOS）：
- `~/Library/Application Support/GeoPrism/config/`
- `~/Library/Application Support/GeoPrism/data/ipdb/`
- `~/Library/Application Support/GeoPrism/logs/`

## 9. 对外接口与类型（Wails Bindings）
### 9.1 核心绑定接口（新增公共接口）
1. `QueryDomain(req QueryRequest) (QueryResult, error)`
2. `ListProviders() ([]Provider, error)`
3. `UpsertProvider(p Provider) error`
4. `DeleteProvider(id string) error`
5. `TestProvider(id string) (ProviderHealth, error)`
6. `ListIPDatabases() ([]IPDBStatus, error)`
7. `UpdateIPDatabase(req UpdateDBRequest) (UpdateJob, error)`
8. `ResolveIP(ip string) ([]IPResolveHit, error)`
9. `ExportResult(req ExportRequest) (ExportFile, error)`

### 9.2 关键类型
1. `Provider`: `id,name,protocol(doh|dot),endpoint,server_name,timeout_ms,enabled,tags`
2. `QueryRequest`: `domain,record_type,provider_ids,edns_client_subnet?,timeout_ms,retry`
3. `DNSAnswer`: `provider_id,rcode,answers[],ttl,rtt_ms,error`
4. `IPResolveHit`: `db_type,ip,region,isp,raw,version`
5. `IPDBStatus`: `db_type,file_path,version,updated_at,checksum,enabled`

## 10. 数据源与下载策略
1. 默认内置 Provider 模板：
- Cloudflare（DoH/DoT）
- Google Public DNS（DoH/DoT）
- Quad9（DoH/DoT）
2. IP 库下载策略：
- 内置任务模板 + 白名单校验。
- 默认仅启用明确配置的数据源，不做隐式联网抓取。
- QQWry/ZX 来源与许可在 UI 明示“需用户确认合规使用”。

地址库和下载链接                                                                                                                                                                                                                 
  数据库: qqwry                                                                                                                                                                             
  本地文件名: qqwry.dat                                                                                                                                                                     
  下载链接: https://github.com/metowolf/qqwry.dat/releases/latest/download/qqwry.dat
  ────────────────────────────────────────
  数据库: zxipv6wry
  本地文件名: zxipv6wry.db
  下载链接: https://ip.zxinc.org/ip.7z
  ────────────────────────────────────────
  数据库: ip2region
  本地文件名: ip2region.xdb
  下载链接: https://cdn.jsdelivr.net/gh/lionsoul2014/ip2region/data/ip2region.xdbhttps://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region.xdb
  ────────────────────────────────────────
  数据库: cdn
  本地文件名: cdn.yml
  下载链接:
  https://cdn.jsdelivr.net/gh/4ft35t/cdn/src/cdn.ymlhttps://raw.githubusercontent.com/4ft35t/cdn/master/src/cdn.ymlhttps://raw.githubusercontent.com/SukkaLab/cdn/master/src/cdn.yml

## 11. 错误处理与边界场景
1. 域名非法：前端即时校验 + 后端二次校验。
2. Provider TLS 证书错误：明确错误码并建议修复字段（SNI/证书链）。
3. DoH 返回 200 但 DNS RCode 非 NOERROR：按业务失败处理并可见。
4. IP 库文件损坏：自动降级到其他可用库并告警。
5. 下载校验失败：保持旧版本，不替换。

## 12. 测试与验收标准
### 12.1 单元测试
1. DoH 报文打包/解包与错误码映射。
2. DoT 握手与超时路径。
3. Provider 配置校验器。
4. 三类 IP 库适配器读取与异常处理。

### 12.2 集成测试
1. 本地 mock DoH 服务 + mock DoT 服务并发查询。
2. 多 Provider 下超时/成功混合返回聚合。
3. IP 库更新下载-校验-切换-回滚链路。

### 12.3 E2E 验收
1. 新建 Provider -> 连通性测试 -> 查询成功。
2. 下载并启用 IP 库 -> 查询结果出现归属信息。
3. 关闭网络后历史可查，查询提示符合预期。
4. 导出 CSV/JSON 可被外部工具正确读取。

### 12.4 发布验收
1. macOS 安装包双击安装可启动。
2. 未出现“来源不明应用阻断”主路径失败（签名+公证达标）。
3. 首次启动不触发非必要网络请求。

## 13. 里程碑
1. M1（1-2 周）：项目骨架、Provider 管理、DoH/DoT 查询 MVP。
2. M2（1-2 周）：IP 库适配层（QQWry/ZX/ip2region）与结果聚合。
3. M3（1 周）：下载助手、白名单网络边界、校验回滚。
4. M4（1 周）：历史导出、日志诊断、E2E 与打包签名公证流程。

## 14. 风险与对策
1. 数据库许可/合规不清晰。  
对策：在产品内加入来源与许可声明，默认要求用户确认后下载。
2. 多数据源格式变更导致更新失败。  
对策：下载器插件化 + 校验与回滚机制。
3. 不同 DoH 服务商兼容性差异。  
对策：默认 RFC 线格式，必要时支持 per-provider header 覆写。
4. macOS 分发被 Gatekeeper 拦截。  
对策：标准化签名+公证流水线，发布前做洁净机验证。

## 15. 假设与默认值（已锁定）
1. 应用仅支持 macOS 本地运行，不上架 App Store。
2. IP 库更新采用“内置下载助手”。
3. DoH 默认 `application/dns-message`。
4. 查询阶段仅 DNS 出网；更新阶段仅白名单域名出网。
5. 目标安装体验为“用户拿到安装包双击即可使用”，故采用签名+公证。

## 16. 参考资料（Context7 + Web）
1. Wails 文档与项目（工程结构、绑定、构建签名）  
[https://wails.io/docs/](https://wails.io/docs/)  
[https://github.com/wailsapp/wails](https://github.com/wailsapp/wails)
2. DoH 标准 RFC 8484  
[https://www.rfc-editor.org/rfc/rfc8484](https://www.rfc-editor.org/rfc/rfc8484)
3. DoT 标准 RFC 7858  
[https://www.rfc-editor.org/rfc/rfc7858](https://www.rfc-editor.org/rfc/rfc7858)
4. Cloudflare DNS over HTTPS/TLS  
[https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-https/](https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-https/)  
[https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-tls/](https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-tls/)
5. Google Public DNS（DoH）  
[https://developers.google.com/speed/public-dns/docs/doh](https://developers.google.com/speed/public-dns/docs/doh)
6. Quad9 DoH/DoT 端点说明  
[https://docs.quad9.net/](https://docs.quad9.net/)
7. ip2region 项目（xdb 结构与使用）  
[https://github.com/lionsoul2014/ip2region](https://github.com/lionsoul2014/ip2region)
8. QQWry/ZX 下载实践参考（用于下载器兼容设计时的来源差异评估）  
[https://github.com/xykt/qqwry](https://github.com/xykt/qqwry)
9. Apple Developer Notarization（非 App Store 分发推荐实践）  
[https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)
