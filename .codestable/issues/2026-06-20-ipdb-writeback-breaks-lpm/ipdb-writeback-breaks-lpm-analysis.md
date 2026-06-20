---
doc_type: issue-analysis
issue: 2026-06-20-ipdb-writeback-breaks-lpm
status: confirmed
root_cause_type: state-pollution
related: [ipdb-writeback-breaks-lpm-report.md, ../../roadmap/ipdb-v2-lpm/ipdb-v2-lpm-roadmap.md]
tags: [ipdb, writeback, lookup, data-corruption, lpm, keyspace, invariant-violation]
---

# ipdb 在线回写破坏离线库查询正确性 根因分析

## 1. 问题定位

| 关键位置 | 说明 |
|---|---|
| `backend/ipdb/builder.go:162-167` 构建期约束 | CSV 构建强制"同 family 内网段有序且互不重叠"。这是 v1 存储格式的**核心不变量**，也是 `LookupIP` 算法成立的前提 |
| `backend/ipdb/store.go:161-170` `LookupIP` 迭代 | `SeekGE(queryKey)` 后最多 `Prev()` 一次 / `Last()` 一次，命中即停。整个查询依赖"keyspace 内前驱唯一且必然覆盖目标"这一不变量 |
| `backend/ipdb/store.go:186` `LookupIP` 判定 | 最近前驱 `!prefix.Contains(addr)` 时**直接返回 MISS，不向更前的大网段回溯**——分叉点 A |
| `backend/ipdb/store.go:84-111` `WriteRecord` | 运行期回写直接 `s.db.Set(key, value, nil)`，与基础库共用同一 keyspace，**不校验也不维持"不重叠"不变量**——控制缺口 |
| `backend/ipdb/codec.go:11-14` `encodePrefixKey` | key 只编码 masked 起始地址，**不含 prefix length**。`1.0.0.0/24` 和 `1.0.0.0/32` 编码出的 key 完全相同——分叉点 B 的机制来源 |
| `internal/cli/ip_merge.go:61-79` `maybeWriteBack` | 回写入口无 guard：ipdb 未命中直写；ipdb 命中但 7 字段不同也覆盖写。无"目标 IP 是否已落在大网段内"的判断 |
| `backend/ipinfo/client.go:27-42` `Response.ToRecord` | ipinfo 结果强制写成 `/32` / `/128`，与 CSV 网段级数据共用 keyspace |
| `internal/cli/ip_lookup.go:119-122` 单 IP 路径 | 无条件触发回写：只要 `ipinfoResp != nil && a.ipdbStore != nil` 就调 `maybeWriteBack` |
| `internal/cli/ip_match.go:162-173` 域名路径 | DNS 结果 IP 匹配同样走 `maybeWriteBack`，且调用时传入的是 `ipdb.Match{IP: ipText}` 构造的未命中对象（见 §5.B） |

## 2. 失败路径还原

### 现象 A（漏命中）失败路径

**正常路径（无回写）**：查 `1.0.0.11` → `LookupIP` `SeekGE(1.0.0.11)` → 命中下一条（如 `1.0.1.0/24`）→ `Prev()` 回到 `1.0.0.0/24` → `Contains` 为 true → 命中。正确。

**失败路径**：

1. 之前查过 `1.0.0.10`，ipinfo 数据与 `1.0.0.0/24` 不同 → `maybeWriteBack` → `WriteRecord(1.0.0.10/32)` → 写入 key `0x04 0x01 0x00 0x00 0x0A`（与基础 keyspace 同段，落在 `1.0.0.0/24` 之后、`1.0.1.0/24` 之前）
2. 现在查 `1.0.0.11` → `LookupIP` `SeekGE` → `Prev()` 命中前驱 `1.0.0.10/32`（比 `1.0.0.0/24` 更近）
3. `1.0.0.10/32` `Contains(1.0.0.11)` 为 **false** → 走 `store.go:186` 返回 `Matched=false`
4. **不会继续 `Prev()` 找 `1.0.0.0/24`** → MISS

