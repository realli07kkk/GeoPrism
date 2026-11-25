# GeoPrism 项目文档

本目录是 GeoPrism 的项目知识库，面向开发者和 AI Agent。**改代码或设计方案前，先按下表找到对应文档对齐现状与边界。**

## 能力索引：什么任务读哪篇

| 你要做什么 | 先读 |
|---|---|
| 了解整体分层、模块边界、数据目录、硬边界 | [architecture.md](architecture.md) |
| 改 DNS 查询链路、Provider 配置与保序、NS 服务器探测、反向 PTR、连通性测试 | [dns-query.md](dns-query.md) |
| 改单 IP / CIDR 查询、ipinfo 在线合并、`data_source_priority`、`Source` 列 | [ip-lookup.md](ip-lookup.md) |
| 改离线 IP 库（ipdb）的构建、key/value 编码、LPM / CIDR 查询、overlay 缓存、文件锁 | [ipdb-v2.md](ipdb-v2.md) |
| 问"为什么这样设计"，或打算推翻某个已确认决策 | [decisions.md](decisions.md) |
| 本地编译、跑测试、提交前质量门、`netip` / 解码 / 文件系统陷阱、平台支持 | [development.md](development.md) |
| 排查历史 bug：回写破坏 LPM、JSON 结果乱序、并发构建悬空 `CURRENT` | [history.md](history.md) |

## 两条能力主线

- **多 Provider DNS 查询**：一条命令并行问多个公共 DNS（DoH / DoT / UDP），按 Provider 并排对比，自动追加 NS 服务器信息，支持反向 PTR。
- **离线 IP / CIDR 查询**：把 ipinfo Lite CSV 导入本地 Pebble 库，断网查单 IP（最长前缀匹配）或 CIDR（所有相交网段）；本地未命中时回退 ipinfo 在线查询并标注来源。

## 文档约定

- 代码锚点统一写"文件路径 + 函数名"，不写行号（行号会随代码漂移）。
- 已确认决策集中在 [decisions.md](decisions.md)，编号 `D-xx`；其它文档只引用编号。
- 未决事项在各文档末尾"未决事项"小节，标注状态与依赖。**不要把未决当现状。**
- 用户向的安装与用法见仓库根 [README.md](../README.md)；Agent 入口见根 [CLAUDE.md](../CLAUDE.md)（`AGENTS.md` 是指向它的软链接）。
- 文档内容与代码冲突时以代码为准，并回头修正文档。

## 目录

```text
doc/
├── README.md        本索引
├── architecture.md  架构总入口
├── dns-query.md     多 Provider DNS 查询子系统
├── ip-lookup.md     离线 IP / CIDR 查询子系统
├── ipdb-v2.md       IPDB v2 存储设计与协议
├── decisions.md     已确认决策及原因
├── development.md   开发、测试与质量门
└── history.md       历史问题与修复
```
