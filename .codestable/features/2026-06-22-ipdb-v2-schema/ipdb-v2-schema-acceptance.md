---
doc_type: feature-acceptance
feature: 2026-06-22-ipdb-v2-schema
status: passed
summary: ipdb v2 schema/codec 层验收通过；v1 零 diff、24 条验收场景全有证据、挂载点无漏记；架构归并/req 回写跳过，roadmap 已回写 done
tags: [ipdb, codec, v2, schema, acceptance]
---

# ipdb-v2-schema 验收报告

> 阶段：阶段 3（验收闭环）
> 验收日期：2026-06-22
> 关联方案 doc：`.codestable/features/2026-06-22-ipdb-v2-schema/ipdb-v2-schema-design.md`

## 1. 接口契约核对

对照方案第 2.1 节名词层逐一核查。

**接口示例逐项核对**：
- [x] key round-trip 示例（`encodePrimaryKeyV2(10.1.0.0/16)` → `[0x14][0x10][0a 01 00 00]`）→ 代码 `codec.go:232` `encodePrimaryKeyV2` 实际行为一致；`TestPrimaryKeyAndCIDRKeyByteLayoutDiffer` 断言字节布局
- [x] base value round-trip 示例（Network 不进 value）→ `codec.go:482/489` 一致；`TestBaseRecordValueV2RoundTripFull` 断言 `decoded.Network == ""`
- [x] overlay value round-trip 示例（expiresAtUnix==0 永不过期）→ `codec.go:510/529` 一致；`TestOverlayRecordValueV1NeverExpires` 校验磁盘字节=0 + decode 还原零值
- [x] 错误路径示例（未 Masked / 越界 prefixLen / version 不符 / flags 非零）→ 各有专门 negative test

**名词层"现状 → 变化"逐项核对**：
- [x] kind 字节常量（`keyKind*` 6 个）→ `types.go:22-27` 一致，数值与 v1 `keyFamily*`（0x04/0x06）不重叠
- [x] `SchemaFeatures` 类型 + 3 capability 常量 → `types.go:32-41` 一致
- [x] `Metadata.SchemaFeatures` 字段（`schema_features,omitempty`）→ `types.go:74-77` 一致
- [x] `OverlayMeta` 类型 → `types.go:89` 一致
- [x] 10 个 codec 函数（6 key + 4 value）→ `codec.go` 全部落点，签名与 design 一致
- [x] **不改**：`Record`/`Match`/`Metadata` 已有字段、v1 全部 6 个 codec 函数、`currentFormatVersion` — git diff 确认 v1 零改动

**流程图核对**（第 2.2 节"三套 key + 两套 value 协议关系"图）：
- [x] primary key → base value、overlay key → overlay value、cidr key（零长度 value）→ 回查 primary：图中关系在代码均有落点（cidr value 零长度由 `encodeCIDRKeyV2` 只产 key 体现，回查逻辑留给后续 feature）

**结论**：接口契约无偏差。

## 2. 行为与决策核对

对照方案第 1 节 + 第 2.2 节。

**需求摘要逐项验证**：
- [x] v2 key/value 编解码协议就绪 → 10 个 codec 函数全部落地
- [x] v1 codec 全量回归保护 → `TestEncodePrefixKeyRoundTrip`/`TestEncodeRecordValueRoundTrip`/`TestEncodeRecordValueWithEmptyFields` PASS
- [x] capability 位 + 两套 value 协议定义就绪 → `SchemaFeatures` + base value v2 + overlay value v1 全部可被下游调用

**明确不做逐项核对**（反向）：
- [x] 不改 `currentFormatVersion` → `grep` 确认 `types.go:10` 仍为 `byte = 1`
- [x] 不写存储/查询逻辑 → `git diff backend/ipdb/builder.go backend/ipdb/store.go` 为空
- [x] 不定义 `OverlayMetadata` → grep 全仓无此类型
- [x] 不实现 TTL 默认值 → codec 函数无 TTL 常量
- [x] 不定义 4 个后续 error → grep 确认 `ErrDuplicatePrefix`/`ErrCorruptIndex`/`ErrLegacyFormat`/`ErrIncompleteSchema` 全仓无定义

**关键决策落地**：
- [x] D1 key 编进 prefixLen → primary key `[kind][prefixLen][addr]`，`TestPrimaryKeyAndCIDRKeyByteLayoutDiffer` 验证
- [x] D2 cidr 零长度 value → `encodeCIDRKeyV2` 只产 key 无 value 参数
- [x] D3 base/overlay value 两套独立协议 → 各自 version 常量（baseValueVersionV2=2 / overlayValueVersionV1=1）
- [x] D4 v1/v2 codec 并存 → v1 函数签名零 diff，v2 函数显式 `…V2` 后缀
- [x] D5 encode 入参必须 Masked → `validatePrefixV2` 校验
- [x] D6 decode 校验 flags==0 → `decodeBaseRecordValueV2`/`decodeOverlayRecordValueV1` 校验
- [x] D7 Record.Network 不进 value → decode 返回空 Network（回填责任留 Store）

