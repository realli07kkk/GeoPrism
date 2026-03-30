package render

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// CIDRLookupMatchSource 描述一条 CIDR 命中记录。
type CIDRLookupMatchSource interface {
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

// CIDRLookupSource 描述 CIDR 查询结果。
type CIDRLookupSource interface {
	QueryCIDRText() string
	CIDRMatchedState() bool
	CIDRMatchCountValue() int
	CIDRMatchCount() int
	CIDRMatchAt(i int) CIDRLookupMatchSource
	FallbackLookup() IPLookupSource
}

// WriteCIDRLookup 渲染 CIDR 查询结果。
func WriteCIDRLookup(w io.Writer, result CIDRLookupSource) {
	if result == nil {
		return
	}

	if isTTY(w) {
		writeCIDRLookupPretty(w, result)
	} else {
		writeCIDRLookupPlain(w, result)
	}
}

func writeCIDRLookupPlain(w io.Writer, result CIDRLookupSource) {
	fmt.Fprint(w, "\nCIDR 查询结果\n\n")
	fmt.Fprintf(w, "Query CIDR: %s\n", result.QueryCIDRText())
	fmt.Fprintf(w, "Match: %s\n", matchStateText(result.CIDRMatchedState()))
	fmt.Fprintf(w, "MatchCount: %d\n", result.CIDRMatchCountValue())

	if result.CIDRMatchCount() > 0 {
		fmt.Fprint(w, "\n命中记录\n\n")

		tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', 0)
		fmt.Fprintln(tw, "Network\tCountry\tCountryCode\tContinent\tContinentCode\tASN\tASName\tASDomain\tSource")
		fmt.Fprintln(tw, "-------\t-------\t-----------\t---------\t-------------\t---\t------\t--------\t------")
		for i := 0; i < result.CIDRMatchCount(); i++ {
			match := result.CIDRMatchAt(i)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				match.NetworkText(),
				match.CountryText(),
				match.CountryCodeText(),
				match.ContinentText(),
				match.ContinentCodeText(),
				match.ASNText(),
				match.ASNameText(),
				match.ASDomainText(),
				match.SourceText(),
			)
		}
		tw.Flush()
	}

	if fallback := result.FallbackLookup(); fallback != nil {
		fmt.Fprintf(w, "\n回退到单 IP 查询（代表 IP: %s）\n\n", fallback.IPText())
		writeCIDRFallbackPlain(w, fallback)
	}
}

func writeCIDRLookupPretty(w io.Writer, result CIDRLookupSource) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, HeaderStyle.Render("CIDR 查询结果"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, MutedStyle.Render(fmt.Sprintf("Query CIDR: %s", result.QueryCIDRText())))
	fmt.Fprintln(w, MutedStyle.Render(fmt.Sprintf("Match: %s", matchStateText(result.CIDRMatchedState()))))
	fmt.Fprintln(w, MutedStyle.Render(fmt.Sprintf("MatchCount: %d", result.CIDRMatchCountValue())))

	if result.CIDRMatchCount() > 0 {
		var rows [][]string
		for i := 0; i < result.CIDRMatchCount(); i++ {
			match := result.CIDRMatchAt(i)
			rows = append(rows, []string{
				match.NetworkText(),
				match.CountryText(),
				match.CountryCodeText(),
				match.ContinentText(),
				match.ContinentCodeText(),
				match.ASNText(),
				match.ASNameText(),
				match.ASDomainText(),
				match.SourceText(),
			})
		}

		fmt.Fprintln(w)
		fmt.Fprintln(w, HeaderStyle.Render("命中记录"))

		t := table.New().
			Headers("Network", "Country", "CountryCode", "Continent", "ContinentCode", "ASN", "ASName", "ASDomain", "Source").
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
				case 5, 6, 7:
					return s.Foreground(ColorMuted)
				case 8:
					if rows[row][8] == "ipinfo" {
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

	if fallback := result.FallbackLookup(); fallback != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, HeaderStyle.Render(fmt.Sprintf("回退到单 IP 查询（代表 IP: %s）", fallback.IPText())))
		writeCIDRFallbackPretty(w, fallback)
	}
}

func writeCIDRFallbackPlain(w io.Writer, result IPLookupSource) {
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

func writeCIDRFallbackPretty(w io.Writer, result IPLookupSource) {
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
				if row[10] == "ipinfo" {
					return s.Foreground(ColorAccent)
				}
				return s.Foreground(ColorMuted)
			}
			return s
		})

	t.Row(row...)
	fmt.Fprintln(w, t.Render())
}