**分叉点 A**：`backend/ipdb/store.go:186` — 最近前驱不含目标 IP 时直接判 MISS 而不回溯。在"keyspace 互不重叠"前提下成立，回写打破了这个前提。

### 现象 B（永久覆盖）失败路径

**正常路径**：`1.0.0.0/24` 以 key `0x04 0x01 0x00 0x00 0x00` 存在，查询范围内任何 IP 都能回看到这条。

**失败路径**：

1. 查 `1.0.0.0`（网段基地址）→ ipinfo 返回数据与 `/24` 不同 → `maybeWriteBack` → `WriteRecord(1.0.0.0/32)`
2. `encodePrefixKey(1.0.0.0/32)` === `encodePrefixKey(1.0.0.0/24)` —— key 完全一致（key 只编码 masked 起始地址）
3. `s.db.Set(key, value, nil)` **覆盖**原 `/24` 的 value，prefix length 字段从 `24` 变 `32`
4. 原 `/24` 记录从此不可见（重建前不可逆）。后续查 `1.0.0.5`：命中 `1.0.0.0/32`，`Contains` 为 false → MISS

**分叉点 B**：`backend/ipdb/codec.go:11-14` — key 不含 prefix length，使同起始地址不同前缀长度的记录共享 key；叠加 `store.go:106` `Set` 的覆盖语义，造成不可逆数据损坏。

## 3. 根因

**根因类型**：`state-pollution`（主，体现为"违反存储不变量"）+ `data-format`（次，放大现象 B）+ `missing-guard`（触发控制缺口）

**根因描述**：

- **主根因：运行期写入路径违反了 v1 存储格式的"不重叠区间"不变量。** v1 base 存储依赖"同 family 内区间有序且互不重叠"（`builder.go:162-167` 构建期强制），这是 `LookupIP` "最近前驱即覆盖者"算法成立的唯一前提。而运行期 `WriteRecord` 把 ipinfo 的 `/32`、`/128` 写进同一个 base keyspace（`store.go:85` → `store.go:106`），绕过了该不变量。最近前驱查询因此产生假 MISS（现象 A）。
- **次根因：key 不包含 prefix length，使同起始地址的不同网段发生覆盖。** 当前编码在"网段互不重叠、同起始地址唯一"的原始约束下可以工作；问题在于系统后来允许一种不满足该约束的数据写入。它主要放大了现象 B：当回写记录与基础网段起始地址相同时，`Set` 持久化覆盖原记录（不可逆）。
- **控制缺口：回写入口（`maybeWriteBack` / `WriteRecord`）没有阻止不符合 base 数据约束的记录进入 base。** 既不判断目标 IP 是否已被 base 网段覆盖，也不判断写入是否会与现有 base 记录重叠。

**是否有多个根因**：是。主因为"违反不变量"，次因为 key 编码缺陷，控制缺口为缺少回写 guard。三者叠加才同时产生现象 A 与现象 B；任缺一项，现象 B（不可逆损坏）都不会发生。

## 4. 影响面

- **影响范围**：不止报告的两个场景。
  - **所有单 IP 快捷查询**（`geoprism <ip>`）：只要本地有 ipdb + 配了 `ipinfo_token`，每次查询都可能回写，污染累积。
  - **所有域名 DNS 查询附带的 IP 匹配**（`geoprism query` / `geoprism <domain>` 的"IP 匹配详情"）：`collectIPMatches` 同样走 `maybeWriteBack`（`ip_match.go:170`）。
  - **CIDR 查询**（`geoprism <cidr>`）：`LookupCIDR` 用区间扫描 + `prefixesOverlap`，回写的 /32 会作为独立相交网段被返回（误多一条）；CIDR 基地址回退单 IP 时（`cidr_lookup.go:140`）也受现象 A/B 影响。
  - **ipinfo-first 模式**：`collectIPMatches` 在 ipinfo-first 下命中的 IP 也会调 ipinfo（`ip_match.go:157`），回写更激进，污染更快。
