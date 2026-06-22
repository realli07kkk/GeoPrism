---
doc_type: decision
category: constraint
date: 2026-06-22
slug: ipdb-codec-reject-ipv4-mapped-ipv6
status: active
area: ipdb存储
tags: [ipdb, codec, ipv6, ipv4-mapped, family, netip, constraint]
---

## 背景

ipdb v2 codec 用 key 首字节（kind）区分 IPv4 / IPv6 family：`primaryV4/V6`、`cidrV4/V6`、`overlayV4/V6` 分别对应两套独立的 key 空间。单 IP 查询（`LookupIP`）按 family 选 ladder——IPv4 地址走 V4 ladder、IPv6 地址走 V6 ladder。

Go 的 `netip.Addr.Is6()` **包含 IPv4-mapped IPv6 地址**（`::ffff:x.x.x.x`，`Is4In6()==true`）。这类地址 `Is4()==false && Is6()==true && Is4In6()==true`，family 判定二义。

实现 `ipdb-v2-schema` 时初版只让 decode 侧拒绝 `Is4In6()`（CR-001 修复时加的 family 一致性校验），encode 侧 `Is6()` 分支仍会接住它——导致 `::ffff:0:0/96` encode 成功产出 V6 key，decode 失败，round-trip 不变量破裂（CR-004）。更深的隐患：即便修好 round-trip，若允许 IPv4-mapped IPv6 走 V6 路径，`LookupIP(ipv4Addr)` 用 V4 ladder 永远查不到被 encode 成 V6 key 的记录，是隐蔽查询 bug。

ipdb 的数据源是 ipinfo Lite CSV，网段记录要么是 `1.2.3.0/24`（IPv4）要么是 `2001:db8::/32`（真 IPv6），不会出现 `::ffff:x.x.x.x` 形式。overlay 存的也是 ipinfo 抓取的单 IP 结果，ipinfo 对 IPv4 返回 IPv4、对 IPv6 返回真 IPv6，同样不会返回 `::ffff` 形式。

## 决定

**ipdb v2 所有 codec 入口（encode 与 decode）一律拒绝 IPv4-mapped IPv6 地址（`netip.Addr.Is4In6()==true`），不区分 family 归属、不尝试规范化。**

具体约束：

1. **encode 侧**（`validatePrefixV2` + `encodeOverlayKeyV2`）：入参 `addr.Is4In6()==true` 即返回 error，不进 `Is4()`/`Is6()` 分支判断。
2. **decode 侧**（`decodePrefixV2`）：从 key 字节还原出的 addr 若 `Is4In6()==true` 视为损坏 key 返回 error（双保险——即便 key 被篡改也不放行）。
3. **一致性要求**：encode/decode 必须双向拒绝。任一方向单独拒绝都会破坏 round-trip 不变量。
4. **ipinfo Lite CSV 不会产生此类输入**——拒绝它不损失任何合法数据；未来若数据源扩展出现 `::ffff` 网段，需先重新评估此约束。

## 理由

- **family 二义性是查询正确性的真实风险**：IPv4-mapped IPv6 被 encode 成 V6 key 后，`LookupIP(ipv4Addr)` 用 V4 ladder 永远查不到它；反过来 `LookupIP(::ffff:1.2.3.4)` 走 V6 ladder 能查到，但语义上它代表 IPv4 地址——同一地址两种查询路径结果不一致，是隐蔽 bug。
- **数据源天然不产生**：ipinfo Lite CSV 与 ipinfo API 都不会返回 `::ffff` 形式，拒绝它零数据损失。
- **让 family 判定收敛**：kind 字节的 V4/V6 二分才能保持无二义，primary ↔ cidr 互相还原不变量才成立。
- **严格拒绝 > 静默规范化**：若尝试把 `::ffff:x.x.x.x` 规范化成 IPv4，要在 codec 层引入 family 转换逻辑，增加复杂度且掩盖上游数据问题；直接拒绝把异常暴露在 codec 边界。

## 考虑过的替代方案

- **decode 也接受（让 `::ffff` 完整走 V6 路径）**（已否决）：虽能恢复 round-trip 一致性，但上面说的查询二义性仍在——V6 key 存了 IPv4-mapped 地址，V4 ladder 查不到。只是把"破裂"换成"隐蔽 bug"。
- **codec 层把 `::ffff:x.x.x.x` 规范化为 IPv4**（已否决）：增加 family 转换逻辑、掩盖上游异常、与"codec 只编码不转换语义"的职责边界冲突。

## 后果

- v2 codec 对 `::ffff:x.x.x.x` 形式的输入一律返回 error，encode/decode 对称。
- builder（`ipdb-v2-base-build`）从 CSV 解析出的 prefix 若是 IPv4-mapped IPv6 会在 encode 阶段被拒，构建失败——这是期望行为（数据源不应有此形式）。
- overlay（`ipdb-overlay-store`）的 `Put` 若收到 `::ffff` 形式的单 IP 会在 `encodeOverlayKeyV2` 被拒——ipinfo 不会返回此形式，正常路径不触发。
- 此约束是 ipdb v2 系列后续 feature（base-build / query / overlay-store）的硬约束输入，它们的 encode/decode 必须沿用同一判断。

## 相关文档

- 触发 feature：`.codestable/features/2026-06-22-ipdb-v2-schema/`（CR-004 修复）
- 同批 learning（decode 边界校验坑点）：`2026-06-22-learning-codec-decode-boundary-check.md`
- 同批 decision（uint64→int 上界检查）：`2026-06-22-decision-codec-uint64-int-bound-check.md`
- attention.md 已沉淀的硬约束：`netip.PrefixFrom` 越界返回 invalid Prefix（与本约束配套——两者都是 netip API 行为陷阱）
- 架构：`../architecture/ip-lookup.md`
