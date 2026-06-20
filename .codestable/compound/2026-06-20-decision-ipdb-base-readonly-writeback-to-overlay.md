---
doc_type: decision
category: constraint
date: 2026-06-20
slug: ipdb-base-readonly-writeback-to-overlay
status: active
area: ipdb存储
tags: [ipdb, writeback, overlay, base, lpm, keyspace, invariant]
---

## 背景

ipdb v1 存储格式（`backend/ipdb/`）的 Pebble key 只编码网段的 masked 起始地址，prefix length 存在 value 里；单 IP 查询靠"SeekGE 后回看一条前驱再判 Contains"近似最长前缀匹配（LPM）。这套算法成立的唯一前提是 **base keyspace 内网段互不重叠且有序**——`BuildFromCSV` 在 CSV 导入阶段强制了这一点（`backend/ipdb/builder.go:162-167`）。

但 ipinfo 在线回写历史上把单 IP 的 /32（IPv6 /128）写进**同一个 base keyspace**（旧 `maybeWriteBack` → `WriteRecord` → `s.db.Set`），绕过了"不重叠"不变量，导致两类正确性故障（详见 issue `2026-06-20-ipdb-writeback-breaks-lpm`）：

- **现象 A（假 MISS）**：被大网段覆盖的 IP，因为最近前驱变成不包含它的 /32 而返回 MISS。
- **现象 B（不可逆覆盖）**：回写 /32 与已有网段起始地址相同时，因 key 不含 prefix length，`Set` 直接覆盖原网段记录，重建前不可恢复。

## 决定

**ipdb base keyspace 在运行期永久只读；任何在线回写必须写入物理独立的 overlay，不得共用 base keyspace。**

具体约束：

1. base 数据库（`versions/{buildID}/db`）以只读方式打开，运行期不接受任何写入
2. 在线缓存走独立 `overlay/db`（独立 Pebble，跨 base 版本存活）
3. base 与 overlay 分别查询（`LookupBase` / `LookupOverlay` / `PutOverlay`），最终优先级由 CLI 合并层（`mergeIPInfo`）按 `data_source_priority` 决定，**不在 Store 内硬编码 base-first**
4. CIDR 查询暂时只查 base，不读 overlay（overlay 只有精确 /32·/128 查找能力，无 CIDR 扫描语义）
5. 旧 `WriteRecord`（向 base 写单 IP）废弃，任何路径都不得再调用

## 理由

- **根因最直接**：问题本质是"运行期写入违反 base 不变量"，物理隔离从机制上同时根治现象 A 和 B，不依赖任何运行期判断的正确性。
- **不丢功能**：保留 ipinfo 在线纠偏能力（overlay 在 ipinfo-first 下可优先）；优先级决策留在 CLI 合并层，与现有 `data_source_priority` 语义一致。
- **故障边界清晰**：base 重建 / 版本切换不影响 overlay；overlay 清理不接触 base；生命周期、备份、回滚各自独立。
- **为 v2 真 LPM 铺路**：overlay/base 分离、key kind 分配（overlay 用 `0x34/0x36`）、接口形态正是 roadmap `ipdb-v2-lpm` §4 已定义的内容，投资不浪费。

## 考虑过的替代方案

- **回写前 guard（已否决）**：方向上想做"已命中粗网段则不回写"。但当前域名路径 `collectIPMatches` 调 `maybeWriteBack` 时传的是人为构造的未命中对象（`ipdb.Match{IP: ipText}`），guard 会被绕过；要真正生效需改两条路径 + 处理已污染状态，不是单点改动。更致命的是对已污染库不安全——现象 A 的假 MISS 会被当成"可安全回写"继续污染。
- **查询态回溯（已否决）**：改 `LookupIP` 让前驱不含目标时继续 `Prev()` 向前找。能缓解现象 A，但完全无法恢复现象 B 的同 key 覆盖数据；最坏情况查询退化为大量反向扫描，性能不可控。

## 后果

- **现有 v1 库需重建**：format version 升级到 2 时，`OpenCurrent` 读到 v1 返回 `ErrLegacyFormat`，CLI 提示用户重新 `ipdb build`。不做 v1→v2 数据级自动迁移（CSV 是数据源真相，重建即正确）。
- **紧急止血先行**：正式方案 A′ 落地前，已先禁用所有向 base 的在线回写（issue 第一层 fix），不保留"base 未命中仍可写 base"的例外。期间 ipinfo 在线数据仍可通过配置 `ipinfo_token` 获得（合并层 `mergeIPInfo` 不变），只是不落盘。
- **CIDR 暂不读 overlay**：若后续确需 CIDR 结果包含在线缓存，需单独定义 `--include-overlay` 及覆盖/去重/排序规则。

## 相关文档

- issue：`../issues/2026-06-20-ipdb-writeback-breaks-lpm/ipdb-writeback-breaks-lpm-report.md`（含 analysis / fix-note）
- roadmap（正式方案 A′ 详设）：`../roadmap/ipdb-v2-lpm/ipdb-v2-lpm-roadmap.md`
- 架构：`../architecture/ip-lookup.md`
- attention.md 摘要：`.codestable/attention.md` "其他"节
