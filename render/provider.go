package render

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// ProviderSource 描述单条 Provider 展示数据。
type ProviderSource interface {
	NameText() string
	ProtocolText() string
	EndpointText() string
	EnabledState() bool
}

// ProviderCollection 描述可渲染的 Provider 集合。
type ProviderCollection interface {
	ProviderCount() int
	ProviderAt(i int) ProviderSource
}

// TestResultSource 描述单条 Provider 测试结果。
type TestResultSource interface {
	NameText() string
	StatusText() string
	LatencyMsValue() int64
	MessageText() string
}

// TestResultCollection 描述可渲染的测试结果集合。
type TestResultCollection interface {
	ResultCount() int
	ResultAt(i int) TestResultSource
}

// WriteProviders 渲染 Provider 列表表格
func WriteProviders(w io.Writer, items ProviderCollection) {
	if items == nil {
		return
	}
	if isTTY(w) {
		writeProvidersPretty(w, items)
	} else {
		writeProvidersPlain(w, items)
	}
}

// writeProvidersPlain 纯文本表格输出（保持旧协议）
func writeProvidersPlain(w io.Writer, items ProviderCollection) {
	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	fmt.Fprintln(tw, "名称\t协议\t端点\t启用")
	fmt.Fprintln(tw, "--------\t------\t----\t----")

	for i := 0; i < items.ProviderCount(); i++ {
		p := items.ProviderAt(i)
		enabled := "是"
		if !p.EnabledState() {
			enabled = "否"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.NameText(), p.ProtocolText(), p.EndpointText(), enabled)
	}
	tw.Flush()
}

// writeProvidersPretty lipgloss 表格输出
func writeProvidersPretty(w io.Writer, items ProviderCollection) {
	var rows [][]string
	for i := 0; i < items.ProviderCount(); i++ {
		p := items.ProviderAt(i)
		enabled := "是"
		if !p.EnabledState() {
			enabled = "否"
		}
		rows = append(rows, []string{p.NameText(), p.ProtocolText(), p.EndpointText(), enabled})
	}

	t := table.New().
		Headers("名称", "协议", "端点", "启用").
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(ColorMuted)).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)

			if row == table.HeaderRow {
				return s.Bold(true).Foreground(ColorTitle)
			}

			if row < 0 || row >= len(rows) {
				return s
			}

			switch col {
			case 0: // 名称
				return s.Foreground(ColorAccent)
			case 3: // 启用
				if rows[row][3] == "是" {
					return s.Foreground(ColorSuccess)
				}
				return s.Foreground(ColorError)
			}
			return s
		})

	for _, row := range rows {
		t.Row(row...)
	}

	fmt.Fprintln(w, t.Render())
}

// WriteTestResults 渲染 Provider 测试结果表格
func WriteTestResults(w io.Writer, items TestResultCollection) {
	if items == nil {
		return
	}
	if isTTY(w) {
		writeTestResultsPretty(w, items)
	} else {
		writeTestResultsPlain(w, items)
	}
}

// writeTestResultsPlain 纯文本表格输出（保持旧协议）
func writeTestResultsPlain(w io.Writer, items TestResultCollection) {
	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	fmt.Fprintln(tw, "Provider\t状态\t延迟\t信息")
	fmt.Fprintln(tw, "--------\t------\t----\t----")

	for i := 0; i < items.ResultCount(); i++ {
		item := items.ResultAt(i)
		latency := "-"
		if item.LatencyMsValue() > 0 {
			latency = fmt.Sprintf("%dms", item.LatencyMsValue())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.NameText(), item.StatusText(), latency, item.MessageText())
	}
	tw.Flush()
}

// writeTestResultsPretty lipgloss 表格输出
func writeTestResultsPretty(w io.Writer, items TestResultCollection) {
	var rows [][]string
	for i := 0; i < items.ResultCount(); i++ {
		item := items.ResultAt(i)
		latency := "-"
		if item.LatencyMsValue() > 0 {
			latency = fmt.Sprintf("%dms", item.LatencyMsValue())
		}
		rows = append(rows, []string{item.NameText(), item.StatusText(), latency, item.MessageText()})
	}

	t := table.New().
		Headers("Provider", "状态", "延迟", "信息").
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(ColorMuted)).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)

			if row == table.HeaderRow {
				return s.Bold(true).Foreground(ColorTitle)
			}

			if row < 0 || row >= len(rows) {
				return s
			}

			switch col {
			case 0: // Provider
				return s.Foreground(ColorAccent)
			case 1: // 状态
				if rows[row][1] == "OK" {
					return s.Foreground(ColorSuccess)
				}
				return s.Foreground(ColorError)
			case 2: // 延迟
				return s.Foreground(ColorMuted)
			}
			return s
		})

	for _, row := range rows {
		t.Row(row...)
	}

	fmt.Fprintln(w, t.Render())
}