- **潜在受害模块**：所有依赖 `ipdbStore.LookupIP` 正确性的上层路径——`runIPLookup`、`runCIDRLookup`（回退分支）、`collectIPMatches`、JSON 输出（`-j` 的 `source` 字段可信度下降）、`Source` 列展示。
- **数据完整性风险**：**有，且严重**。现象 A 是查询态错误（基础数据未被改）；现象 B 是**持久化数据损坏**——基础库的网段记录被覆盖成 /32，重建 ipdb 前不可恢复，违反"基础库不可被运行期写入破坏"的隐含契约。
- **严重程度复核**：**维持 P1**。核心功能静默错误 + 现象 B 不可逆接近 P0，但"不配 token 即不触发"的确定性绕过让它停在 P1。

## 5. 修复方案

> 修复分两层：**紧急止血**（本 issue 的 fix 范围）+ **正式方案 A′**（由 roadmap `ipdb-v2-lpm` 承接，本 issue 不重复实现）。下面先列三个候选方向的最终评价，再给出已确认的两层修复。

### 候选方向评价

#### 方向 A′：独立 `overlay/db`，base 永久只读（正式方案，由 roadmap 承接）

- **做什么**：
  - 在线缓存迁移到独立的 `~/.geoprism/ipdb/overlay/db`（独立 Pebble，跨 base 版本存活）；base 以只读方式打开。
  - 删除 / 废弃 `Store.WriteRecord`；在线结果只能走 `PutOverlay`。
  - Store 分别暴露 `LookupBase` / `LookupOverlay` / `PutOverlay`（见 roadmap §4.3 / §4.4 / §4.5），**不在 Store 内硬编码 base-first**——最终优先级由 CLI 的 `mergeIPInfo` 按 `data_source_priority` 决定（ipdb-first 走 base 优先，ipinfo-first 走 overlay 优先）。
  - overlay key kind 使用 `0x34` / `0x36`（与 roadmap §4.1 对齐；v2 primary 用 `0x14/0x16`，cidr 二级索引用 `0x24/0x26`，三者互不冲突）。
  - CIDR 查询暂时**只查 base，不读 overlay**（overlay 只有精确 /32·/128 查找能力，无 CIDR 扫描语义；若后续需要再单独定义 `--include-overlay` 及覆盖/去重/排序规则）。
- **优点**：从机制上根治 A/B——overlay 物理上无法覆盖 base，base 命中也不受 overlay 干扰；不丢"ipinfo 在线纠偏"能力；base 重建 / 版本切换不影响 overlay，overlay 清理不接触 base，故障边界清晰；为 v2 真 LPM 铺路。
- **缺点 / 风险**：改动面大（新增 overlay 存储、改 Store 聚合、cli 各路径切接口、format version 升级）；已污染 v1 库必须重建（roadmap §6 已确认选择"明确报错 + 提示重建"，不做数据级迁移）。
- **影响面**：`backend/ipdb/`（codec / store / builder / 新增 overlay.go）、`internal/cli/`（ip_lookup / ip_match / cidr_lookup / app）、format version 升级；对外行为：v1 库需重建，其余查询语义不变。

#### 方向 B：回写前 guard（已否决，不作正式止血）

- **评价**：方向上想做"已命中粗网段则不回写"，但**当前代码结构下它不是一处改动，且对已污染库不安全**：
  1. 域名路径 `ip_match.go:162-173` 在拿到 ipinfo 结果后把 `match` 替换成 ipinfo 结果，调用 `maybeWriteBack` 时传的是人为构造的 `ipdb.Match{IP: ipText}`（未命中对象）。即使 `maybeWriteBack` 加 `if ipdbMatch.Matched { return }`，域名路径仍会认为 base 未命中并继续写入。要真正生效至少需要：保留独立的 `baseMatch`、所有调用点传真实 base 查询结果、修两条路径、处理已污染状态——不是单点改动。
  2. **对已污染库不安全**：一旦现象 A 已发生，`LookupIP` 会对原本被 base 大网段覆盖的 IP 返回假 MISS，guard 会把这个假 MISS 当成"可安全回写"，继续写入更多 /32，进一步污染。它只在"完全重建干净 base + 所有路径正确传 baseMatch + 以后无其它写入入口"三个前提同时满足时才相对安全，不构成可靠的单点防线。

