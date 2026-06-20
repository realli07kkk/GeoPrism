---
doc_type: issue-report
issue: 2026-06-20-ipdb-writeback-breaks-lpm
status: confirmed
severity: P1
summary: ipinfo 在线回写把单 IP /32 写入 ipdb base keyspace，破坏最长前缀匹配，导致被大网段覆盖的 IP 反而返回 MISS，且同起始地址回写会永久覆盖原网段
tags: [ipdb, writeback, lookup, data-corruption]
---

# ipdb 在线回写破坏离线库查询正确性 Issue Report

## 1. 问题现象

配置了 `ipinfo_token` 后，ipinfo 在线查询结果会以单 IP（IPv4 /32、IPv6 /128）形式回写进离线库。由于回写记录和基础网段共用同一个 Pebble keyspace，回写后离线查询会出现两类错误：

- **现象 A（漏命中）**：某个原本被大网段覆盖的 IP，在其同网段内另一个 IP 触发过 /32 回写之后，离线查询返回 MISS（未命中），尽管它仍落在大网段范围内。
- **现象 B（永久覆盖）**：当回写的 /32 与已有网段起始地址相同（例如对网段基地址那一个 IP 触发回写），新写入的 /32 记录会直接覆盖原网段记录，原网段的覆盖范围被永久缩成 /32，且离线库重建前无法恢复。

两类现象都是静默发生，查询不会报错，只是给出错误的"未命中"或错误的归属。

## 2. 复现步骤

### 现象 A（漏命中）

1. 用 CSV 构建离线库，库中包含网段 `1.0.0.0/24`（且不含 `1.0.0.10` 的更具体网段）。
2. 在 `settings.toml` 配置 `ipinfo_token`。
3. 执行 `geoprism 1.0.0.10`。该单 IP 路径会调用 ipinfo；当 ipinfo 返回的数据与 `1.0.0.0/24` 记录不同时，触发回写，写入 `1.0.0.10/32`。
4. 在仅走离线库的条件下执行 `geoprism 1.0.0.11`（该 IP 同样落在 `1.0.0.0/24` 内）。
5. 观察到：`1.0.0.11` 返回 **MISS / 未命中**。

复现频率：稳定（满足"大网段存在 + 网段内某 IP 触发过差异回写 + 查询同网段内排在该 /32 之后的 IP"即必现）。

### 现象 B（永久覆盖）

1. 用 CSV 构建离线库，库中包含网段 `1.0.0.0/24`。
2. 配置 `ipinfo_token`。
3. 执行 `geoprism 1.0.0.0`（即网段基地址），ipinfo 返回数据与 `1.0.0.0/24` 不同，触发回写 `1.0.0.0/32`。
4. 此后执行 `geoprism 1.0.0.5`。
5. 观察到：`1.0.0.5` 返回 **MISS**，且 `1.0.0.0` 的归属也变成了 ipinfo 写入的那一条 /32，原 `/24` 记录已不可见。

复现频率：稳定。

## 3. 期望 vs 实际

**期望行为**：在线回写只应作为"基础库未覆盖时的补充缓存"，不应改变基础网段对其范围内任何 IP 的最长前缀匹配结果；对 `1.0.0.11` 这类仍落在 `1.0.0.0/24` 内的 IP，离线查询应继续命中 `1.0.0.0/24`。

**实际行为**：回写的 /32 与基础网段混在同一 keyspace，破坏了"前驱即唯一候选"的不变量；查询只回看一条最近前驱并判断是否包含，遇到不包含目标的 /32 前驱即返回 MISS，不会继续向更前的大网段回溯；同起始地址的 /32 还会直接覆盖大网段记录。

## 4. 环境信息

- 涉及模块 / 功能：`backend/ipdb`（离线 IP 库的写入与查询）、单 IP 查询路径与域名 IP 匹配回写路径
- 相关文件 / 函数（作为线索，根因待 analyze 阶段确认）：
  - `backend/ipdb/store.go` — `LookupIP`（前驱回看 + Contains 判定逻辑）、`WriteRecord`
  - `backend/ipdb/codec.go` — `encodePrefixKey` / `encodeAddrKey`（key 只含 masked 起始地址，prefix length 存 value）
  - `internal/cli/ip_merge.go` — `maybeWriteBack` / `writeIPInfoRecord`（回写触发条件）
  - `internal/cli/ip_lookup.go` — `LookupIP` Step 2/Step 4（单 IP 路径无条件调 ipinfo 并回写）
  - `internal/cli/ip_match.go` — `collectIPMatches`（域名路径回写）
- 运行环境：本地 macOS，dev
- 其他上下文：问题由系统审计发现并经代码核对确认。基础库由 CSV 构建阶段保证网段互不重叠，因此"找最近前驱"在纯基础库下成立；回写引入 /32/128 后该前提被打破。完整重构（v2 prefix-key LPM + base/overlay 分离）另立 roadmap 跟踪，本 issue 聚焦止血。

## 5. 严重程度

**P1** — 影响离线查询这一核心功能的正确性，会静默返回错误结果，且现象 B 会造成基础库数据被永久覆盖（重建前不可逆）；存在绕过方法（不配置 `ipinfo_token` 即不触发回写），故定 P1 而非 P0。

## 备注

- 本 issue 的修复目标是"止血"：在不引入 v2 存储格式的前提下，阻止回写破坏基础库查询正确性（候选方向如：回写记录与基础库物理隔离、或查询时识别 overlay 记录后回退基础库；具体方案 analyze 阶段定）。
- 根本性重构（真正的最长前缀匹配 key 结构 + CIDR 二级索引）由 `cs-roadmap` 的 ipdb-v2 文档承接，是 M3 更新功能的前置条件。
