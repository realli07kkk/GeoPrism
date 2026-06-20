---
doc_type: issue-report
issue: 2026-06-20-nondeterministic-result-order
status: confirmed
severity: P2
summary: 相同查询多次执行时，JSON 输出里 answers 数组与 providers 列表的顺序会变化，破坏 diff / 缓存 / 测试的可重复性
tags: [json, resolver, provider, determinism, output-protocol]
---

# JSON 输出顺序不稳定 Issue Report

## 1. 问题现象

对完全相同的输入多次执行命令，JSON 输出中元素的顺序会在不同次运行之间变化：

- `geoprism <domain> -j` 的 `answers[]` 数组顺序会变（哪个 Provider 排前面取决于本次谁先返回）。
- `geoprism providers -j` 的 Provider 列表、`geoprism test --all -j` 的结果列表顺序会变（取决于本次 map 遍历顺序）。

值不变，只是元素排列顺序不稳定。对人类一次性查看无碍，但对脚本 / AI agent / 测试 / 缓存 / diff 不友好——同样的查询拿到的 JSON 文本不一致，无法做稳定比对和缓存命中。

## 2. 复现步骤

### 现象 A（answers 顺序）

1. 配置至少 3 个 enabled Provider。
2. 连续多次执行 `geoprism example.com -j`。
3. 观察到：`answers[]` 里各 Provider 的相对顺序在不同次之间不一致（响应快的排前面）。

### 现象 B（providers 顺序）

1. `providers.toml` 配置多个 Provider。
2. 连续多次执行 `geoprism providers -j`（或 `geoprism test --all -j`）。
3. 观察到：输出列表顺序在不同次之间变化。

复现频率：概率性（取决于网络抖动与 map 遍历随机化），多跑几次即可观察到。

## 3. 期望 vs 实际

**期望行为**：相同输入产生稳定、可预测的输出顺序——`answers[]` 按调用方传入的 Provider 顺序排列；`providers` / `test --all` 按配置顺序排列；显式 `-p a,b,c` 按用户参数顺序排列。

**实际行为**：`answers[]` 按 goroutine 完成（响应快慢）顺序排列；`providers` / `test --all` 按 Go map 遍历顺序排列，两者都不稳定。

## 4. 环境信息

- 涉及模块 / 功能：DNS 多 Provider 并行查询（resolver）、Provider 存储（provider）、JSON 输出
- 相关文件 / 函数（线索，根因 analysis 确认）：
  - `backend/resolver/resolver.go` — `QueryMulti`（按 channel 完成顺序 `append`）
  - `backend/provider/provider.go` — `List` / `GetEnabled`（遍历 map）、`save`（当前按 ID 排序写回）
  - `internal/cli/app.go` — `QueryDomain`（消费 `GetEnabled` 的顺序）、`matchProvidersByName`
- 运行环境：本地 macOS，dev
- 其他上下文：由系统审计发现并经代码核对确认。CLAUDE.md 明确把 JSON 输出定位为供 AI agent / 脚本消费的接口，故顺序稳定性对本项目尤为重要。

## 5. 严重程度

**P2** — 不影响单次查询结果的正确性，但破坏 JSON 输出作为对外协议的可重复性（diff / 缓存 / 自动化测试），与项目"JSON 面向 AI agent 消费"的定位冲突。无数据损坏，故定 P2。

## 备注

- 修复需同时覆盖两个独立乱序源（resolver 的 append 顺序 + provider 的 map 遍历），只改一层仍会乱。
- 本 issue 只解决"顺序确定性"；JSON 顶层 `schema_version` 信封与 DNS 记录结构化字段属于"稳定 JSON 协议 v1"新能力，走独立 feat，不在本 issue 范围。
