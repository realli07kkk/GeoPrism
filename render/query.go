package render

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// QueryAnswerSource 描述单个 Provider 的查询结果。
type QueryAnswerSource interface {
	ProviderName() string
	SuccessState() bool
	RCodeNameText() string
	RTTMsValue() int64
	ErrorText() string
	RecordCount() int
	RecordDataAt(i int) string
	RecordTTLAt(i int) uint32
}

// QueryResultSource 描述 DNS 查询结果的渲染输入。
type QueryResultSource interface {
	DomainText() string
	RecordTypeText() string
	TotalTimeMs() int64
	AnswerCount() int
	AnswerAt(i int) QueryAnswerSource
}

// WriteQueryResult 渲染 DNS 查询结果表格
func WriteQueryResult(w io.Writer, data QueryResultSource) {
	if isTTY(w) {
		writeQueryPretty(w, data)
	} else {
		writeQueryPlain(w, data)
	}
}

// writeQueryPlain 纯文本表格输出（保持旧协议）
func writeQueryPlain(w io.Writer, data QueryResultSource) {
	fmt.Fprintf(w, "\n查询: %s (%s)\n\n", data.DomainText(), data.RecordTypeText())

	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	fmt.Fprintln(tw, "Provider\t状态\t延迟\t记录\tTTL")
	fmt.Fprintln(tw, "--------\t------\t----\t----------------\t---")

	for i := 0; i < data.AnswerCount(); i++ {
		ans := data.AnswerAt(i)
		if !ans.SuccessState() {
			errMsg := ans.ErrorText()
			if errMsg == "" {
				errMsg = ans.RCodeNameText()
			}
			fmt.Fprintf(tw, "%s\tERROR\t-\t%s\t-\n", ans.ProviderName(), errMsg)
			continue
		}

		if ans.RecordCount() == 0 {
			fmt.Fprintf(tw, "%s\t%s\t%dms\t-\t-\n",
				ans.ProviderName(), ans.RCodeNameText(), ans.RTTMsValue())
			continue
		}

		fmt.Fprintf(tw, "%s\t%s\t%dms\t%s\t%d\n",
			ans.ProviderName(), ans.RCodeNameText(), ans.RTTMsValue(),
			ans.RecordDataAt(0), ans.RecordTTLAt(0))

		for j := 1; j < ans.RecordCount(); j++ {
			fmt.Fprintf(tw, "\t\t\t%s\t%d\n", ans.RecordDataAt(j), ans.RecordTTLAt(j))
		}
	}
	tw.Flush()

	fmt.Fprintf(w, "\n总耗时: %dms\n", data.TotalTimeMs())
}

// writeQueryPretty lipgloss 表格输出
func writeQueryPretty(w io.Writer, data QueryResultSource) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, HeaderStyle.Render(fmt.Sprintf("查询: %s (%s)", data.DomainText(), data.RecordTypeText())))
	fmt.Fprintln(w)

	// 构建表格行
	var rows [][]string
	for i := 0; i < data.AnswerCount(); i++ {
		ans := data.AnswerAt(i)
		if !ans.SuccessState() {
			errMsg := ans.ErrorText()
			if errMsg == "" {
				errMsg = ans.RCodeNameText()
			}
			rows = append(rows, []string{ans.ProviderName(), "ERROR", "-", errMsg, "-"})
			continue
		}

		if ans.RecordCount() == 0 {
			rows = append(rows, []string{
				ans.ProviderName(), ans.RCodeNameText(),
				fmt.Sprintf("%dms", ans.RTTMsValue()), "-", "-",
			})
			continue
		}

		rows = append(rows, []string{
			ans.ProviderName(), ans.RCodeNameText(),
			fmt.Sprintf("%dms", ans.RTTMsValue()),
			ans.RecordDataAt(0),
			fmt.Sprintf("%d", ans.RecordTTLAt(0)),
		})

		for j := 1; j < ans.RecordCount(); j++ {
			rows = append(rows, []string{
				"", "", "", ans.RecordDataAt(j), fmt.Sprintf("%d", ans.RecordTTLAt(j)),
			})
		}
	}

	t := table.New().
		Headers("Provider", "状态", "延迟", "记录", "TTL").
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
				if rows[row][1] == "NOERROR" {
					return s.Foreground(ColorSuccess)
				}
				return s.Foreground(ColorError)
			case 2, 4: // 延迟、TTL
				return s.Foreground(ColorMuted)
			}
			return s
		})

	for _, row := range rows {
		t.Row(row...)
	}

	fmt.Fprintln(w, t.Render())
	fmt.Fprintln(w, MutedStyle.Render(fmt.Sprintf("总耗时: %dms", data.TotalTimeMs())))
}
