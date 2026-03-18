# GeoPrism

GeoPrism 是一个 macOS 本地运行的 DNS 查询 CLI 工具，使用纯 Go 开发。

## 当前实现状态

- 已完成（M1）：域名查询（A/AAAA/CNAME/TXT/NS/MX/SOA）
- 已完成（M1）：多 Provider 并行查询
- 已完成（M1）：DoH / DoT / DNS(UDP) 支持
- 已完成（M1）：Provider 配置加载、列举、连通性测试与默认模板
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
  config/     # providers.toml
  ipdb/       # Pebble 离线 IP 库
```

Provider 配置说明：
- 程序默认从 `~/.geoprism/config/providers.toml` 读取 Provider 配置。
- 若文件不存在，程序会在首次启动时自动写入默认模板。
- 配置文件使用 `[[providers]]` 数组表格式，每个 Provider 必须显式声明 `id`。
- 旧的 `providers.json` 不再读取，也不会自动迁移；若检测到旧文件，程序会输出手动迁移警告。
