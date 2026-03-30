# GeoPrism

GeoPrism 是一个 macOS 本地运行的 DNS / IP 查询 CLI 工具，使用纯 Go 开发。

## 当前实现状态

- 已完成（M1）：域名查询（A/AAAA/CNAME/TXT/NS/MX/SOA/PTR）
- 已完成（M1）：多 Provider 并行查询
- 已完成（M1）：DoH / DoT / DNS(UDP) 支持
- 已完成（M1）：Provider 配置加载、列举、连通性测试与默认模板
- 已完成（M1）：NS 服务器信息查询
- 已完成（M2）：离线 IP 库导入、解析匹配与单 IP 查询、ipinfo 在线查询与回写、数据源优先级配置
- 待实现：Provider 导入/导出
- 待实现：IP 库下载更新（M3）
- 待实现：历史与导出、日志诊断（M4）

## 技术栈

- Go 1.23
- 标准库 CLI（`os.Args` + `flag.NewFlagSet`）
- `github.com/BurntSushi/toml`（Provider TOML 配置）
- `github.com/cockroachdb/pebble/v2`（离线 IP 库）
- `github.com/charmbracelet/lipgloss` + `github.com/mattn/go-isatty`（TTY 表格渲染）

## 用法

```bash
# 快捷查询
geoprism example.com
geoprism 1.1.1.1

# 指定记录类型
geoprism query example.com -t AAAA

# 反向查询（IP → 域名）
geoprism -x 8.8.8.8
geoprism query 8.8.8.8 -x

# 指定 Provider
geoprism query example.com -p cloudflare,google

# JSON 格式输出（供 AI agent 或脚本使用）
geoprism -j providers
geoprism query example.com -j
geoprism example.com --json
geoprism 1.1.1.1 -j

# 导入离线 IP 库
geoprism ipdb build --csv /absolute/path/ipinfo_lite.csv

# 导入离线库后，query 会追加 IP 匹配详情
geoprism query example.com

# 导入离线库后，可直接查询单个 IP
geoprism 1.1.1.1

# 列出所有 Provider
geoprism providers

# 测试 Provider 连通性
geoprism test --all
geoprism test cloudflare
```

## 开发命令

```bash
# 编译
go build -o geoprism .

# 运行（开发）
go run . example.com
# 需先构建离线 IP 库
go run . 1.1.1.1

# 清理构建产物
rm geoprism
```

## 目录说明

```text
main.go      # CLI 薄入口，仅负责调用 internal/cli
internal/
  cli/       # CLI 路由、App 实现、路径管理、IP 查询与测试
render/      # CLI 表格渲染与 TTY 样式增强
backend/
  provider/   # Provider 配置管理
  resolver/   # DoH/DoT/DNS 查询
  ipdb/       # 离线 IP 库导入与查询
  ipinfo/     # ipinfo Lite API 客户端
  settings/   # 应用配置（ipinfo token、数据源优先级）
```

说明：
- `backend/updater` 和 `backend/storage` 目前仍属于 M3 / M4 规划，仓库里尚未创建对应目录。

## 输出行为

- `query`、`providers`、`test` 以及单个 IP 查询在 TTY 下使用增强表格渲染
- 非 TTY 输出保持纯文本表格协议，便于重定向和脚本处理
- `IP 匹配详情` 在 TTY 与非 TTY 下都保持表格语义，只在样式上有差异
- 若配置了 ipinfo token，ipdb 未命中的 IP 会自动调用 ipinfo API 查询并异步回写；新增 `Source` 列标识数据来源
- `data_source_priority` 支持 `ipdb-first`（默认）和 `ipinfo-first` 两种模式
- 同一 IP 在多个 Provider 结果中出现时，ipinfo API 只调用一次
- `query` 会追加 `NS 服务器信息`；若目标域名本身不是 zone apex，会自动向上探测真实 zone，并显示 `实际命中 Zone`
- `geoprism <ip>` 会优先使用本地离线 IP 库；若本地无离线库但已配置 `ipinfo_token`，会回退到 ipinfo 在线查询；两者都不可用时，才会报错并提示先执行 `geoprism ipdb build --csv /absolute/path/ipinfo_lite.csv`
- `geoprism <ip> -j` 输出单个 IP 结果对象，包含 `source` 字段标识数据来源，不复用 `query` 的 `domain/answers` 结构

### JSON 输出模式

使用 `-j` / `--json` 参数输出 JSON 格式：

- 支持：`query`、`providers`、`test`（前导或后置参数均可）
- 快捷查询同样支持：`geoprism example.com -j`、`geoprism 1.1.1.1 -j`
- 不支持：`ipdb`（会输出警告并忽略）
- `query` 会包含 `ns_info` 字段，包含 `query_domain`、`resolved_zone`、可用性、查询耗时、NS 服务器列表或错误信息
- 错误信息输出到 stderr，格式为 `{"error":"..."}`

## 数据目录

```text
~/.geoprism/
  config/     # providers.toml, settings.toml
  ipdb/       # Pebble 离线 IP 库
```

Provider 配置说明：
- 程序默认从 `~/.geoprism/config/providers.toml` 读取 Provider 配置。
- 若文件不存在，程序会在首次启动时自动写入默认模板。
- 配置文件使用 `[[providers]]` 数组表格式，每个 Provider 必须显式声明 `id`。
- 旧的 `providers.json` 不再读取，也不会自动迁移；若首次启动时 `providers.toml` 不存在且检测到旧文件，程序会输出手动迁移警告。

应用配置说明：
- 程序从 `~/.geoprism/config/settings.toml` 读取应用级配置。
- 若文件不存在，程序会在首次启动时自动写入默认模板。
- 配置项位于 `[settings]` 表下：`ipinfo_token`（ipinfo API token）、`data_source_priority`（`ipdb-first` 或 `ipinfo-first`）。
- `ipinfo_token` 为空时，不启用 ipinfo 在线查询功能。
