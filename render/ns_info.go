package render

import (
	"fmt"
	"io"
	"reflect"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// NSIPSource 描述单个 NS IP 信息。
type NSIPSource interface {
	IPText() string
	RecordTypeText() string
	MatchedState() bool
	CountryText() string
	ASNText() string
	ASNameText() string
}

// NSServerSource 描述单个 NS 服务器信息。
type NSServerSource interface {
	NameText() string
	IPCount() int
	IPAt(i int) any // 返回 any，由渲染函数做类型断言
	HasError() bool
	ErrorText() string
}

// NSInfoSource 描述 NS 信息汇总。
type NSInfoSource interface {
	ServerCount() int
	ServerAt(i int) any // 返回 any，由渲染函数做类型断言
	QueryTimeMs() int64
	IsAvailable() bool
	ErrorText() string
}

// WriteNSInfo 渲染 NS 服务器信息。
func WriteNSInfo(w io.Writer, data NSInfoSource) {
	if isNilNSInfoSource(data) {
		return
	}

	if isTTY(w) {
		writeNSInfoPretty(w, data)
	} else {
		writeNSInfoPlain(w, data)
	}
}

// isNilNSInfoSource 判断 NS 渲染输入是否为空。
// 这里只做 data == nil 不够，因为 interface 里可能封装了 typed nil；
// 这类值在比较时不等于 nil，但继续调方法会直接 panic。
func isNilNSInfoSource(data NSInfoSource) bool {
	if data == nil {
		return true
	}

	value := reflect.ValueOf(data)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// writeNSInfoPlain 纯文本表格输出。
func writeNSInfoPlain(w io.Writer, data NSInfoSource) {
	fmt.Fprint(w, "\nNS 服务器信息\n\n")

	if !data.IsAvailable() {
		fmt.Fprintf(w, "错误: %s\n\n", data.ErrorText())
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	fmt.Fprintln(tw, "NS 名称\tIP\t类型\tMatch\t国家\tASN\tAS 名称")
	fmt.Fprintln(tw, "-------\t--\t----\t-----\t----\t---\t-------")

	for i := 0; i < data.ServerCount(); i++ {
		server := toNSServerSource(data.ServerAt(i))

		if server.HasError() {
			fmt.Fprintf(tw, "%s\tERROR\t-\t-\t-\t-\t%s\n", server.NameText(), server.ErrorText())
			continue
		}

		if server.IPCount() == 0 {
			fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t-\t无 IP\n", server.NameText())
			continue
		}

		// 第一行显示 NS 名称
		ip := toNSIPSource(server.IPAt(0))
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			server.NameText(),
			ip.IPText(),
			ip.RecordTypeText(),
			matchStateText(ip.MatchedState()),
			ip.CountryText(),
			ip.ASNText(),
			ip.ASNameText(),
		)

		// 后续行只显示 IP
		for j := 1; j < server.IPCount(); j++ {
			ip := toNSIPSource(server.IPAt(j))
			fmt.Fprintf(tw, "\t%s\t%s\t%s\t%s\t%s\t%s\n",
				ip.IPText(),
				ip.RecordTypeText(),
				matchStateText(ip.MatchedState()),
				ip.CountryText(),
				ip.ASNText(),
				ip.ASNameText(),
			)
		}
	}

	tw.Flush()
	fmt.Fprintf(w, "\nNS 查询耗时: %dms\n", data.QueryTimeMs())
}

// writeNSInfoPretty lipgloss 表格输出。
func writeNSInfoPretty(w io.Writer, data NSInfoSource) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, HeaderStyle.Render("NS 服务器信息"))

	if !data.IsAvailable() {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "错误: %s\n", data.ErrorText())
		return
	}

	// 构建表格行
	var rows [][]string
	for i := 0; i < data.ServerCount(); i++ {
		server := toNSServerSource(data.ServerAt(i))

		if server.HasError() {
			rows = append(rows, []string{server.NameText(), "-", "-", "ERROR", "-", "-", server.ErrorText()})
			continue
		}

		if server.IPCount() == 0 {
			rows = append(rows, []string{server.NameText(), "-", "-", "-", "-", "-", "无 IP"})
			continue
		}

		// 第一行显示 NS 名称
		ip := toNSIPSource(server.IPAt(0))
		rows = append(rows, []string{
			server.NameText(),
			ip.IPText(),
			ip.RecordTypeText(),
			matchStateText(ip.MatchedState()),
			ip.CountryText(),
			ip.ASNText(),
			truncateASName(ip.ASNameText()),
		})

		// 后续行只显示 IP（NS 名称留空）
		for j := 1; j < server.IPCount(); j++ {
			ip := toNSIPSource(server.IPAt(j))
			rows = append(rows, []string{
				"",
				ip.IPText(),
				ip.RecordTypeText(),
				matchStateText(ip.MatchedState()),
				ip.CountryText(),
				ip.ASNText(),
				truncateASName(ip.ASNameText()),
			})
		}
	}

	t := table.New().
		Headers("NS 名称", "IP", "类型", "Match", "国家", "ASN", "AS 名称").
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
			case 0: // NS 名称
				if rows[row][0] != "" {
					return s.Foreground(ColorAccent)
				}
			case 1: // IP
				return s.Foreground(ColorAccent)
			case 3: // Match
				if rows[row][3] == "HIT" {
					return s.Foreground(ColorSuccess)
				}
				if rows[row][3] == "MISS" {
					return s.Foreground(ColorMuted)
				}
				return s.Foreground(ColorError)
			case 5, 6: // ASN, AS 名称
				return s.Foreground(ColorMuted)
			}
			return s
		})

	for _, row := range rows {
		t.Row(row...)
	}

	fmt.Fprintln(w, t.Render())
	fmt.Fprintln(w, MutedStyle.Render(fmt.Sprintf("NS 查询耗时: %dms", data.QueryTimeMs())))
}

// toNSServerSource 类型断言辅助函数
func toNSServerSource(v any) NSServerSource {
	if s, ok := v.(NSServerSource); ok {
		return s
	}
	return nil
}

// toNSIPSource 类型断言辅助函数
func toNSIPSource(v any) NSIPSource {
	if ip, ok := v.(NSIPSource); ok {
		return ip
	}
	return nil
}

// truncateASName 截断 AS 名称，避免表格过宽
func truncateASName(name string) string {
	const maxLen = 30
	if len(name) <= maxLen {
		return name
	}
	return name[:maxLen-3] + "..."
}
