---
doc_type: decision
category: constraint
date: 2026-06-22
slug: codec-uint64-int-bound-check
status: active
area: ipdb存储
tags: [ipdb, codec, decode, uint64, int, overflow, bound-check, constraint]
---

## 背景

ipdb v2 value 协议用 `binary.Uvarint` 编码字段长度（变长前缀），decode 时读出 `uint64` 再转 `int` 做切片边界计算：

```go
fieldLen, n := binary.Uvarint(value[offset:])  // 返回 uint64
offset += n
if offset+int(fieldLen) > len(value) { ... }    // int(fieldLen) 可能溢出
fields[i] = string(value[offset : offset+int(fieldLen)])
```

`int(fieldLen)` 在 `fieldLen` 接近 `uint64` 上限时溢出为负数，`offset+int(fieldLen)` 的边界检查失真（负数 + offset 可能仍 ≤ len(value)），随后 `value[offset : offset+int(fieldLen)]` 切片触发 panic。

实现 `ipdb-v2-schema` 时初版两处独立踩中（CR-002）：`decodeRecordFields`（7 字段长度）和 overlay source decode（source 长度）。损坏 value（磁盘损坏 / 部分写 / 版本串台）可构造出超大 uvarint 字段触发 panic，违反"decoder 对损坏数据默认返回 error"的纪律。

## 决定

**ipdb 所有 codec（及任何处理外部字节序列的 decode 路径）把 `uint64`/`uint32` 转 `int` 前必须先做上界检查，确认不溢出再转换；禁止"直接 `int()` 转换后做边界检查"的写法。**

具体约束：

1. **转换前上界检查**：用 `uint64(remainingLen)` 比较，确认 `fieldLen <= uint64(len(buf)-offset)` 后再转 int。
2. **抽 helper 收敛**：本 feature 抽了 `addLenOffset(offset int, fieldLen uint64, valueLen int) (int, bool)`，所有 uvarint 长度 decode 共用，避免 copy-paste 漏校验。
3. **decoder 对损坏数据默认返回 error**，不 panic——这是比"转换前检查"更深一层的纪律：任何来自外部存储 / 网络 / 文件的字节序列都假定可能损坏。
4. **适用范围**：不仅限 uvarint——任何 `uint*` → `int` 转换、`uint*` → 更窄整型转换前都要做上界检查。

## 理由

- **Go 的 `int` 在 64 位平台是 int64，但 `uint64 > math.MaxInt64` 仍会溢出为负**——`int()` 转换是静默的，不会 panic 也不会报错，溢出后的负数让所有 `> len()` 比较失效。
- **decoder 是系统边界**：value 来自 Pebble 本地存储，但本地存储可能因磁盘损坏 / 进程崩溃部分写 / 库版本串台而产生任意字节。"数据是自己 encode 的所以一定合法"是错误假设。
- **panic 而非 error 会拖垮整个进程**：一次损坏 value 的 decode 让整个 CLI 崩溃，违反"单条数据损坏只影响该条查询，不拖垮主流程"的组件级降级纪律（roadmap §4.5 降级矩阵）。
- **上界检查代价极低**：一次 `uint64` 比较，相对 decode 的其他开销可忽略。

## 考虑过的替代方案

- **`int64(fieldLen)` 代替 `int(fieldLen)`**（已否决）：64 位平台等价于 `int`，32 位平台反而更窄；没解决根本问题，只是推迟溢出阈值。
- **依赖上层"value 一定合法"的假设**（已否决）：等于不处理。Pebble 不保证 value 完整性（无校验和），磁盘损坏会静默产出坏字节。

## 后果

- ipdb v2 value decode（base value / overlay value）对超大 uvarint 长度稳定返回 error，不 panic。
- `addLenOffset` 是 ipdb package 内 helper，后续 base-build/query/overlay-store 的 decode 路径若涉及 uvarint 长度应复用，不另起 copy-paste。
- 此约束是 ipdb v2 系列后续 feature decode 路径的硬约束输入。
- 更广适用：项目内任何处理外部字节序列的 decode 路径都应遵守此纪律（不限 ipdb）。

## 相关文档

- 触发 feature：`.codestable/features/2026-06-22-ipdb-v2-schema/`（CR-002 修复）
- 同批 learning（decode 边界校验坑点）：`2026-06-22-learning-codec-decode-boundary-check.md`
- 同批 decision（IPv4-mapped IPv6 拒绝）：`2026-06-22-decision-ipdb-codec-reject-ipv4-mapped-ipv6.md`
- 架构：`../architecture/ip-lookup.md`
