---
doc_type: architecture
slug: render-output
scope: CLI 输出渲染——TTY 检测、lipgloss 彩色表格、JSON 输出、各查询类型的渲染器
summary: render 包负责所有面向用户的输出格式化，自动区分终端（彩色表格）和非终端（纯文本表格 + JSON）
status: current
last_reviewed: 2026-04-24
tags: [render, output, tty, json, lipgloss]
depends_on: []
implements: [output-formats]
---

## 1. 定位与受众

本 doc 描述输出渲染层的设计——TTY 检测策略、lipgloss 样式系统、各类查询结果的渲染接口约定。读者是需要新增渲染类型或修改输出格式的人。

读完能：知道如何新增一种查询结果的渲染、TTY 检测的判断逻辑、渲染接口的约定模式。

## 2. 结构与交互

### 文件职责

| 文件 | 职责 |
|---|---|
| `output.go` | OutputMode 定义 + 统一 JSON 输出 |
| `style.go` | lipgloss 颜色/样式 + TTY 检测 |
| `query.go` | 域名查询结果渲染 |
| `iplookup.go` | 单 IP 查询结果渲染 |
| `cidrlookup.go` | CIDR 查询结果渲染 |
| `ipmatch.go` | DNS 结果中的 IP 匹配详情渲染 |
| `ns_info.go` | NS 服务器信息渲染 |
| `provider.go` | Provider 列表与测试结果渲染 |
| `match_state.go` | HIT/MISS 状态文案复用 |

### TTY 检测

`style.go:30` 的 `isTTY` 函数判断 `io.Writer` 是否为 `*os.File` 且 `fd` 是终端：

```go
isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
```

非 `*os.File` 的 writer（如 `bytes.Buffer`）直接返回 false。

加载了 lipgloss 的样式但通过此检测决定是否使用增强渲染——若 writer 是 TTY 用 `lipgloss` 样式表格（`WriteEnhancedTable`），否则用纯文本表格（`WritePlainTable`）。表格渲染本身由 lipgloss 的 `table` 包负责。

### 输出模式

`output.go:9` 定义 `OutputMode`（`OutputText` / `OutputJSON`）。调用方（`cli-entry`）在解析 `-j` 参数后设置全局模式。各 `render.Write*` 函数只负责格式化，不判断模式——模式判断在 cli 层。

`WriteJSON` (`output.go:19`) 使用 `SetEscapeHTML(false)` 避免 `&` 等字符被 HTML 转义。

### 渲染接口约定

所有渲染目标类型通过接口暴露给渲染函数，避免渲染包直接依赖 cli 包的类型：

- `QueryAnswerSource` — 域名查询的单个 Provider 应答
- `IPMatchSource` — IP 匹配结果
- `IPLookupSource` — 单 IP 查询结果
- `CIDRLookupMatchSource` — CIDR 命中记录
- `ProviderSource` — Provider 列表项
- `TestResultSource` — 连通性测试结果

cli 包的类型实现这些接口（如 `QueryAnswer` 实现 `QueryAnswerSource`），渲染函数通过接口消费。

## 3. 数据与状态

- 渲染层完全无状态——所有函数接受 `io.Writer` + 数据源，写入后返回
- JSON 模式下错误输出到 stderr（在 cli 层处理），渲染函数不涉及 stderr
- 样式变量（`HeaderStyle`、`SuccessStyle` 等，`style.go:22-27`）是包级变量，初始化后不可变

## 4. 关键决策

暂无已归档的 decision。

- **接口隔离**：渲染包通过接口消费数据，不 import `internal/cli`。新加渲染类型需定义对应接口
- **lipgloss** 引入是为了 TTY 下的彩色表格渲染，无终端时不启用——纯文本表格协议保留

## 5. 代码锚点

| 文件 | 关键符号 | 说明 |
|---|---|---|
| `render/output.go:9` | `OutputMode` | 输出模式枚举 |
| `render/output.go:19` | `WriteJSON` | 统一 JSON 输出 |
| `render/style.go:12` | `ColorSuccess` 等 | 自适应颜色定义 |
| `render/style.go:22` | `HeaderStyle` 等 | 公共样式 |
| `render/style.go:30` | `isTTY` | TTY 检测 |
| `render/query.go` | — | 域名查询渲染 |
| `render/iplookup.go` | — | 单 IP 查询渲染 |
| `render/cidrlookup.go` | — | CIDR 查询渲染 |
| `render/ipmatch.go` | — | IP 匹配渲染 |
| `render/ns_info.go` | — | NS 信息渲染 |
| `render/provider.go` | — | Provider 渲染 |
| `render/match_state.go` | — | HIT/MISS 状态文案 |

## 6. 已知约束 / 边界情况

- 非 TTY 下 lipgloss 样式仍被加载但通过条件分支跳过——消耗 lipgloss 初始化成本，但不影响输出
- JSON 输出时 HTML 转义关闭——若输出包含 `<` `>` `&` 会原样保留
- 不提供 CSV/XML/YAML 等其他格式
- 渲染层不负责 `-j` 参数解析——那是 cli 层的职责

## 7. 相关文档

- `cli-entry.md` — 调用方，持有 outputMode 并决定调用 WriteJSON 还是 Write*
- 需求：`output-formats`
