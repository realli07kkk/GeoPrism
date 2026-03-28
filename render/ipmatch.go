package render

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// IPMatchSource 描述单条 IP 匹配结果。
type IPMatchSource interface {
	ProviderName() string
	RecordTypeText() string
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

// IPMatchCollection 描述可渲染的 IP 匹配集合。
type IPMatchCollection interface {
	MatchCount() int
	MatchAt(i int) IPMatchSource
}

// WriteIPMatches 渲染 IP 匹配结果
func WriteIPMatches(w io.Writer, matches IPMatchCollection) {
	if matches == nil || matches.MatchCount() == 0 {
		return
	}

	if isTTY(w) {
		writeIPMatchesPretty(w, matches)
	} else {
		writeIPMatchesPlain(w, matches)
	}
}

// writeIPMatchesPlain 纯文本表格输出
func writeIPMatchesPlain(w io.Writer, matches IPMatchCollection) {
	fmt.Fprint(w, "\nIP 匹配详情\n\n")

	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
	fmt.Fprintln(tw, "Provider\tType\tIP\tMatch\tNetwork\tCountry\tCountryCode\tContinent\tContinentCode\tASN\tASName\tASDomain\tSource")
	fmt.Fprintln(tw, "--------\t----\t--\t-----\t-------\t-------\t-----------\t---------\t-------------\t---\t------\t--------\t------")

	for i := 0; i < matches.MatchCount(); i++ {
		m := matches.MatchAt(i)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			m.ProviderName(),
			m.RecordTypeText(),
			m.IPText(),
			matchStateText(m.MatchedState()),
			m.NetworkText(),
			m.CountryText(),
			m.CountryCodeText(),
			m.ContinentText(),
			m.ContinentCodeText(),
			m.ASNText(),
			m.ASNameText(),
			m.ASDomainText(),
			m.SourceText(),
		)
	}
	tw.Flush()
}

// writeIPMatchesPretty lipgloss 表格输出（保持表格语义，仅做样式增强）
func writeIPMatchesPretty(w io.Writer, matches IPMatchCollection) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, HeaderStyle.Render("IP 匹配详情"))
	var rows [][]string
	for i := 0; i < matches.MatchCount(); i++ {
		m := matches.MatchAt(i)
		rows = append(rows, []string{
			m.ProviderName(),
			m.RecordTypeText(),
			m.IPText(),
			matchStateText(m.MatchedState()),
			m.NetworkText(),
			m.CountryText(),
			m.CountryCodeText(),
			m.ContinentText(),
			m.ContinentCodeText(),
			m.ASNText(),
			m.ASNameText(),
			m.ASDomainText(),
			m.SourceText(),
		})
	}

	t := table.New().
		Headers("Provider", "Type", "IP", "Match", "Network", "Country", "CountryCode", "Continent", "ContinentCode", "ASN", "ASName", "ASDomain", "Source").
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
			case 0:
				return s.Foreground(ColorAccent)
			case 3:
				if rows[row][3] == "HIT" {
					return s.Foreground(ColorSuccess)
				}
				return s.Foreground(ColorError)
			case 2, 9, 10, 11:
				return s.Foreground(ColorMuted)
			case 12:
				src := rows[row][12]
				if src == "ipinfo" {
					return s.Foreground(ColorAccent)
				}
				return s.Foreground(ColorMuted)
			}
			return s
		})

	for _, row := range rows {
		t.Row(row...)
	}

	fmt.Fprintln(w, t.Render())
}
