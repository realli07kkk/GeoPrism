package cli

import (
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"

	"geoprism/backend/ipdb"
	"geoprism/render"
)

// CIDRLookupView 表示单次 CIDR 查询结果。
type CIDRLookupView struct {
	QueryCIDR  string          `json:"query_cidr"`
	Matched    bool            `json:"matched"`
	MatchCount int             `json:"match_count"`
	Matches    []CIDRMatchView `json:"matches"`
	Fallback   *IPLookupView   `json:"fallback,omitempty"`
}

// QueryCIDRText 返回查询 CIDR。
func (v CIDRLookupView) QueryCIDRText() string {
	return v.QueryCIDR
}

// CIDRMatchedState 返回是否命中离线 CIDR 记录。
func (v CIDRLookupView) CIDRMatchedState() bool {
	return v.Matched
}

// CIDRMatchCountValue 返回命中数量。
func (v CIDRLookupView) CIDRMatchCountValue() int {
	return v.MatchCount
}

// CIDRMatchCount 返回可渲染的命中数量。
func (v CIDRLookupView) CIDRMatchCount() int {
	return len(v.Matches)
}

// CIDRMatchAt 返回指定命中结果。
func (v CIDRLookupView) CIDRMatchAt(i int) render.CIDRLookupMatchSource {
	return v.Matches[i]
}

// FallbackLookup 返回回退后的单 IP 查询结果。
func (v CIDRLookupView) FallbackLookup() render.IPLookupSource {
	if v.Fallback == nil {
		return nil
	}
	return v.Fallback
}

// CIDRMatchView 表示一条 CIDR 命中记录。
type CIDRMatchView struct {
	Source string `json:"source,omitempty"`
	ipdb.Record
}

// NetworkText 返回网段。
func (v CIDRMatchView) NetworkText() string {
	return v.Network
}

// CountryText 返回国家名称。
func (v CIDRMatchView) CountryText() string {
	return v.Country
}

// CountryCodeText 返回国家代码。
func (v CIDRMatchView) CountryCodeText() string {
	return v.CountryCode
}

// ContinentText 返回大洲名称。
func (v CIDRMatchView) ContinentText() string {
	return v.Continent
}

// ContinentCodeText 返回大洲代码。
func (v CIDRMatchView) ContinentCodeText() string {
	return v.ContinentCode
}

// ASNText 返回 ASN。
func (v CIDRMatchView) ASNText() string {
	return v.ASN
}

// ASNameText 返回 AS 名称。
func (v CIDRMatchView) ASNameText() string {
	return v.ASName
}

// ASDomainText 返回 AS 域名。
func (v CIDRMatchView) ASDomainText() string {
	return v.ASDomain
}

// SourceText 返回数据来源。
func (v CIDRMatchView) SourceText() string {
	return v.Source
}

// LookupCIDR 查询单个 CIDR 的信息。
func (a *App) LookupCIDR(cidr string) (CIDRLookupView, error) {
	queryPrefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return CIDRLookupView{}, fmt.Errorf("CIDR 格式非法: %s", cidr)
	}
	queryPrefix = queryPrefix.Masked()

	result := CIDRLookupView{
		QueryCIDR: queryPrefix.String(),
		Matches:   make([]CIDRMatchView, 0),
	}

	hasIPDB := a.ensureIPDBStore() != nil
	if hasIPDB {
		records, err := a.ipdbStore.LookupCIDR(queryPrefix.String())
		if err != nil {
			return CIDRLookupView{}, err
		}
		if len(records) > 0 {
			result.Matched = true
			result.MatchCount = len(records)
			result.Matches = make([]CIDRMatchView, 0, len(records))
			for _, record := range records {
				result.Matches = append(result.Matches, CIDRMatchView{
					Source: "ipdb",
					Record: record,
				})
			}
			return result, nil
		}
	}

	fallback, err := a.LookupIP(queryPrefix.Addr().String())
	if err != nil {
		return CIDRLookupView{}, err
	}
	result.Fallback = &fallback
	return result, nil
}

// runCIDRLookup 执行单个 CIDR 查询。
func (a *App) runCIDRLookup(args []string) {
	fs := flag.NewFlagSet("cidr", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("j", false, "JSON 格式输出")
	fs.BoolVar(jsonFlag, "json", false, "JSON 格式输出")
	if hasJSONFlag(args) {
		a.outputMode = render.OutputJSON
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		a.writeError(err.Error())
		os.Exit(2)
	}

	if *jsonFlag {
		a.outputMode = render.OutputJSON
	}

	if fs.NArg() == 0 {
		a.writeError("请指定要查询的 CIDR")
		os.Exit(1)
	}
	if fs.NArg() != 1 {
		a.writeError("CIDR 查询只支持单个 CIDR")
		os.Exit(1)
	}

	result, err := a.LookupCIDR(fs.Arg(0))
	if err != nil {
		a.writeError(err.Error())
		os.Exit(1)
	}

	// 查询成功后、输出结果前打印离线库告警（如 v1 库重建提示）。
	// 走 stderr，不污染 stdout 的 JSON / 文本协议。
	a.printIPDBWarning()

	if a.outputMode == render.OutputJSON {
		render.WriteJSON(os.Stdout, result)
		return
	}

	render.WriteCIDRLookup(os.Stdout, result)
}