**流程级约束核对**：
- [x] encode Masked 前置 → `validatePrefixV2`
- [x] prefixLen 边界（含 Bits==-1 识别）→ `validatePrefixV2` + `decodePrefixV2`
- [x] key 长度严格 → 各 decode 函数长度校验
- [x] value version/flags 校验 → 两个 value decode 函数
- [x] value 尾部严格（多余字节 error）→ offset != len(value) 判断
- [x] primary ↔ cidr 互相还原不变量 → `TestPrimaryCIDRMutualRoundTrip`
- [x] Record.Network 回填责任 → decode 返回空
- [x] expiresAtUnix==0 永不过期 → `TestOverlayRecordValueV1NeverExpires` 校验不用 `time.Time{}.Unix()`

**挂载点反向核对（可卸载性）**——对照第 2.3 节：
- [x] design 结论"本 feature 不引入新挂入点" → **grep 反向核查通过**：所有 v2 符号（10 codec 函数 + `keyKind*` + `SchemaFeatures` + `OverlayMeta`）引用全部落在 `backend/ipdb/codec.go`（定义 + 内部 helper 互调）+ `codec_v2_test.go`/`types_v2_test.go`（测试）。**无任何外部调用方**（builder.go/store.go 零引用）。
- [x] **拔除沙盘推演**：删 `codec.go` v2 段 + `types.go` v2 类型/常量 + 两个测试文件 → v1 完全不受影响，卸载干净无残留

**结论**：行为与决策无偏差，挂载点无漏记。

## 3. 验收场景核对

对照方案第 3 节 24 条关键场景，逐条可观察证据（含 review 补的 CR-1/2/3/4 测试）：

- [x] **S1** v1 codec 全量回归 → 单测 `TestEncodePrefixKeyRoundTrip` 等 3 个 PASS
- [x] **S2-3** primary/cidr key round-trip V4/V6 边界 → `TestEncodePrimaryKeyV2RoundTrip`/`TestEncodeCIDRKeyV2RoundTrip`
- [x] **S4** overlay key round-trip → `TestEncodeOverlayKeyV2RoundTrip`
- [x] **S5** primary↔cidr 互相还原 → `TestPrimaryCIDRMutualRoundTrip`
- [x] **S6** base value round-trip 全/空字段 → `TestBaseRecordValueV2RoundTripFull`/`...EmptyFields`
- [x] **S7** decode Network=="" → 上两测试显式断言
- [x] **S8** overlay value round-trip OverlayMeta → `TestOverlayRecordValueV1RoundTrip`
- [x] **S9** expiresAtUnix==0 永不过期 → `TestOverlayRecordValueV1NeverExpires`
- [x] **S10-11** /0、/32、/128 边界 → 含在 round-trip prefixes 集合 + `TestDecodeV2KeyAcceptsValidMaskedPrefixAllBits`
- [x] **S12** overlay 全 7 字段为空 → `TestOverlayRecordValueV1AllEmptyFields`（CR-003 补）
- [x] **S13** encode 未 Masked → `TestEncodePrimaryKeyV2NotMasked`
- [x] **S14** prefixLen 越界（Bits==-1）→ `TestEncodePrefixV2InvalidPrefixLen` + `TestDecodeV2KeyRejectsInvalidPrefixLen`（CR-001 补 decode 侧）
- [x] **S15** decode key 长度非法 → `TestDecodeV2KeyWrongLength`/`TestDecodeV2KeyEmpty`
- [x] **S16** kind 未知 → `TestDecodeV2KeyUnknownKind`
- [x] **S17** base value version≠2 → `TestBaseRecordValueV2VersionMismatch`
- [x] **S18** base value flags≠0 → `TestBaseRecordValueV2FlagsNonZero`
- [x] **S19** base value uvarint 截断 → `TestBaseRecordValueV2TruncatedUvarint`
- [x] **S20** base value 多余尾部 → `TestBaseRecordValueV2ExtraTrailingBytes`
- [x] **S21** overlay version≠1 → `TestOverlayRecordValueV1VersionMismatch`
- [x] **S22** overlay flags非0/截断/尾部 → `TestOverlayRecordValueV1FlagsNonZero`/`...Truncated`/`...ExtraTrailingBytes`
- [x] **S23** capability 位组合 → `TestSchemaFeaturesCapabilityBits`
- [x] **S24** schema_features JSON tag → `TestMetadataSchemaFeaturesJSONOmitEmpty` + `TestMetadataV1RegressionNoSchemaFeatures`

**额外覆盖**（review 补强，超出原 24 条）：
- [x] decode 非 masked addr → `TestDecodeV2KeyRejectsNonMaskedAddr`（CR-001）
- [x] 超大长度字段防溢出 → `TestBaseRecordValueV2OverlargeFieldLen`/`TestOverlayRecordValueV1OverlargeSourceLen`（CR-002）
- [x] IPv4-mapped IPv6 拒绝 → `TestEncodeV2RejectsIPv4MappedIPv6`（CR-004）

