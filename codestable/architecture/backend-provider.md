---
doc_type: architecture
slug: backend-provider
scope: Provider 配置的 TOML 文件读写、校验、内存索引
summary: backend/provider 管理 DNS Provider 的全生命周期——从 TOML 文件加载、内存索引、增删改查到默认模板生成
status: current
last_reviewed: 2026-04-24
tags: [backend, provider, config, toml]
depends_on: []
implements: [provider-config]
---

## 1. 定位与受众

本 doc 描述 Provider 配置的持久化层——TOML 文件格式、加载/保存流程、内存索引、校验规则。读者是需要修改 Provider 配置逻辑或理解配置生命周期的人。

读完能：知道配置文件位置、Provider 结构体字段含义、如何安全并发地增删改查、校验规则的边界。

## 2. 结构与交互

### 核心类型

- **Provider** (`provider.go:31`)：TOML + JSON 双序列化的单条 Provider 配置。字段含 ID、Name、Protocol、Endpoint、ServerName、Port、Timeout、Enabled、Tags
- **ProviderStore** (`provider.go:44`)：线程安全的内存索引 + 文件持久化，用 `sync.RWMutex` 保护 `map[string]Provider`
- **Protocol** (`provider.go:17`)：string 类型别名，合法值 `doh`/`dot`/`dns`
- **ProviderView** (`provider.go:213`)：对外展示用的扁平结构体，通过 `ToView()` 从 Provider 转换

### 文件位置

配置文件固定为 `<configDir>/providers.toml`（`provider.go:25`）。configDir 由 `internal/cli/paths.go` 传入，默认为 `~/.geoprism/config/`。

### 加载流程

`NewProviderStore` (`provider.go:55`) 启动时：

1. `ensureConfigFile`：文件不存在则写入嵌入的默认模板（`defaults/providers.toml`），同时检测旧版 `providers.json` 并输出迁移警告
2. `load`：TOML 解码 → 逐条 `normalizeProvider`（去空白）→ `validateProvider`（字段合法性）→ 检查 ID 重复 → 写入 map

### 写路径

所有写操作（`Upsert`、`Delete`）先拿写锁修改内存 map，再 `save()` 全量写回 TOML。save 时按 ID 排序保证输出稳定。

### 读接口

| 方法 | 锁 | 说明 |
|---|---|---|
| `List()` | RLock | 返回所有 Provider 切片 |
| `Get(id)` | RLock | 按 ID 查单个 |
| `GetEnabled()` | RLock | 只返回 Enabled=true 的 |

## 3. 数据与状态

- ProviderStore 的 `providers` map 是唯一真实状态源。文件是持久化镜像。
- ID 为空时 Upsert 自动生成 UUID（`provider.go:178`）
- TOML 解析使用严格模式——`meta.Undecoded()` 非空时报错，防止拼写错误被静默忽略
- 默认模板通过 `//go:embed` 嵌入二进制，不依赖外部文件

## 4. 关键决策

暂无已归档的 decision。

- **全量写回而非增量 patch**：save 每次重写整个文件。Provider 数量通常很小（< 20），全量写回的简单性价值高于增量更新的性能
- **TOML 而非 JSON**：TOML 对人更友好（注释、更少引号），适合手动编辑。旧 JSON 格式只在首次启动时警告，不自动迁移

## 5. 代码锚点

| 文件 | 关键符号 | 说明 |
|---|---|---|
| `backend/provider/provider.go:17` | `Protocol` | 协议类型 |
| `backend/provider/provider.go:31` | `Provider` | Provider 配置结构体 |
| `backend/provider/provider.go:44` | `ProviderStore` | 存储管理器 |
| `backend/provider/provider.go:55` | `NewProviderStore` | 构造函数（含加载流程） |
| `backend/provider/provider.go:79` | `ensureConfigFile` | 默认模板生成 |
| `backend/provider/provider.go:97` | `load` | TOML 加载与校验 |
| `backend/provider/provider.go:155` | `List` | 全量列表 |
| `backend/provider/provider.go:166` | `Get` | 单条查询 |
| `backend/provider/provider.go:174` | `Upsert` | 创建或更新 |
| `backend/provider/provider.go:189` | `Delete` | 删除 |
| `backend/provider/provider.go:200` | `GetEnabled` | 获取已启用的 |
| `backend/provider/provider.go:213` | `ProviderView` | 展示视图 |
| `backend/provider/provider.go:241` | `normalizeProvider` | 空白规范化 |
| `backend/provider/provider.go:250` | `validateProvider` | 字段校验 |

## 6. 已知约束 / 边界情况

- 不支持热加载——修改 TOML 文件后需重启程序才能生效
- TOML 是唯一支持格式，不存在 JSON fallback
- Provider 数量无上限但全量写回意味着超大文件（> 1000 条）会有性能问题
- 不验证 Provider 的网络可达性——那是 `test` 子命令的职责

## 7. 相关文档

- `cli-entry.md` — 调用方
- `backend-resolver.md` — Provider 信息消费者
- 需求：`provider-config`
