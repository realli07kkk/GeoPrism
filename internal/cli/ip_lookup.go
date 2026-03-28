package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"

	"geoprism/backend/ipdb"
	"geoprism/backend/ipinfo"
	"geoprism/render"
)

const missingIPDBErrorMessage = "未找到可用的离线 IP 库，请先执行 geoprism ipdb build --csv <absolute-path>"

// IPLookupView 表示单个 IP 的离线匹配结果。
type IPLookupView struct {
	IP            string `json:"ip"`
	Matched       bool   `json:"matched"`
	Network       string `json:"network"`
	Country       string `json:"country"`
	CountryCode   string `json:"country_code"`
	Continent     string `json:"continent"`
	ContinentCode string `json:"continent_code"`
	ASN           string `json:"asn"`
	ASName        string `json:"as_name"`
	ASDomain      string `json:"as_domain"`
	Source        string `json:"source,omitempty"` // 数据来源：ipdb / ipinfo
}

// IPText 返回 IP 地址。
func (v IPLookupView) IPText() string {
	return v.IP
}

// MatchedState 返回是否命中离线库。
func (v IPLookupView) MatchedState() bool {
	return v.Matched
}

// NetworkText 返回网段。
func (v IPLookupView) NetworkText() string {
	return v.Network
}

// CountryText 返回国家名称。
func (v IPLookupView) CountryText() string {
	return v.Country
}

// CountryCodeText 返回国家代码。
func (v IPLookupView) CountryCodeText() string {
	return v.CountryCode
}

// ContinentText 返回大洲名称。
func (v IPLookupView) ContinentText() string {
	return v.Continent
}

// ContinentCodeText 返回大洲代码。
func (v IPLookupView) ContinentCodeText() string {
	return v.ContinentCode
}

// ASNText 返回 ASN。
func (v IPLookupView) ASNText() string {
	return v.ASN
}

// ASNameText 返回 AS 名称。
func (v IPLookupView) ASNameText() string {
	return v.ASName
}

// ASDomainText 返回 AS 域名。
func (v IPLookupView) ASDomainText() string {
	return v.ASDomain
}

// SourceText 返回数据来源。
func (v IPLookupView) SourceText() string {
	return v.Source
}

// LookupIP 查询单个 IP 的信息，合并 ipdb 和 ipinfo 结果。
func (a *App) LookupIP(ip string) (IPLookupView, error) {
	if net.ParseIP(ip) == nil {
		return IPLookupView{}, fmt.Errorf("IP 格式非法: %s", ip)
	}

	// Step 1: ipdb 查询
	var ipdbMatch ipdb.Match
	hasIPDB := a.ensureIPDBStore() != nil
	if hasIPDB {
		var err error
		ipdbMatch, err = a.ipdbStore.LookupIP(ip)
		if err != nil {
			return IPLookupView{}, err
		}
	}

	// Step 2: ipinfo 查询
	var ipinfoResp *ipinfo.Response
	if a.ipinfoClient != nil {
		ipinfoResp = a.lookupIPInfoSync(ip)
	}

	// 无数据可用
	if !hasIPDB && ipinfoResp == nil {
		return IPLookupView{}, a.ipdbLookupError()
	}

	// Step 3: 合并
	merged, source := a.mergeIPInfo(ip, ipdbMatch, ipinfoResp)

	// Step 4: 回写
	if ipinfoResp != nil && a.ipdbStore != nil {
		a.maybeWriteBack(ip, ipinfoResp, ipdbMatch)
	}

	// Step 5: 构建视图
	view := newIPLookupView(merged)
	view.Source = source
	return view, nil
}

// runIPLookup 执行单个 IP 查询。
func (a *App) runIPLookup(args []string) {
	fs := flag.NewFlagSet("ip", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonFlag := fs.Bool("j", false, "JSON 格式输出")
	fs.BoolVar(jsonFlag, "json", false, "JSON 格式输出")
	// 解析失败也要遵守统一 JSON 错误协议，所以需要在 Parse 前先探测一次。
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
		a.writeError("请指定要查询的 IP 地址")
		os.Exit(1)
	}
	if fs.NArg() != 1 {
		a.writeError("IP 查询只支持单个 IP 地址")
		os.Exit(1)
	}

	result, err := a.LookupIP(fs.Arg(0))
	if err != nil {
		a.writeError(err.Error())
		os.Exit(1)
	}

	if a.outputMode == render.OutputJSON {
		render.WriteJSON(os.Stdout, result)
		return
	}

	render.WriteIPLookup(os.Stdout, result)
}

func (a *App) ipdbLookupError() error {
	if errors.Is(a.ipdbInitErr, ipdb.ErrNoCurrentDatabase) {
		return errors.New(missingIPDBErrorMessage)
	}
	if a.ipdbInitErr != nil {
		return fmt.Errorf("加载离线 IP 库失败: %w", a.ipdbInitErr)
	}
	return errors.New("离线 IP 库不可用")
}

func newIPLookupView(match ipdb.Match) IPLookupView {
	view := IPLookupView{
		IP:      match.IP,
		Matched: match.Matched,
	}
	if !match.Matched {
		return view
	}

	view.Network = match.Record.Network
	view.Country = match.Record.Country
	view.CountryCode = match.Record.CountryCode
	view.Continent = match.Record.Continent
	view.ContinentCode = match.Record.ContinentCode
	view.ASN = match.Record.ASN
	view.ASName = match.Record.ASName
	view.ASDomain = match.Record.ASDomain
	return view
}

func hasJSONFlag(args []string) bool {
	for _, arg := range args {
		if isGlobalFlag(arg) {
			return true
		}
	}
	return false
}
