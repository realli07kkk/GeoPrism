---
doc_type: decision
category: architecture
date: 2026-06-22
slug: ipdb-base-reject-duplicate-prefix
status: active
area: ipdb存储
tags: [ipdb, builder, prefix, duplicate, overlap, lpm, data-model]
---

## 背景

ipdb v2 为实现真正的 LPM，把 prefix length 编进 Pebble key，并**删除了 v1 builder "任何区间重叠都拒绝"的约束**（v1 `backend/ipdb/builder.go:162-167`），改为支持重叠记录——这是真 LPM（同一 IP 可被多层网段覆盖）和未来 M3 多来源合并的前提。

但放开重叠后出现一个新问题：v2 key 虽含 prefixLen，**相同 prefix（同 family 内 `Masked()` 后完全相同的 `netip.Prefix`）仍是相同的 Pebble key**。若不显式处理，`batch.Set` 会按 last-wins 静默覆盖；多来源数据合并时，同一 prefix 也可能出现多条业务字段不同的记录。必须为"重复 prefix"定义明确策略。

## 决定

**base 构建对重复 prefix 采用 `reject duplicate`：同一 address family 内，两条输入记录经 `Masked()` 后得到完全相同的 `netip.Prefix` 即视为重复，无论两条记录的业务字段是否相同，构建一律失败（新增 `ErrDuplicatePrefix`）。**

具体约束：

1. **允许不同 prefix 的重叠**，例如：
   ```
   10.0.0.0/8
   10.0.0.0/16
   10.1.0.0/16
   ```
2. **仅拒绝完全相同的 prefix**，例如：
   ```
   10.0.0.0/8
   10.0.0.0/8     ← ErrDuplicatePrefix
   ```
3. 删除 v1 的"任何区间重叠都拒绝"，重叠场景交由真 LPM（primary ladder）与 CIDR 三段查询正确处理。
4. 错误信息须包含：规范化后的 prefix、首次出现行号、重复出现行号、两条记录的 source 或关键字段。例如：
   `CSV 第 128 行出现重复网段 10.0.0.0/8，首次出现于第 42 行`。
5. **输入排序契约**：保留"每个 family 内起始地址非递减"作为 builder 输入契约 + 性能优化，但它**不再是查询正确性的前提**；相同起始地址、不同 prefix length 合法；重复判断必须比较**完整 prefix**，不能只比较地址。

## 理由

- **结果不依赖输入顺序**：first-wins / last-wins 会让构建结果取决于 CSV 行序，且 last-wins 正是 Pebble `Set` 的默认静默行为——等于把数据质量问题藏进存储层。严格 reject 把问题暴露在构建期。
- **存储层不偷偷决定来源可信度**：相同 prefix 的来源冲突属于数据整合范畴，应由未来 M3 的数据整合层显式解决，而不是让存储层隐式选一条。
- **每条逻辑记录在 primary 与 cidr 二级索引中唯一**：避免两套索引出现"同 key 多义"。
- **严格失败更易发现上游数据问题**：即使两条 Record 完全相同也不做隐式去重——隐式去重会掩盖上游重复。

## 考虑过的替代方案

- **first-wins / last-wins（已否决）**：结果依赖输入顺序；last-wins 还与 Pebble 默认覆盖行为重合，等于"不处理"，把数据缺陷静默吞掉。
- **key 中加入 source 允许同 prefix 多来源共存（已否决）**：把来源冲突下放到存储层与查询层，LPM / CIDR 需额外定义"同 prefix 多记录返回哪条"，复杂度外溢；该能力留给 M3 数据整合阶段再评估。
- **相同业务字段静默去重（已否决）**：仍需定义"字段不同"的情形，且掩盖上游重复，违背"暴露问题"的初衷。

## 后果

- builder 需维护"已见 prefix"判定（可依赖输入按 `(起始地址, prefixLen)` 排序做流式相邻比较，或维护全集判重）——具体实现归 `ipdb-v2-base-build` 的 feature-design。
- 上游 CSV / 未来多来源数据若含重复 prefix，构建直接失败，必须在数据整合阶段解决，不能指望存储层兜底。
- 与"允许不同 prefix 重叠"配套：v2 builder 不再有 overlap reject，只有 duplicate-prefix reject。
- 该约束是新增的、粒度比 v1 更精确的检查（v1 把任何区间重叠都视为错误）。

## 相关文档

- roadmap（详设）：`../roadmap/ipdb-v2-lpm/ipdb-v2-lpm-roadmap.md` §4.1 / §4.3 / §6
- 配套决策（base 只读 + 回写写入 overlay）：`2026-06-20-decision-ipdb-base-readonly-writeback-to-overlay.md`
- 同批决策（CIDR 二级索引零长度 value）：`2026-06-22-decision-ipdb-cidr-index-empty-value.md`
- 架构：`../architecture/ip-lookup.md`
