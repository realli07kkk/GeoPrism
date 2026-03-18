# GeoPrism

GeoPrism 是一个 macOS 本地运行的 DNS 查询 CLI 工具，使用纯 Go 开发。

## 当前实现状态

- 已完成（M1）：域名查询（A/AAAA/CNAME/TXT/NS/MX/SOA）
- 已完成（M1）：多 Provider 并行查询
- 已完成（M1）：DoH / DoT / DNS(UDP) 支持
- 已完成（M1）：Provider 管理（CRUD + 默认模板）
- 已完成（M2）：离线 IP 库导入与解析匹配
- 待实现：Provider 导入/导出
- 待实现：IP 库下载更新（M3）
- 待实现：历史与导出、日志诊断（M4）

## 技术栈

- Go 1.23
- 标准库 CLI（`os.Args` + `flag.NewFlagSet`）
- `github.com/cockroachdb/pebble/v2`（离线 IP 库）

## 用法

```bash
# 快捷查询
geoprism example.com

# 指定记录类型
geoprism query example.com -t AAAA

# 指定 Provider
geoprism query example.com -p cloudflare,google

# 导入离线 IP 库
geoprism ipdb build --csv /absolute/path/ipinfo_lite.csv

# 导入离线库后，query 会追加 IP 匹配详情
geoprism query example.com

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

# 清理构建产物
rm geoprism
```

## 目录说明

```text
backend/
  provider/   # Provider 配置管理
  resolver/   # DoH/DoT/DNS 查询
  ipdb/       # 离线 IP 库导入与查询
  updater/    # 预留（M3）
  storage/    # 预留（M4）
```

## 数据目录

```text
~/.geoprism/
  config/     # providers.json
  ipdb/       # Pebble 离线 IP 库
```

兼容性说明：
- 若新路径下还没有 `providers.json`，程序会在启动时从旧路径 `~/Library/Application Support/GeoPrism/config/providers.json` 复制已有配置。
