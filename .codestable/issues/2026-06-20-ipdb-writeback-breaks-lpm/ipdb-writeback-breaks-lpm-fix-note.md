---
doc_type: issue-fix-note
issue: 2026-06-20-ipdb-writeback-breaks-lpm
status: fixed
severity: P1
fixed_at: 2026-06-20
fix_scope: 紧急止血（第一层）
related: [ipdb-writeback-breaks-lpm-report.md, ipdb-writeback-breaks-lpm-analysis.md, ../../roadmap/ipdb-v2-lpm/ipdb-v2-lpm-roadmap.md]
tags: [ipdb, writeback, lookup, data-corruption, lpm, keyspace]
---

# ipdb 在线回写破坏离线库查询正确性 修复记录

## 1. 修复概述

本 issue 的修复分两层，本次 fix-note 仅记录**第一层紧急止血**；**第二层正式方案 A′**（overlay 物理隔离 + base 永久只读）由 roadmap `ipdb-v2-lpm` 承接，不在此实现。

**紧急止血做了什么**：

1. **彻底禁用所有向 base 的在线回写**——删除 `maybeWriteBack` / `recordsDiffer` / `writeIPInfoRecord` 三个函数，移除单 IP 路径和域名路径的全部回写调用。当次 ipinfo 响应仍正常用于合并输出（`mergeIPInfo` 不变），只是不再持久化进 base keyspace。
2. **v1 库提示重建**——`ensureIPDBStore` 打开 `FormatVersion < 2` 的库时设置 `ipdbWarning`；并在所有会打开 v1 库的用户入口（`runIPLookup` / `runCIDRLookup` / `runQuery`）输出结果前调用 `printIPDBWarning`，把重建提示送到 stderr。提示走 stderr，不污染 stdout 的 JSON / 文本协议。

## 2. 改动清单

| 文件 | 改动 | 说明 |
|---|---|---|
| `internal/cli/ip_merge.go` | 删除 `maybeWriteBack` / `recordsDiffer` / `writeIPInfoRecord` | 移除向 base 回写的整条链路；保留 `mergeIPInfo` / `lookupIPInfoSync`（当次查询仍用 ipinfo） |
| `internal/cli/ip_lookup.go` | 移除单 IP 路径 `LookupIP` 的 Step 4 回写调用；`runIPLookup` 成功后调用 `printIPDBWarning` | 加注释说明停用原因与正式方案去向；warning 接入 CLI 入口 |
| `internal/cli/ip_match.go` | 移除域名路径 `collectIPMatches` 的回写调用；更新过时函数注释 | 注释不再提"对比回写"，避免误导后续维护者恢复已否决的危险路径 |
| `internal/cli/cidr_lookup.go` | `runCIDRLookup` 成功后调用 `printIPDBWarning` | CIDR 快捷入口同样输出 v1 重建提示 |
| `internal/cli/app.go` | `ensureIPDBStore` 成功打开 v1 库时 `setIPDBWarning` 重建提示 | 复用既有告警机制，v2 库出现后此分支自然失效 |
| `internal/cli/issue_writeback_fix_test.go` | 新增 `TestIssueWritebackDisabled_NoBaseMutation` | 单元/集成回归：验证 base 不被污染 + v1 内部告警字段触发 |
| `internal/cli/cli_test.go` | 新增 `TestCLIV1RebuildHint` | CLI 级回归：IP / CIDR / `-j` 四种入口下 v1 提示在 stderr 可见，stdout 协议不被污染 |

## 3. 验证

### 复现步骤验证（按 report 第 2 节）

集成测试 `TestIssueWritebackDisabled_NoBaseMutation` 三个子用例全部通过：

- **单 IP 路径**：注入返回与 base 不同数据的 ipinfo mock，查询 `1.0.0.1`（命中 /24，旧逻辑会因 `recordsDiffer` 回写覆盖）和 `8.8.8.8`（未命中，旧逻辑会回写 /32），然后直接打开 base 确认：
  - `1.0.0.5` 仍命中 `1.0.0.0/24`（现象 A 不再发生——没有 /32 作为错误前驱）
  - `8.8.8.8` 仍未命中（没有新增 /32 污染 keyspace）
- **域名路径**：`collectIPMatches` 对 `1.0.0.1` + `8.8.8.8` 触发 ipinfo，打开 base 确认 `8.8.8.8` 未被持久化。
- **v1 库提示**：`ensureIPDBStore` 后 `ipdbWarning` 含"旧版离线库格式"与"ipdb build"。

### 期望行为验证（report 第 3 节）

在线回写已彻底停用，base keyspace 在运行期永不被写入；`1.0.0.11` 这类仍落在 `1.0.0.0/24` 内的 IP，离线查询继续命中 `/24`（集成测试以 `1.0.0.5` 为代表验证）。

### v1 重建提示用户可见性验证

`TestCLIV1RebuildHint`（CLI 级，编译真实二进制）覆盖四个入口：

- `geoprism 1.0.0.1`（IP 文本）：stderr 含"检测到旧版离线库格式" + "ipdb build"，stdout 仍是文本结果不含 warning
- `geoprism -j 1.0.0.1`（IP JSON）：stderr 含提示，stdout 仍是合法 JSON（`ip` 字段正确）
- `geoprism 1.0.0.0/24`（CIDR 文本）：stderr 含提示，stdout 仍是文本结果
- `geoprism -j 1.0.0.0/24`（CIDR JSON）：stderr 含提示，stdout 仍是合法 JSON（`query_cidr` 字段正确）

### 影响面回归（analysis 第 4 节）

`go test ./...` 全量通过：

```
ok  	geoprism/backend/ipdb
ok  	geoprism/backend/provider
ok  	geoprism/internal/cli
ok  	geoprism/render
```

ipinfo 在线查询能力未受影响（`mergeIPInfo` 不变，配置 `ipinfo_token` 仍可获得在线数据，只是不落盘）。

### 编译

`go build ./...` 通过。

## 4. 未覆盖 / 已知留白

- **存量已污染库无法自动修复**：现象 B（同起始地址 /32 覆盖原网段）造成的数据损坏不可逆。本次通过 v1 库重建提示引导用户手动 `ipdb build` 重建——这是 report 第 5 节"备注"明确接受的取舍（CSV 是数据源真相，重建即正确）。
- **v1 格式无法从数据本身判断是否被污染**：因此对所有 v1 库统一提示重建（而非仅提示"检测到污染"）。v2 库（`FormatVersion >= 2`）出现后此分支自然失效。
- **`backend/ipdb/store.go:84` `WriteRecord` 仍保留**：运行期已无任何调用方，但作为 base 写入 API 暂留——它是正式方案 A′ 要"删除/废弃"的目标，本次不动避免范围蔓延。

## 5. 后续（正式方案衔接）

正式方案 A′ 由 roadmap `ipdb-v2-lpm` 承接（§3-§5）：

- `ipdb-overlay-store`（roadmap 第 4 条）落地后，正式取代本 issue 第一层止血——届时在此 fix-note 追加"已被 A′ 取代"标注（roadmap §7 已约定）。
- A′ 落地后可恢复"ipinfo 在线数据持久化"能力（写入独立 `overlay/db`，不污染 base）。

## 6. 文件索引

- 问题报告：`ipdb-writeback-breaks-lpm-report.md`
- 根因分析：`ipdb-writeback-breaks-lpm-analysis.md`
- 正式方案：`../../roadmap/ipdb-v2-lpm/ipdb-v2-lpm-roadmap.md`
