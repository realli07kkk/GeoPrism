---
doc_type: architecture
slug: backend-ipdb
scope: 基于 Pebble 的离线 IP 地理位置库——构建、编码、查询（单 IP 与 CIDR）
summary: backend/ipdb 用 Pebble KV 存储 IP 网段到地理位置的映射，支持高效的单 IP 查询和 CIDR 范围相交查询
status: current
last_reviewed: 2026-04-24
tags: [backend, ipdb, pebble, ip, geolocation]
depends_on: []
implements: [ip-geolocation]
---

## 1. 定位与受众

本 doc 描述离线 IP 地理位置库的存储层——Pebble 编码方案、单 IP 查询的长前缀匹配算法、CIDR 相交查询算法。读者是需要调试 IP 查询结果或修改编码方案的人。

读完能：理解 IP 地址如何编码为 Pebble key、查询如何定位到覆盖给定 IP 的最长前缀网段、CIDR 查询如何找到所有相交记录。

## 2. 结构与交互

### 文件职责

| 文件 | 职责 |
|---|---|
| `types.go` | 数据类型定义（Record、Match、Metadata、BuildOptions）|
| `codec.go` | IP 地址 ↔ Pebble key 的编解码 |
| `store.go` | Store 生命周期 + LookupIP + LookupCIDR |
| `builder.go` | 离线库构建流程（CSV 解析 → 编码 → Pebble 写入）|
| `logger.go` | Pebble 日志静默适配器 |

### 存储结构

```
~/.geoprism/ipdb/
├── CURRENT              # 纯文本，记录当前启用的 buildID
└── versions/
    └── <buildID>/
        └── db/          # Pebble 数据目录
```

`CURRENT` 文件内容为 buildID 字符串。`OpenCurrent` (`store.go:28`) 读取 CURRENT → 拼接 `versions/<buildID>/db/` → `pebble.Open`。

### Key 编码方案

四级 key 前缀（`types.go:5-9`）：

- `0x00`：元数据 key（`keyFamilyMeta`），仅 `meta` 一条
- `0x04`：IPv4 数据 key（`keyFamilyIPv4`），后接 4 字节网络字节序地址
- `0x06`：IPv6 数据 key（`keyFamilyIPv6`），后接 16 字节网络字节序地址

每个网段用其**起始地址**编码为 key，value 包含网段 prefix length + 所有地理位置字段。这是典型的区间编码：key = startIP，value 自带 mask length 表示覆盖范围。

### 单 IP 查询 (LookupIP)

`store.go:132`。两步算法：

1. **SeekGE + Prev**：用目标 IP 编码后的 key 做 SeekGE。如果找到的 key > 目标 key，退一步 Prev()。这一步拿到 <= 目标 IP 的最大起始地址记录
2. **Contains 校验**：解码 value 得到网段，用 `prefix.Contains(addr)` 验证目标 IP 确实在此网段内

这等价于经典的最长前缀匹配（LPM），但利用了 Pebble 的有序性：存储按起始地址排序，SeekGE 找到第一个 >= 目标的记录，Prev 回到最后一个 <= 目标的记录。

### CIDR 查询 (LookupCIDR)

`store.go:198`。三步算法：

1. **找起始点**：对查询 CIDR 的起始地址做 SeekGE。如果 key > 起始地址，Prev 退一步（覆盖起始地址的大网段不会在 SeekGE 结果里）
2. **遍历**：从找到的 key 开始，Next 遍历直到 key 超过查询 CIDR 的末尾地址
3. **相交判断**：每条记录解码后用 `prefixesOverlap` (`store.go:304`) 判断两个网段是否相交

## 3. 数据与状态

- **Record** (`types.go:20`)：单条 IP 地理位置信息，Network 字段格式为 `a.b.c.d/n`
- **Match** (`types.go:32`)：单 IP 查询结果（IP + Matched + Record）
- **Metadata** (`types.go:39`)：构建元信息（FormatVersion、SourceCSV、BuiltAt、RowCount 等）
- **Store** (`store.go:19`)：持有 Pebble DB 实例 + metadata
- Store 的生命周期：`OpenCurrent` 打开 → 查询期间保持打开 → `Close` 释放

## 4. 关键决策

暂无已归档的 decision。

- **Pebble 而非 BoltDB**：Pebble 是 CockroachDB 的 LSM-tree 引擎，对范围扫描友好（CIDR 查询依赖 Next 遍历），且 LevelDB/RocksDB 兼容
- **自实现编码而非用 Pebble 的 prefix 功能**：通过自定义 key family byte + 地址字节编码，保持完全控制迭代边界
- **CURRENT 文件而非 Pebble manifest**：简单文本指针方案，避免耦合 Pebble 内部版本管理

## 5. 代码锚点

| 文件 | 关键符号 | 说明 |
|---|---|---|
| `backend/ipdb/types.go:20` | `Record` | IP 记录类型 |
| `backend/ipdb/types.go:32` | `Match` | 单 IP 匹配结果 |
| `backend/ipdb/types.go:39` | `Metadata` | 库元信息 |
| `backend/ipdb/store.go:19` | `Store` | 存储结构体 |
| `backend/ipdb/store.go:28` | `OpenCurrent` | 打开当前库 |
| `backend/ipdb/store.go:84` | `WriteRecord` | 记录写入（回写用）|
| `backend/ipdb/store.go:132` | `LookupIP` | 单 IP 查询 |
| `backend/ipdb/store.go:198` | `LookupCIDR` | CIDR 查询 |
| `backend/ipdb/codec.go` | — | 编解码实现 |
| `backend/ipdb/builder.go` | — | 离线库构建 |
| `backend/ipdb/logger.go` | — | Pebble 日志静默 |

## 6. 已知约束 / 边界情况

- 单 IP 查询依赖最长前缀匹配——若数据中存在两个网段覆盖同一 IP，返回哪个取决于 Pebble 内部排序，当前编码方案下结果确定（返回起始地址最大的覆盖网段，即最具体的网段）
- CIDR 查询仅在本地 ipdb 中匹配，不回退到在线查询——该回退在 `cli-entry` 层实现
- 不支持增量更新——构建是全量重建，WriteRecord 仅用于 ipinfo 单条回写
- Pebble 数据库不跨版本兼容——构建时的 Pebble 版本与运行时的版本差异可能导致打开失败

## 7. 相关文档

- `cli-entry.md` — 调用方
- `backend-ipinfo.md` — 在线数据源 + 回写
- `backend-settings.md` — 数据源优先级
- 需求：`ip-geolocation`
