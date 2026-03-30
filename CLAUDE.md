# GeoPrism 项目开发指南

## 项目概述

GeoPrism 是一个 macOS 本地运行的 DNS / IP 查询 CLI 工具，使用纯 Go 开发。

## 技术栈

- **语言**: Go 1.23（`go.mod`）
- **CLI**: 标准库 `os.Args` + `flag.NewFlagSet`，无第三方框架
- **Provider 配置**: `github.com/BurntSushi/toml`
- **离线 IP 存储**: `github.com/cockroachdb/pebble/v2`
- **终端渲染**: `github.com/charmbracelet/lipgloss` + `github.com/mattn/go-isatty`

## 项目结构

```
GeoPrism/
├── main.go              # CLI 薄入口，仅负责调用 internal/cli
├── internal/
│   └── cli/             # CLI 路由、App 实现、路径管理、IP/CIDR 查询与测试
├── render/              # CLI 表格渲染与 TTY 样式增强
│   ├── output.go        # 输出模式（Text/JSON）与统一 JSON 输出
│   ├── cidrlookup.go    # CIDR 查询结果渲染
│   ├── iplookup.go      # 单个 IP 查询结果渲染
│   ├── ns_info.go       # NS 服务器信息渲染
│   └── match_state.go   # HIT/MISS 状态文案复用
├── go.mod               # Go 依赖
├── backend/             # Go 后端模块
│   ├── resolver/        # DoH/DoT/DNS 查询与归一化
│   ├── provider/        # Provider TOML 配置管理
│   ├── ipdb/            # 离线 IP 库构建、编码与查询
│   ├── ipinfo/          # ipinfo Lite API 客户端
│   └── settings/        # 应用配置（ipinfo token、数据源优先级）
```

说明：
- `M3` / `M4` 对应的 `backend/updater`、`backend/storage` 当前尚未创建，文档只保留里程碑说明，不再把它们写成现有目录。

## 开发命令

```bash
# 编译
go build -o geoprism .

# 运行（开发）
go run . example.com
# 需先构建离线 IP 库
go run . 1.1.1.1
go run . 1.0.0.0/24

# 构建离线 IP 库
go run . ipdb build --csv /absolute/path/ipinfo_lite.csv

# 清理构建产物
rm geoprism
```

## 数据目录

```text
~/.geoprism/
├── config/              # providers.toml, settings.toml
└── ipdb/                # Pebble 离线 IP 库
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

## 开发规范

### Go
- 使用中文注释
- 模块化设计，按职责分离
- Provider 配置使用结构体管理

## 当前里程碑状态（代码现状）

- M1 已完成：域名查询、多 Provider 并行、DoH/DoT/DNS、Provider 配置加载/列举/连通性测试、NS 服务器信息查询与默认模板
- M2 已完成：离线 IP CSV 导入、Pebble 构建、DNS 结果本地 IP 匹配、单 IP / CIDR 查询输出、ipinfo 在线查询与回写、数据源优先级配置
- M1 待补：Provider 导入/导出
- M3 待实现：IP 库下载与更新
- M4 待实现：历史、导出、日志诊断

## 当前 CLI 行为

- `geoprism query ...` 在 TTY 下使用增强表格渲染；非 TTY 保持原有纯文本表格协议
- `geoprism <ip>` 会优先使用本地离线 IP 库；若本地无离线库但已配置 `ipinfo_token`，会回退到 ipinfo 在线查询；两者都不可用时才报错退出
- `geoprism <cidr>` 会直接查询 ipdb 并返回所有与该 CIDR 相交的离线网段记录；该主路径忽略 `data_source_priority`，仅在 ipdb 完全未命中时，才会以 CIDR 基地址回退到当前单 IP 查询逻辑
- `geoprism providers` 列出当前 TOML 配置中的 Provider
- `geoprism test --all|<name>` 测试 Provider 连通性
- `geoprism providers`、`geoprism test`、`geoprism <ip>`、`geoprism <cidr>` 在 TTY 下同样使用增强表格渲染；非 TTY 保持纯文本输出
- 若本地存在可用离线库，会在 DNS 结果后追加 `IP 匹配详情`；TTY 与非 TTY 都保持表格语义
- 若配置了 ipinfo token，ipdb 未命中的 IP 会自动调用 ipinfo API 查询，并将结果异步回写 ipdb
- `data_source_priority` 支持 `ipdb-first`（默认）和 `ipinfo-first` 两种模式
- IP 匹配详情和 IP 查询结果新增 `Source` 列，标识数据来源（`ipdb` / `ipinfo`）
- 同一 IP 在多个 Provider 结果中出现时，ipinfo API 只调用一次（去重）
- 若本地不存在离线库，仅打印告警并继续 DNS 查询，不中断主流程
- `geoprism query ...` 或 `geoprism <domain>` 会在主查询后追加 `NS 服务器信息`
- `geoprism <ip> -j` 输出单个 IP 结果对象，包含 `source` 字段标识数据来源，不复用 `query` 的 `domain/answers` 结构
- `geoprism <cidr> -j` 输出独立的 CIDR 结果对象，包含 `query_cidr`、`match_count`、`matches` 以及可选的 `fallback`
- `geoprism -x <ip>` 或 `geoprism query <ip> -x` 执行反向 PTR 查询（IP → 域名），支持 IPv4 和 IPv6

### JSON 输出

支持 `-j` / `--json` 参数，供 AI agent 或脚本消费：

- 支持的命令：`query`、`providers`、`test`
- 不支持的命令：`ipdb`（使用 `-j` 时会输出警告并忽略）
- 前导和后置参数都支持：`geoprism -j providers` 或 `geoprism providers -j`
- 快捷查询也支持：`geoprism example.com -j`、`geoprism 1.1.1.1 -j`、`geoprism 1.0.0.0/24 -j`
- `query` 会输出 `ns_info` 字段，包含 `query_domain`、`resolved_zone`、可用性、查询耗时、NS 服务器列表或错误信息
- CIDR 快捷查询返回独立 JSON 结构，不复用单 IP 查询的 `ip/matched/network` 结构
- 错误信息在 JSON 模式下输出到 stderr，格式为 `{"error":"..."}`