**前端浏览器验证**：不适用（纯后端 codec 层，无 UI）。

**结论**：24 条场景全部有可观察证据 + 4 条 review 补强。`go test ./backend/ipdb/...` 42 个测试全 PASS，0 失败。

## 4. 术语一致性

对照方案第 0 节 + 第 2.1 节命名 grep 代码：

- `keyKind*`（6 个）→ 代码命中全部一致，与 v1 `keyFamily*` 语义区分清晰 ✓
- `SchemaFeatures` / `SchemaFeature*`（3 capability）→ 命名一致 ✓
- `OverlayMeta` → 与未来 `OverlayMetadata`（库级元信息）注释明确区分（`types.go:88`）✓
- 10 个 codec 函数命名（`…V2` / `…V1` 后缀）→ 全部一致 ✓
- 防冲突 grep（启动检查做过）：新符号仅在 roadmap/decision/feature 文档 + 本次代码出现，无冲突 ✓

**结论**：术语无不一致。

## 5. 架构归并

对照方案第 4 节。逐项评估三个归并维度：

- [x] **名词归并** → 不需要。v2 kind 字节/SchemaFeatures/OverlayMeta/10 codec 函数是模块内部协议细节，**无外部调用方**（builder/store 零引用）。`ip-lookup.md` §3"Key 编码"当前是 v1 描述（0x04/0x06），若现在写入 v2 的 0x14/0x16 等，会描述"系统里尚不存在的查询路径"，反而误导。归并时机是 `ipdb-v2-query` 收口切换公开入口时。
- [x] **动词骨架归并** → 不需要。纯编码层无跨模块主流程（无 workflow）。
- [x] **流程级约束归并** → 不需要。v2 codec 的流程级约束（Masked 前置/零长度 value/Network 回填等）已写在 roadmap §4.1-4.2（权威契约），arch 归并会重复。等 query 收口随查询路径一起进 arch"已知约束"更合适。

**判据核对**：没读过 design 的人打开 architecture 能否知道"系统里有这个能力"？——目前**不应**知道（v2 对用户/系统不可见，是未激活的地基）。归并跳过符合方案第 4 节判断，与 roadmap §8 观察项一致（"留给 ipdb-lookup-integration 的 cs-feat-accept 统一回写"）。

`attention.md` 已在 implement 阶段经 cs-note 补入 `netip.PrefixFrom` 行为坑（"命令与脚本陷阱"分节），本验收无新规约需补。

## 6. requirement 回写

方案 frontmatter `requirement: offline-ip-lookup`（status: current）。

本 feature 是 v2 存储重构地基，纯内部 codec 层，对 req 的用户故事/边界**零影响**——用户视角的"离线查 IP 归属/CIDR 相交"行为完全没变（v2 尚未接入查询路径）。

**结论**：req-offline-ip-lookup 未变，无需 update。

## 7. roadmap 回写

方案 frontmatter `roadmap: ipdb-v2-lpm` / `roadmap_item: ipdb-v2-schema`。

- [x] `ipdb-v2-lpm-items.yaml`：`ipdb-v2-schema` 条目 `status: in-progress → done`，`feature: 2026-06-22-ipdb-v2-schema` 保持，`validate-yaml.py` 校验通过
- [x] `ipdb-v2-lpm-roadmap.md` 第 5 节子 feature 清单第 1 条：`状态：planned → done`、`对应 feature：未启动 → 2026-06-22-ipdb-v2-schema`

两份已同步一致。

## 8. attention.md 候选盘点

回看本次实现暴露的"每个 feature 都会撞一次"的环境/工具/工作流坑：

- `netip.PrefixFrom(addr, N>maxBits)` 返回 `Bits()==-1` 的 invalid Prefix 而非 panic → **已在 implement 阶段经 cs-note 写入** attention.md"命令与脚本陷阱"分节（来源 CR-001）

**结论**：本 feature 无新候选（唯一候选已落地）。

## 9. 遗留

**后续优化点**（已记录，不在本 feature 范围）：
- `backend/ipdb/codec.go` 现 530 行（>500）。design 第 2.5 节"超出范围的观察"已预案：建议后续走 `cs-refactor` 把 v2 codec 拆到 `codec_v2.go`（纯文件移动 + import 不变，编译器绿灯）。v1/v2 并存是过渡态，不构成稳定 convention，不在本 feature 沉淀。

**顺手发现**（implement 阶段记录，待后续 issue）：
- `backend/ipdb/types.go:13` `metadataKeyName = "meta"` 是既存死代码：grep 全仓仅定义处无引用；实际 metadata key 在 `codec.go:9` 用字面量构造。非本次改动造成。
- `render/style.go` 既存 gofmt 问题：`gofmt -l` 列出该文件，git diff 确认非本次改动。

**已知限制**：
- v2 codec 尚无调用方，对用户/系统不可见。需 `ipdb-v2-base-build`（依赖本 feature）接入 builder、`ipdb-v2-query` 收口切换公开入口后，v2 才端到端生效。
