---
doc_type: architecture
slug: backend-ipinfo
scope: ipinfo Lite API HTTP 客户端——在线查询与响应模型转换
summary: backend/ipinfo 封装 ipinfo.io Lite API 的 HTTP 调用，将 JSON 响应转换为内部 Record 模型
status: current
last_reviewed: 2026-04-24
tags: [backend, ipinfo, api, online]
depends_on: []
implements: [ip-geolocation]
---

## 1. 定位与受众

本 doc 描述 ipinfo 在线查询客户端——API 调用方式、响应模型、到 ipdb Record 的转换。读者是需要调试在线查询失败问题或修改 ipinfo 集成的人。

读完能：知道 API endpoint、token 配置位置、响应到 Record 的转换规则。

## 2. 结构与交互

### 核心类型

`client.go` 是唯一源文件。

- **Client** (`client.go:54`)：持有 token 和 `*http.Client`（Timeout=10s）
- **Response** (`client.go:14`)：ipinfo Lite API JSON 响应的直接映射

### 查询流程

`LookupIP` (`client.go:70`)：

```
GET https://api.ipinfo.io/lite/{ip}?token={token}
  → HTTP 200 → JSON decode → Response
  → 非 200 → error
```

无重试、无缓存、无速率限制——纯透传。

### Response → Record 转换

`ToRecord` (`client.go:27`) 将 `Response` 转为 `ipdb.Record`。Network 字段按 IP 地址族自动设置：IPv4 → `/32`，IPv6 → `/128`。IPv6 判断用字符 `:` 检测（`client.go:44`）。

### 与 cli 层的协作

ipinfo 查询的实际调用入口在 `internal/cli/ip_merge.go`，不在本模块内。cli 层负责：
- 去重：同一 IP 多 Provider 命中时只调一次 ipinfo
- 回写：将 Response 通过 `ipdb.Store.WriteRecord` 写入离线库
- 优先级：按 `settings.DataSourcePriority` 决定 ipdb 和 ipinfo 的合并顺序

本模块只提供 `NewClient` + `LookupIP` 两个公共接口。

## 3. 数据与状态

- Client 无状态，仅持有 token 和 HTTP 连接
- Response 的字段与 ipdb.Record 基本一一对应（无 city/region/location 等 Lite API 不返回的字段）

## 4. 关键决策

暂无已归档的 decision。

## 5. 代码锚点

| 文件 | 关键符号 | 说明 |
|---|---|---|
| `backend/ipinfo/client.go:14` | `Response` | API 响应类型 |
| `backend/ipinfo/client.go:27` | `ToRecord` | 转换为 ipdb.Record |
| `backend/ipinfo/client.go:54` | `Client` | HTTP 客户端 |
| `backend/ipinfo/client.go:60` | `NewClient` | 构造函数 |
| `backend/ipinfo/client.go:70` | `LookupIP` | 查询单 IP |

## 6. 已知约束 / 边界情况

- token 为空时 Client 不创建——`cli-entry` 在 `NewApp` 时检查 `settings.IsIPInfoEnabled()`
- 无速率限制——高频调用可能在 ipinfo 侧触发限流
- 无重试——网络错误直接返回给调用方
- IPv6 检测用字符串扫描而非 `net.ParseIP`——对格式异常的 IP 可能误判

## 7. 相关文档

- `cli-entry.md` — 调用方（ip_merge.go 中的去重与回写逻辑）
- `backend-ipdb.md` — Record 目标模型与回写目标
- `backend-settings.md` — token 来源与优先级配置
- 需求：`ip-geolocation`
