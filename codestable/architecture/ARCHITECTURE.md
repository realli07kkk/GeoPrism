# GeoPrism 架构总入口

> 状态：已填充
> 最后更新：2026-04-24

## 1. 项目简介

GeoPrism 是一个 macOS 本地运行的 DNS / IP 查询 CLI 工具，使用纯 Go 开发。

## 2. 核心概念 / 术语表

- **Provider**: DNS-over-HTTPS (DoH) / DNS-over-TLS (DoT) / 传统 DNS 服务器
- **IPDB / Pebble**: 基于 Pebble 的离线 IP 地理位置库
- **ipinfo**: 在线 IP 查询 API 服务
- **CIDR**: 无类别域间路由表示法（如 `1.0.0.0/24`）

## 3. 子系统 / 模块索引

### CLI 层

- [cli-entry](cli-entry.md) — CLI 路由入口、App 生命周期、子命令分发、IP/CIDR/域名智能识别

### 后端模块

- [backend-resolver](backend-resolver.md) — DoH/DoT/DNS 三种协议的 DNS 查询实现、多 Provider 并行
- [backend-provider](backend-provider.md) — Provider TOML 配置的加载、校验、增删改查
- [backend-ipdb](backend-ipdb.md) — 基于 Pebble 的离线 IP 库：构建、key 编码、单 IP 与 CIDR 查询
- [backend-ipinfo](backend-ipinfo.md) — ipinfo Lite API HTTP 客户端
- [backend-settings](backend-settings.md) — 应用级配置（ipinfo token + 数据源优先级）

### 渲染层

- [render-output](render-output.md) — TTY 检测、lipgloss 彩色表格、JSON 输出、各查询类型渲染器

## 4. 关键架构决定

暂无已归档的 decision 文档。代码中可观察的隐含决策见各子系统文档的第 4 节。

## 5. 已知约束 / 硬边界

- 零 CLI 框架依赖——仅使用标准库 `os.Args` + `flag.NewFlagSet`
- Go 1.23 + 纯标准库为主，外部依赖最小化
- macOS 本地运行，无 server 模式
- 离线 IP 库默认路径 `~/.geoprism/ipdb/`，配置文件 `~/.geoprism/config/`
