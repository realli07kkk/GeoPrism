---
doc_type: decision
category: architecture
date: 2026-06-22
slug: ipdb-cidr-index-empty-value
status: active
area: ipdb存储
tags: [ipdb, cidr, secondary-index, pebble, codec, storage-layout]
---

## 背景

ipdb v2 在 primary LPM 主索引（按 `[kind][prefixLen][maskedAddr]` 组织）之外，新增 CIDR 二级索引（按 `[kind][maskedAddr][prefixLen]` 组织）以支撑 CIDR 查询的起始地址区间扫描。需要决定这条二级索引的 value 存什么，三种候选：① 复制整份 Record；② 存 primary key 指针；③ 零长度 value、查询时回查 primary。

## 决定

**CIDR 二级索引采用零长度 value：逻辑记录的 canonical Record value 只存在于 primary 索引，cidr key → 空 value。**

具体约束：

1. 编码层：
   ```
   primaryKey(prefix) -> encoded Record value
   cidrKey(prefix)    -> 零长度
   ```
2. `LookupCIDR` 从 cidr key 解出 `prefix`，**确定性生成 primary key 回查取 value**：
   ```go
   primaryKey := encodePrimaryKeyV2(prefix)
   value, closer, err := db.Get(primaryKey)
   ```
3. **完整性约束**：cidr key 存在但对应 primary key 不存在，**不得静默跳过**，返回 `ErrCorruptIndex`（IPDB 索引不一致）。
4. builder 对同一条逻辑记录的 primary 与 cidr key **必须在同一个 Pebble batch 中写入**；构建不变量 `primaryCount == cidrCount == Metadata.RowCount`。
5. 预留演进位：未来若 CIDR 查询确成瓶颈，以新的 capability `SchemaFeatureCIDRInlineValue` 引入"cidr 索引内联整份 Record"，而非现在就复制。

## 理由

- **primary key 可由 cidr key 解出的 prefix 完整推导**（`cidr key → prefix → primary key`），存 primary key 指针只是重复存储，且仍需二次 `Get`，没有实际收益。
- **复制整份 Record 代价高**：基础数据 value 接近双份存储；构建写入量与 compaction 成本增加；两份 Record 存在不一致风险；后续扩展字段时两套索引必须同步演进。
- **主查询路径仍是单 IP**：canonical value 放在 primary 最合理；CIDR 返回 N 条结果时增加 N 次 point Get，属可接受范围，由 acceptance benchmark 验证。

## 考虑过的替代方案

- **存 primary key 指针（已否决）**：仍需二次 `Get`，却额外占用 6（IPv4）或 18（IPv6）字节 value，纯重复无收益。
- **复制整份 Record（已否决）**：可减少 CIDR 查询的 point Get，但带来双份存储、构建/compaction 成本、一致性风险、双索引同步演进负担。若未来 CIDR 真成瓶颈，再以 `SchemaFeatureCIDRInlineValue` capability 演进，不在现在过早优化。

## 后果

- `LookupCIDR` 每条命中结果多一次 primary 索引 point Get。
- primary 与 cidr 必须同 batch 原子写入；否则可能出现"cidr 有、primary 无"的不一致 → `ErrCorruptIndex`。该不变量须在 `ipdb-v2-base-build` 验收（`primaryCount == cidrCount == RowCount`）。
- 为未来的内联 value 优化保留了 capability 演进位，不破坏当前格式。

## 相关文档

- roadmap（详设）：`../roadmap/ipdb-v2-lpm/ipdb-v2-lpm-roadmap.md` §4.1 / §4.3 / §4.6
- 同批决策（重复 prefix reject）：`2026-06-22-decision-ipdb-base-reject-duplicate-prefix.md`
- 配套决策（base 只读 + 回写写入 overlay）：`2026-06-20-decision-ipdb-base-readonly-writeback-to-overlay.md`
- 架构：`../architecture/ip-lookup.md`
