package render

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// IPLookupSource 描述单个 IP 查询结果。
type IPLookupSource interface {
	IPText() string
	MatchedState() bool
	NetworkText() string
	CountryText() string
	CountryCodeText() string
	ContinentText() string
	ContinentCodeText() string
	ASNText() string
	ASNameText() string
	ASDomainText() string
	SourceText() string
}

// WriteIPLookup 渲染单个 IP 查询结果。
func WriteIPLookup(w io.Writer, result IPLookupSource) {
	if result == nil {
		return
	}

	if isTTY(w) {
		writeIPLookupPretty(w, result)
	} else {
		writeIPLookupPlain(w, result)
	}
}

// writeIPLookupPlain 纯文本表格输出。
func writeIPLookupPlain(w io.Writer, result IPLookupSource) {
	fmt.Fprint(w, "\nIP 查询结果\n\n")

	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	fmt.Fprintln(tw, "IP\tMatch\tNetwork\tCountry\tCountryCode\tContinent\tContinentCode\tASN\tASName\tASDomain\tSource")
	fmt.Fprintln(tw, "--\t-----\t-------\t-------\t-----------\t---------\t-------------\t---\t------\t--------\t------")
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		result.IPText(),
		matchStateText(result.MatchedState()),
		result.NetworkText(),
		result.CountryText(),
		result.CountryCodeText(),
		result.ContinentText(),
		result.ContinentCodeText(),
		result.ASNText(),
		result.ASNameText(),
		result.ASDomainText(),
		result.SourceText(),
	)
	tw.Flush()
}

// writeIPLookupPretty lipgloss 表格输出。
func writeIPLookupPretty(w io.Writer, result IPLookupSource) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, HeaderStyle.Render("IP 查询结果"))

	row := []string{
		result.IPText(),
		matchStateText(result.MatchedState()),
		result.NetworkText(),
		result.CountryText(),
		result.CountryCodeText(),
		result.ContinentText(),
		result.ContinentCodeText(),
		result.ASNText(),
		result.ASNameText(),
		result.ASDomainText(),
		result.SourceText(),
	}

	t := table.New().
		Headers("IP", "Match", "Network", "Country", "CountryCode", "Continent", "ContinentCode", "ASN", "ASName", "ASDomain", "Source").
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(ColorMuted)).
		StyleFunc(func(rowIndex, col int) lipgloss.Style {
			s := lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)

			if rowIndex == table.HeaderRow {
				return s.Bold(true).Foreground(ColorTitle)
			}

			switch col {
			case 0:
				return s.Foreground(ColorAccent)
			case 1:
				if row[1] == "HIT" {
					return s.Foreground(ColorSuccess)
				}
				return s.Foreground(ColorError)
			case 7, 8, 9:
				return s.Foreground(ColorMuted)
			case 10:
				// Source 列用特殊颜色区分来源
				src := row[10]
				if src == "ipinfo" {
					return s.Foreground(ColorAccent)
				}
				return s.Foreground(ColorMuted)
			}
			return s
		})

	t.Row(row...)
	fmt.Fprintln(w, t.Render())
}