#### 方向 C：查询态回溯（可缓解 A，救不了 B）

- **评价**：改 `LookupIP` 让前驱不含目标时继续 `Prev()` 向前找。在不同起始地址的规范 CIDR 嵌套关系下，回溯到第一条 `Contains(addr)` 的记录通常能给出最具体候选，可缓解现象 A。但：**完全无法恢复同 key 覆盖的数据**（现象 B 的 /24 已被 /32 覆盖，向前找不到原记录）；最坏情况查询退化为大量反向扫描，性能不可控。核心反对理由是数据丢失无解 + 性能不可控，而非候选语义不确定。

### 已确认的两层修复

#### 第一层：紧急止血（本 issue fix 范围）

在 A′ 落地前，**立即禁用所有向 base 的在线回写**——不留"base 未命中时仍可写 base"的例外，直接全部禁止：

- 实现方式二选一（具体由 `cs-issue-fix` 定）：在 `maybeWriteBack`（`ip_merge.go:62`）入口直接 `return`，或彻底移除其所有调用点（`ip_lookup.go:121` / `ip_match.go:170`）。
- 当前查询仍可使用当次 ipinfo 响应（合并逻辑 `mergeIPInfo` 不变），只是不持久化。
- 这样立即满足：不再产生新的重叠 /32；不再覆盖同起始地址的大网段；不依赖 `LookupIP` 当前结果是否可信；不依赖调用方是否正确传递 baseMatch；不会继续污染 CIDR 查询。
- 对可能已污染的 v1 库，在打开时给出提示（具体提示文案与触发位置由 `cs-issue-fix` 定，建议挂在 `ensureIPDBStore` 或首次查询时）：

  ```
  检测到旧版可写 base，在线回写已停用。
  如曾启用 ipinfo 回写，请重新执行 ipdb build 重建离线库。
  ```

#### 第二层：正式方案 A′（roadmap `ipdb-v2-lpm` 承接）

按 roadmap §3-§5 落地：codec-v2 → base-lpm（最小闭环）→ cidr-index → overlay-store → lookup-integration。其中 `ipdb-overlay-store`（roadmap 第 4 条）落地后正式取代本 issue 第一层止血，届时在 issue fix-note 标注（roadmap §7 已约定）。本 issue 不重复定义 A′ 的实现细节。

### 推荐方案

**正式方案 A′（由 roadmap 承接）+ 第一层紧急止血（本 issue fix）**，理由：

1. **根因最直接**——问题本质是"运行期写入违反 base 不变量"，A′ 通过物理隔离 + base 永久只读从机制上消除，不依赖运行期判断正确性。
2. **不丢功能**——保留 ipinfo 在线纠偏（overlay 在 ipinfo-first 下可优先），且优先级决策留在 CLI 合并层，与现有 `data_source_priority` 语义一致。
3. **与 roadmap 对齐、投资不浪费**——A′ 的 overlay/base 分离、key kind 分配、接口形态正是 roadmap `ipdb-v2-lpm` 已定义的内容，本 issue 不另起设计。
4. **紧急止血先于正式方案**——现象 B 是不可逆损坏，不能等 A′ 全部落地；第一层禁写立即止住新增污染，已污染库提示重建。

**否决 B/C**：B 在当前代码结构下非单点改动且对已污染库不安全；C 救不了现象 B 的数据丢失且性能不可控。

## 备注

- 本 issue 的 fix 范围**仅限第一层紧急止血**（禁用 base 回写 + 已污染库提示重建）。
- 第二层 A′ 由 roadmap `ipdb-v2-lpm` 承接，不在本 issue 实现；`ipdb-overlay-store` 落地后在 fix-note 标注取代关系。
- 紧急止血期间，用户若需要 ipinfo 在线数据，仍可通过配置 `ipinfo_token` 获得（合并层 `mergeIPInfo` 不变），只是结果不落盘。
