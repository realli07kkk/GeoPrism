---
doc_type: architecture
slug: backend-settings
scope: 应用级配置的 TOML 加载——ipinfo token 与数据源优先级
summary: backend/settings 管理应用配置的单例加载，提供 ipinfo token 和数据源优先级两个配置项的读取
status: current
last_reviewed: 2026-04-24
tags: [backend, settings, config]
depends_on: []
implements: [ip-geolocation]
---

## 1. 定位与受众

本 doc 描述应用级配置层——settings.toml 的格式、加载流程、读取接口。读者是需要新增应用配置项的人。

读完能：知道配置文件位置、如何在 Settings 结构体中新增字段、如何暴露读取方法。

## 2. 结构与交互

### 核心类型

`settings.go` 是唯一源文件。

- **Settings** (`settings.go:21`)：配置字段——`IPInfoToken` + `DataSourcePriority`
- **SettingsStore** (`settings.go:27`)：不可变单例，构造时加载，无写路径
- **DataSourcePriority** (`settings.go:13`)：string 类型别名，合法值 `ipdb-first` / `ipinfo-first`

### 文件位置

配置文件固定为 `<configDir>/settings.toml`，格式：

```toml
[settings]
ipinfo_token = ""
data_source_priority = "ipdb-first"
```

### 加载流程

`NewSettingsStore` (`settings.go:35`)：

1. 文件不存在 → 写入嵌入的默认模板（`defaults/settings.toml`）
2. TOML 解码 → 严格模式（`Undecoded()` 非空报错）
3. `DataSourcePriority` 为空时默认 `ipdb-first`

### 读取接口

| 方法 | 说明 |
|---|---|
| `IPInfoToken()` | 返回 token，Store 为 nil 时返回 "" |
| `DataSourcePriority()` | 返回优先级，Store 为 nil 时返回 `ipdb-first` |
| `IsIPInfoEnabled()` | token 非空即为启用 |
| `IsIPInfoFirst()` | 优先级 == ipinfo-first |

所有方法对 nil receiver 安全。

## 3. 数据与状态

- SettingsStore 是不可变的——没有 Set 方法，修改配置需要重启
- 默认模板通过 `//go:embed` 嵌入

## 4. 关键决策

暂无已归档的 decision。

- **不可变 Store**：当前无运行时修改配置的需求，保持简单。未来若有需求（如 `geoprism config set`），需要加写锁和文件回写
- **DataSourcePriority 是全局设置**：影响所有 IP 查询，不支持 per-query 覆盖

## 5. 代码锚点

| 文件 | 关键符号 | 说明 |
|---|---|---|
| `backend/settings/settings.go:13` | `DataSourcePriority` | 优先级类型 |
| `backend/settings/settings.go:21` | `Settings` | 配置字段 |
| `backend/settings/settings.go:27` | `SettingsStore` | 存储结构体 |
| `backend/settings/settings.go:35` | `NewSettingsStore` | 构造函数 |
| `backend/settings/settings.go:69` | `IPInfoToken` | token 读取 |
| `backend/settings/settings.go:77` | `DataSourcePriority` | 优先级读取 |
| `backend/settings/settings.go:85` | `IsIPInfoEnabled` | 是否启用 ipinfo |
| `backend/settings/settings.go:90` | `IsIPInfoFirst` | 是否 ipinfo 优先 |

## 6. 已知约束 / 边界情况

- 配置项无运行时热更新——修改文件需重启
- token 为空时 ipinfo 客户端不创建——整条 ipinfo 链路不生效
- 加载失败不中断程序——`cli-entry` 的 `NewApp` 中 `settings.NewSettingsStore` 返回 error 只打印警告
- DataSourcePriority 仅对单 IP 查询生效，CIDR 查询不参考此配置

## 7. 相关文档

- `cli-entry.md` — 调用方
- `backend-ipinfo.md` — token 消费者
- `backend-ipdb.md` — 优先级影响 ipdb vs ipinfo 的查询顺序
- 需求：`ip-geolocation`
