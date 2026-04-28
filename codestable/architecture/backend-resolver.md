---
doc_type: architecture
slug: backend-resolver
scope: DNS 查询执行——DoH/DoT/DNS 三种协议的查询实现与多 Provider 并行
summary: backend/resolver 封装了三种 DNS 传输协议的统一查询接口，通过 goroutine 实现多 Provider 并行查询
status: current
last_reviewed: 2026-04-24
tags: [backend, dns, doh, dot, resolver]
depends_on: []
implements: [domain-query, ptr-reverse-lookup, provider-config]
---

## 1. 定位与受众

本 doc 描述 DNS 查询执行层——如何对单个 Provider 发起 DoH/DoT/DNS 查询、如何并行查询多个 Provider 并收集结果。读者是实现新协议支持或排查 DNS 查询问题的人。

读完能：知道三种协议的查询入口在哪，理解并行查询的 goroutine + channel 模型，知道如何新增协议支持。

## 2. 结构与交互

### 核心类型

`resolver.go` 文件自包含，无额外子文件。核心类型：

- **Resolver** (`resolver.go:77`)：持有复用的 `*http.Client`（连接池），无状态。通过 `NewResolver()` 创建
- **ProviderInfo** (`resolver.go:17`)：Provider 连接参数（ID、Endpoint、ServerName、Port、Protocol、Name）
- **DNSQuery** (`resolver.go:41`)：查询请求（Domain、RecordType、Timeout）
- **DNSAnswer** (`resolver.go:49`)：单个 Provider 的查询结果（RCode、Answers、TTL、RTTMs）
- **QueryResult** (`resolver.go:69`)：多 Provider 查询的聚合结果

### 查询接口

`Query` (`resolver.go:356`) 是统一入口，按 protocol 字段派发：

```
Query(endpoint, serverName, port, protocol, DNSQuery)
  ├── "doh" → QueryDoH(endpoint, domain, rt, timeout)
  ├── "dot" → QueryDoT(serverName, domain, port, rt, timeout)
  └── "dns" → QueryDNS(serverName, port, domain, rt, timeout)
```

默认回退到 DoH。

### 三种协议实现

- **DoH** (`resolver.go:162`)：HTTP GET + `application/dns-message` Accept header。DNS 消息用 Base64URL 编码为 query string 参数 `?dns=...`
- **DoT** (`resolver.go:235`)：TLS 直连 + DNS over TLS 长度前缀帧格式（2 字节长度 + 消息体）
- **DNS** (`resolver.go:312`)：原生 UDP，使用 `github.com/miekg/dns` 的 `dns.Client`

### 并行查询

`QueryMulti` (`resolver.go:376`) 对每个 Provider 启动一个 goroutine，结果通过 buffered channel 收集。函数等待所有 goroutine 返回后才返回聚合结果——非流式，是同步等待所有完成的模式。每个 goroutine 的 error 不中断其他查询，错误被包装为 `DNSAnswer{Success: false}`。

### 连通性测试

`TestConnection` (`resolver.go:430`) 用 `google.com` 的 A 记录做 probe，返回 (success, message, latencyMs)。三种协议的 dispatch 逻辑与 `Query` 一致。

## 3. 数据与状态

- **DNSRecord** (`resolver.go:61`)：单条 DNS 记录（Type、Name、TTL、Data）。Data 字段保留原始格式（如 `example.com 300 IN A 104.18.26.120`）
- **RecordType** (`resolver.go:27`)：string 类型别名，支持 A/AAAA/CNAME/TXT/NS/MX/SOA/PTR，通过 `getRecordTypeCode` 映射到 `dns.Type*` 常量
- Resolver 本身无状态——`httpClient` 是唯一持有资源，连接池参数为 MaxIdleConns=10、IdleConnTimeout=30s

## 4. 关键决策

暂无已归档的 decision。代码中可观察的选择：

- **默认 DoH**：`Query` 和 `TestConnection` 对未知协议默认回退 DoH——考虑到 DoH 是当前最通用的加密 DNS 传输方式
- **并行模型选 buffered channel 而非 errgroup**：保持零第三方依赖，场景简单不需要取消传播
- **DoT 不使用 miekg/dns 内置支持**：手动实现 TLS 连接 + 长度前缀帧，`miekg/dns` 仅用于 DNS（UDP）场景的消息构造/解析

## 5. 代码锚点

| 文件 | 关键符号 | 说明 |
|---|---|---|
| `backend/resolver/resolver.go:17` | `ProviderInfo` | Provider 连接参数 |
| `backend/resolver/resolver.go:27` | `RecordType` | 记录类型别名 |
| `backend/resolver/resolver.go:41` | `DNSQuery` | 查询请求 |
| `backend/resolver/resolver.go:49` | `DNSAnswer` | 查询响应 |
| `backend/resolver/resolver.go:69` | `QueryResult` | 多 Provider 聚合结果 |
| `backend/resolver/resolver.go:77` | `Resolver` | 解析器结构体 |
| `backend/resolver/resolver.go:82` | `NewResolver` | 构造函数 |
| `backend/resolver/resolver.go:162` | `QueryDoH` | DoH 查询 |
| `backend/resolver/resolver.go:235` | `QueryDoT` | DoT 查询 |
| `backend/resolver/resolver.go:312` | `QueryDNS` | 原生 DNS 查询 |
| `backend/resolver/resolver.go:356` | `Query` | 统一查询入口 |
| `backend/resolver/resolver.go:376` | `QueryMulti` | 多 Provider 并行查询 |
| `backend/resolver/resolver.go:430` | `TestConnection` | 连通性测试 |

## 6. 已知约束 / 边界情况

- DoT 使用 `tls.Dial` 直连，不支持通过 HTTP 代理——与 DoH 不同（DoH 走 HTTP 客户端可配代理）
- 不缓存 DNS 结果——每次调用都是全新查询
- `QueryMulti` 不设全局超时（各 Provider 独立 timeout），极端情况下会等最慢的 Provider
- 只解析 Answer section，不处理 Authority 和 Additional section

## 7. 相关文档

- `cli-entry.md` — 调用方
- `backend-provider.md` — Provider 配置来源
- 需求：`domain-query`、`ptr-reverse-lookup`、`provider-config`
