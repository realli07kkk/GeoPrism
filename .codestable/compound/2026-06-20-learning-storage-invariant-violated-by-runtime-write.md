---
doc_type: learning
track: pitfall
date: 2026-06-20
slug: storage-invariant-violated-by-runtime-write
component: backend/ipdb
severity: high
tags: [ipdb, writeback, lpm, keyspace, invariant, pebble, data-corruption]
---

## 1. 问题

存储格式依赖某个**构建期强制的不变量**来保证查询算法正确，但存在**绕过构建期、直接写入同一 keyspace 的运行期路径**，且该路径不维持不变量。结果：不变量被静默破坏，查询算法在污染数据上给出错误结果，且数据损坏可能不可逆。

具体场景（issue `2026-06-20-ipdb-writeback-breaks-lpm`）：ipdb v1 base 的 Pebble key 只编码网段 masked 起始地址（prefix length 存 value），单 IP 查询靠"SeekGE 后回看一条前驱再判 Contains"近似最长前缀匹配。该算法成立的唯一前提是 **base keyspace 内网段互不重叠且有序**——`BuildFromCSV` 在 CSV 导入阶段强制了这一点。但 ipinfo 在线回写把单 IP 的 /32 /128 写进**同一个 base keyspace**（绕过 CSV 校验），破坏了"不重叠"前提。

## 2. 症状

两类故障，都是**静默发生**（查询不报错，只给出错误结果）：

- **假 MISS（漏命中）**：某 IP 本被大网段覆盖，在该网段内另一个 IP 触发过 /32 回写后，最近前驱变成不含目标的 /32，查询返回 MISS，且不会回溯更前的大网段。
- **不可逆覆盖**：当回写 /32 与某基础网段**起始地址相同**时（例如对网段基地址触发回写），因 key 只编码起始地址（不含 prefix length），`Set` 直接覆盖原网段记录。原网段覆盖范围被永久缩成 /32，重建前不可恢复。

## 3. 没用的做法

- **"回写前加 guard：已命中粗网段就不回写"**——看似一处改动，实际不成立：
  1. 域名路径 `collectIPMatches` 调 `maybeWriteBack` 时传的是人为构造的**未命中对象**（`ipdb.Match{IP: ipText}`），guard 直接被绕过。要真正生效得改两条路径 + 处理已污染状态，不是单点改动。
  2. 更致命：**对已污染库不安全**。现象 A 产生的假 MISS 会被 guard 当成"可安全回写"，继续写入更多 /32，污染加速。guard 只在"干净 base + 所有路径正确传 match + 无其它写入入口"三个前提同时满足时才相对安全，不构成可靠防线。
- **"查询态回溯：前驱不含目标就继续 `Prev()` 向前找"**——能缓解假 MISS，但完全救不了不可逆覆盖（原 /24 已被 /32 覆盖，向前找不到）；最坏情况查询退化为大量反向扫描，性能不可控。

教训：**两类修复都是在原 keyspace 内打补丁**——只要"运行期写入与 base 共用 keyspace"这个结构没变，guard 会被绕过、回溯救不回数据。结构性的问题用补丁修，只会让"何时失效"更难预测。

## 4. 解法

**让运行期写入与 base 物理隔离**——不在同一 keyspace 打补丁：

- 紧急止血：彻底禁用所有向 base 的在线回写（删除 `maybeWriteBack` 链路，两条调用点全移除）。当次 ipinfo 响应仍用于合并输出，只是不持久化。已污染 v1 库提示用户 `ipdb build` 重建。
- 正式方案：在线缓存写入独立的 `overlay/db`（独立 Pebble，跨 base 版本存活），base 永久只读。Store 分别暴露 `LookupBase` / `LookupOverlay` / `PutOverlay`，最终优先级由 CLI 合并层按 `data_source_priority` 决定（**不在 Store 内硬编码 base-first**，否则与 ipinfo-first 冲突）。详见 roadmap `ipdb-v2-lpm`。

## 5. 为什么有效

- **不变量恢复成立**：base keyspace 重新只由 `BuildFromCSV` 写入，"互不重叠且有序"前提重新成立，LPM 近似算法恢复正确。
- **运行期写入不再有破坏面**：overlay 物理上无法覆盖 base；base 命中也不受 overlay 干扰（查询先看哪个由合并层决定）。
- **同 key 覆盖问题消失**：overlay key 与 base key 在不同 keyspace，即使编码方式相同也不会互相覆盖。
- **故障边界清晰**：base 重建不影响 overlay，overlay 清理不接触 base，回滚/备份各自独立。

## 6. 预防

**存储格式的不变量是"契约"，所有写入路径（不只是构建期）都必须维持它。** 通用检查清单（设计带不变量的存储时过一遍）：

1. **列出不变量**：写存储前明确"查询算法依赖哪些前提"（如有序、不重叠、key 唯一）。ipdb 的教训是这些前提没被显式记录，直到被违反才被发现。
2. **审计所有写入路径**：除了构建期写入，还有哪些路径会写同一 keyspace？运行期增量写、缓存写、迁移写、测试注入写——每条都要么维持不变量，要么物理隔离到别的 keyspace。
3. **不变量不能靠"调用方自觉"**：CSV 构建在 `builder.go` 里强制校验了有序+不重叠，但 `WriteRecord`（运行期写入入口）没有任何校验。**校验要在写入边界做，不能假设上游传对了。**
4. **key 编码要包含区分维度**：如果不同记录语义上可能"重叠"，key 必须携带足够区分度（如 prefix length）。ipdb 的 key 不含 prefix length，使同起始地址记录必然互相覆盖——这在"不重叠"约束下没问题，但约束一破就是不可逆损坏。
5. **可逆 vs 不可逆**：评估故障的可恢复性。假 MISS 可逆（清污染数据即可），同 key 覆盖不可逆（原数据已没）。不可逆故障要设更高的防御门槛——宁可禁止写入，也不要给一条"可能覆盖"的路径。

**通用模式**：当一个数据结构依赖"互斥/有序/唯一"等不变量，且存在运行期写入路径时，默认让运行期写入走**物理独立的 keyspace / 分区**，而不是试图在原地维持不变量。后者的维护成本会随写入路径数量爆炸，且任何一条漏掉就是静默损坏。

## 相关文档

- issue：`../issues/2026-06-20-ipdb-writeback-breaks-lpm/`（report / analysis / fix-note）
- decision：`./2026-06-20-decision-ipdb-base-readonly-writeback-to-overlay.md`
- roadmap（正式方案）：`../roadmap/ipdb-v2-lpm/ipdb-v2-lpm-roadmap.md`
