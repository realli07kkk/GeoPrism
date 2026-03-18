# GeoPrism 项目开发指南

## 项目概述

GeoPrism 是一个 macOS 本地运行的 DNS 查询 CLI 工具，使用纯 Go 开发。

## 技术栈

- **语言**: Go 1.23（`go.mod`）
- **CLI**: 标准库 `os.Args` + `flag.NewFlagSet`，无第三方框架
- **Provider 配置**: `github.com/BurntSushi/toml`
- **离线 IP 存储**: `github.com/cockroachdb/pebble/v2`

## 项目结构

```
GeoPrism/
├── main.go              # CLI 入口与子命令分发
├── app.go               # App 核心逻辑与 CLI 子命令实现
├── paths.go             # ~/.geoprism 路径管理
├── ipdb_cmd.go          # ipdb build 子命令
├── ip_match.go          # DNS 结果中的 IP 匹配输出
├── go.mod               # Go 依赖
├── backend/             # Go 后端模块
│   ├── resolver/        # DoH/DoT/DNS 查询与归一化
│   ├── provider/        # Provider TOML 配置管理
│   ├── ipdb/            # 离线 IP 库构建、编码与查询
│   ├── updater/         # 预留目录（M3）
│   └── storage/         # 预留目录（M4）
```

## 开发命令

```bash
# 编译
go build -o geoprism .

# 运行（开发）
go run . example.com

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

- M1 已完成：域名查询、多 Provider 并行、DoH/DoT/DNS、Provider 配置加载/列举/连通性测试与默认模板
- M2 已完成：离线 IP CSV 导入、Pebble 构建、本地 IP 匹配与查询结果输出
- M1 待补：Provider 导入/导出
- M3 待实现：IP 库下载与更新
- M4 待实现：历史、导出、日志诊断

## 当前 CLI 行为

- `geoprism query ...` 保留原有 DNS 结果表格
- `geoprism providers` 列出当前 TOML 配置中的 Provider
- `geoprism test --all|<name>` 测试 Provider 连通性
- 若本地存在可用离线库，会在 DNS 结果后追加 `IP 匹配详情`
- 若本地不存在离线库，仅打印告警并继续 DNS 查询，不中断主流程
