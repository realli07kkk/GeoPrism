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
│   └── cli/             # CLI 路由、App 实现、路径管理、IP 查询与测试
├── render/              # CLI 表格渲染与 TTY 样式增强
│   ├── output.go        # 输出模式（Text/JSON）与统一 JSON 输出
│   ├── iplookup.go      # 单个 IP 查询结果渲染
│   ├── ns_info.go       # NS 服务器信息渲染
│   └── match_state.go   # HIT/MISS 状态文案复用
├── go.mod               # Go 依赖
├── backend/             # Go 后端模块
│   ├── resolver/        # DoH/DoT/DNS 查询与归一化
│   ├── provider/        # Provider TOML 配置管理
│   ├── ipdb/            # 离线 IP 库构建、编码与查询
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

# 构建离线 IP 库
go run . ipdb build --csv /absolute/path/ipinfo_lite.csv

# 清理构建产物
rm geoprism
```

## 数据目录

```text
~/.geoprism/
├── config/              # providers.toml
└── ipdb/                # Pebble 离线 IP 库
```

Provider 配置说明：
- 程序默认从 `~/.geoprism/config/providers.toml` 读取 Provider 配置。
- 若文件不存在，程序会在首次启动时自动写入默认模板。
- 配置文件使用 `[[providers]]` 数组表格式，每个 Provider 必须显式声明 `id`。
- 旧的 `providers.json` 不再读取，也不会自动迁移；若检测到旧文件，程序会输出手动迁移警告。

## 开发规范

### Go
- 使用中文注释
- 模块化设计，按职责分离
- Provider 配置使用结构体管理

## 当前里程碑状态（代码现状）

- M1 已完成：域名查询、多 Provider 并行、DoH/DoT/DNS、Provider 配置加载/列举/连通性测试、NS 服务器信息查询与默认模板
- M2 已完成：离线 IP CSV 导入、Pebble 构建、DNS 结果本地 IP 匹配与单 IP 查询输出
- M1 待补：Provider 导入/导出
- M3 待实现：IP 库下载与更新
- M4 待实现：历史、导出、日志诊断

## 当前 CLI 行为

- `geoprism query ...` 在 TTY 下使用增强表格渲染；非 TTY 保持原有纯文本表格协议
- `geoprism <ip>` 会直接查询本地离线 IP 库；若离线库不存在则报错退出
- `geoprism providers` 列出当前 TOML 配置中的 Provider
- `geoprism test --all|<name>` 测试 Provider 连通性
- `geoprism providers` 与 `geoprism test` 在 TTY 下同样使用增强表格渲染；非 TTY 保持纯文本输出
- 若本地存在可用离线库，会在 DNS 结果后追加 `IP 匹配详情`；TTY 与非 TTY 都保持表格语义
- 若本地不存在离线库，仅打印告警并继续 DNS 查询，不中断主流程
- `geoprism query ...` 或 `geoprism <domain>` 会在主查询后追加 `NS 服务器信息`
- `geoprism <ip> -j` 输出单个 IP 结果对象，不复用 `query` 的 `domain/answers` 结构
- `geoprism -x <ip>` 或 `geoprism query <ip> -x` 执行反向 PTR 查询（IP → 域名），支持 IPv4 和 IPv6

### JSON 输出

支持 `-j` / `--json` 参数，供 AI agent 或脚本消费：

- 支持的命令：`query`、`providers`、`test`
- 不支持的命令：`ipdb`（使用 `-j` 时会输出警告并忽略）
- 前导和后置参数都支持：`geoprism -j providers` 或 `geoprism providers -j`
- 快捷查询也支持：`geoprism example.com -j`、`geoprism 1.1.1.1 -j`
- `query` 会输出 `ns_info` 字段，包含可用性、查询耗时、NS 服务器列表或错误信息
- 错误信息在 JSON 模式下输出到 stderr，格式为 `{"error":"..."}`
