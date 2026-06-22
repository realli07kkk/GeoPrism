---
doc_type: learning
track: pitfall
date: 2026-06-22
slug: codec-decode-boundary-check
component: backend/ipdb
severity: high
tags: [ipdb, codec, decode, protocol-invariant, uint64-overflow, netip, review]
---

# 二进制 codec 的 decode 路径必须与 encode 路径对称校验协议不变量

## 问题

实现 ipdb v2 key/value codec 时（feature `2026-06-22-ipdb-v2-schema`），decode 路径漏掉了协议不变量校验，两处独立缺陷被 review 抓出：

1. **CR-001（Critical）**：key decoder 只校验 kind 字节和 key 长度，就直接 `netip.PrefixFrom(addr, prefixLen)` 返回。decode 出的 prefix 没有：
   - `IsValid()` 判断（越界 prefixLen 会让 `PrefixFrom` 返回 `Bits()==-1` 的 invalid Prefix）
   - prefixLen family 上限校验（V4 key 里塞 prefixLen=33 的字节）
   - masked addr 校验（key 布局契约要求 `maskedAddr`，带 host bits 的 addr 视为损坏）

2. **CR-002（Warning）**：value decoder 把 `binary.Uvarint` 返回的 `uint64` 直接 `int()` 转换后做 `offset+int(fieldLen)` 边界检查。长度字段值接近 `uint64` 上限时，`int(fieldLen)` 溢出为负数，边界检查失真，后续 slice 操作 panic。

## 症状

- CR-001：损坏/篡改的 key 被 decode 当成"成功"返回非法 prefix，后续 builder 同 batch 双写、CIDR 回查 primary、network 回填都建立在坏契约上。round-trip 不变量（primary ↔ cidr 互相还原同一 prefix）名存实亡——单测用的都是合法 key，全绿但地基是坏的。
- CR-002：本地损坏 value（磁盘损坏 / 部分写 / 版本串台）触发 panic 而非返回 decode error。不是远程攻击面（value 来自本地 Pebble），但违反"decoder 对损坏数据默认返回 error"的纪律。

## 没用的做法

- **"测试都过了所以没问题"**：CR-001 的单测 round-trip 全绿，但只覆盖了合法 key——decoder 放水让坏 key 也"成功"，测试根本没机会暴露。证明测试通过 ≠ 协议不变量守住。
- **只看 encode 路径校验**：encode 的 `validatePrefixV2` 三条校验（IsValid / Masked / prefixLen 边界）都写了，让人产生"协议校验已落齐"的错觉，忽略了 decode 路径是另一套独立入口。
- **信任 Go 标准库会拒绝非法输入**：`netip.PrefixFrom(addr, 33)` 不 panic、不 error，返回 `Bits()==-1` 的 invalid Prefix。Go 标准库很多 API 对越界输入是"返回零值/invalid 而非报错"，decoder 必须主动判 `IsValid()`。

## 解法

两条对称原则：

1. **decode 路径必须重新校验 encode 写入时的所有协议不变量**——不能假设"key 是自己 encode 出来的所以一定合法"。抽一个 decode 专用校验 helper（`decodePrefixV2`），与 encode 的 `validatePrefixV2` 对称，primary/cidr decoder 共用。校验四点：family 与 kind 一致 / prefixLen 在 family 上限 / IsValid / addr 已 masked。

2. **`uint64` → `int` 转换前必须先做上界检查**——用 `uint64(remainingLen)` 比较，确认不溢出再转 int。抽 helper `addLenOffset(offset, fieldLen uint64, valueLen int)`，所有 uvarint 长度字段 decode 共用。

## 为什么有效

- 对称校验让协议不变量在 encode 和 decode 两个入口都被守住，任一入口放水都不会让坏数据溜进系统。
- uint64 上界检查在转换前发生，溢出无机会触发 panic，decoder 对损坏数据稳定返回 error。
- helper 收敛校验逻辑，新增 codec 函数时复用而非 copy-paste（copy-paste 是漏校验的主要来源）。

## 预防

- **写二进制 codec 时，把 encode 和 decode 的协议不变量列成清单，两边逐条核对**——不能只写 encode 校验。review 时专门看"decode 返回前有没有重新校验"。
- **任何 `uint64`/`uint32` → `int` 转换前，grep 一遍有没有先做上界检查**。Go 的 `int` 在 64 位平台是 int64，但 `uint64 > math.MaxInt64` 仍会溢出为负。
- **decoder 单测必须有"损坏输入矩阵"**：篡改 version / flags / 长度字段 / kind 字节 / 多余尾部 / 截断，每类至少一个 negative test。只测合法 round-trip 是放行坏数据。
- **`netip.PrefixFrom` / `AddrFrom4` / `AddrFrom16` 等 Go stdlib API 对越界输入不报错**——调用后必须主动判 `IsValid()` / `Addr()==Masked().Addr()`（这条已沉淀到 `.codestable/attention.md` "命令与脚本陷阱"分节）。
- **acceptance 第 3 节验收场景要显式覆盖 decode 路径的 negative case**——本 feature 原验收清单只写了 encode 侧的"未 Masked / 越界"，decode 侧是 review 才补上的（CR-003 同类问题：overlay 全空字段测试缺失）。验收清单逐条核对时不能只看"有测试"，要看"测的是不是清单那条"。

## 相关文档

- 触发 feature：`.codestable/features/2026-06-22-ipdb-v2-schema/`（CR-001 / CR-002 修复记录在 implement 汇报）
- 同批决策（IPv4-mapped IPv6 family 收敛）：`2026-06-22-decision-ipdb-codec-reject-ipv4-mapped-ipv6.md`
- 同批决策（uint64→int 上界检查约束）：`2026-06-22-decision-codec-uint64-int-bound-check.md`
- attention.md 已沉淀的硬约束：`netip.PrefixFrom` 越界返回 invalid Prefix 而非 panic（"命令与脚本陷阱"分节）
