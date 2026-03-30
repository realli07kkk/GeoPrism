package cli

import (
	"fmt"
	"io"
	"net"
	"os"

	"geoprism/backend/ipdb"
	"geoprism/backend/resolver"
	"geoprism/render"
)

// IPMatchView 表示 DNS 查询结果里的单条 IP 匹配详情。
type IPMatchView struct {
	Provider   string `json:"provider"`
	RecordType string `json:"record_type"`
	IP         string `json:"ip"`
	Matched    bool   `json:"matched"`
	Source     string `json:"source,omitempty"`
	ipdb.Record
}

// ProviderName 返回 Provider 名称。
func (m IPMatchView) ProviderName() string {
	return m.Provider
}

// RecordTypeText 返回记录类型。
func (m IPMatchView) RecordTypeText() string {
	return m.RecordType
}

// IPText 返回 IP 地址。
func (m IPMatchView) IPText() string {
	return m.IP
}

// MatchedState 返回是否命中离线库。
func (m IPMatchView) MatchedState() bool {
	return m.Matched
}

// NetworkText 返回网段。
func (m IPMatchView) NetworkText() string {
	return m.Network
}

// CountryText 返回国家名称。
func (m IPMatchView) CountryText() string {
	return m.Country
}

// CountryCodeText 返回国家代码。
func (m IPMatchView) CountryCodeText() string {
	return m.CountryCode
}

// ContinentText 返回大洲名称。
func (m IPMatchView) ContinentText() string {
	return m.Continent
}

// ContinentCodeText 返回大洲代码。
func (m IPMatchView) ContinentCodeText() string {
	return m.ContinentCode
}

// ASNText 返回 ASN。
func (m IPMatchView) ASNText() string {
	return m.ASN
}

// ASNameText 返回 AS 名称。
func (m IPMatchView) ASNameText() string {
	return m.ASName
}

// ASDomainText 返回 AS 域名。
func (m IPMatchView) ASDomainText() string {
	return m.ASDomain
}

// SourceText 返回数据来源。
func (m IPMatchView) SourceText() string {
	return m.Source
}

// printIPDBWarning 按需输出离线库告警。
func (a *App) printIPDBWarning() {
	if a == nil || a.ipdbWarning == "" || a.ipdbWarningShown {
		return
	}
	fmt.Fprintf(os.Stderr, "警告: %s\n", a.ipdbWarning)
	a.ipdbWarningShown = true
}

// collectIPMatches 收集所有 A/AAAA 记录的离线匹配结果。
// ipdb 未命中的 IP 会通过 ipinfo API 查询；
// ipinfo-first 模式下命中的 IP 也会调 ipinfo 并对比回写。
func (a *App) collectIPMatches(answers []QueryAnswer) []IPMatchView {
	if a == nil {
		return nil
	}

	hasIPDB := a.ensureIPDBStore() != nil
	hasIPInfo := a.hasIPInfoLookup()

	if !hasIPDB && !hasIPInfo {
		return nil
	}

	isIPInfoFirst := a.settings != nil && a.settings.IsIPInfoFirst()

	// seen 缓存已查询过的 IP 结果，避免对同一 IP 重复调用 ipinfo API
	type ipCache struct {
		match  ipdb.Match
		source string
	}
	seen := make(map[string]*ipCache)

	matches := make([]IPMatchView, 0)
	for _, answer := range answers {
		if !answer.Success {
			continue
		}

		for _, ipText := range extractAnswerIPs(answer.Answers) {
			// 命中缓存则直接复用
			if cache := seen[ipText]; cache != nil {
				matches = append(matches, IPMatchView{
					Provider:   answer.Provider,
					RecordType: answerRecordType(answer.Answers, ipText),
					IP:         cache.match.IP,
					Matched:    cache.match.Matched,
					Source:     cache.source,
					Record:     cache.match.Record,
				})
				continue
			}

			var match ipdb.Match
			var source string
			if hasIPDB {
				var err error
				match, err = a.ipdbStore.LookupIP(ipText)
				if err != nil {
					a.setIPDBWarning(fmt.Sprintf("离线 IP 匹配失败，已跳过后续匹配: %v", err))
					return matches
				}
				if match.Matched {
					source = "ipdb"
				}
			}

			// 需要调 ipinfo 的场景：ipdb 未命中，或 ipinfo-first 模式
			needIPInfo := hasIPInfo && (!match.Matched || isIPInfoFirst)
			if needIPInfo {
				if ipinfoResp := a.lookupIPInfoSync(ipText); ipinfoResp != nil {
					ipinfoRec := ipinfoResp.ToRecord()
					// ipinfo-first 或 ipdb 未命中：用 ipinfo 数据
					match = ipdb.Match{
						IP:      ipText,
						Matched: true,
						Record:  ipinfoRec,
					}
					source = "ipinfo"
					// 异步回写 ipdb
					if a.ipdbStore != nil {
						go a.maybeWriteBack(ipText, ipinfoResp, ipdb.Match{IP: ipText})
					}
				}
			}

			seen[ipText] = &ipCache{match: match, source: source}

			matches = append(matches, IPMatchView{
				Provider:   answer.Provider,
				RecordType: answerRecordType(answer.Answers, ipText),
				IP:         match.IP,
				Matched:    match.Matched,
				Source:     source,
				Record:     match.Record,
			})
		}
	}

	return matches
}

type ipMatchList []IPMatchView

// MatchCount 返回匹配数量。
func (m ipMatchList) MatchCount() int {
	return len(m)
}

// MatchAt 返回指定匹配结果。
func (m ipMatchList) MatchAt(i int) render.IPMatchSource {
	return m[i]
}

// writeIPMatches 保留旧测试入口，但不再复制展示 DTO。
func writeIPMatches(w io.Writer, matches []IPMatchView) {
	render.WriteIPMatches(w, ipMatchList(matches))
}

// extractAnswerIPs 提取所有可识别的 IP 记录。
func extractAnswerIPs(records []resolver.DNSRecord) []string {
	ips := make([]string, 0)
	for _, record := range records {
		if record.Type != string(resolver.RecordTypeA) && record.Type != string(resolver.RecordTypeAAAA) {
			continue
		}

		data := extractRecordData(record.Data)
		if net.ParseIP(data) == nil {
			continue
		}
		ips = append(ips, data)
	}
	return ips
}

// answerRecordType 获取指定 IP 对应的记录类型。
func answerRecordType(records []resolver.DNSRecord, ip string) string {
	for _, record := range records {
		if extractRecordData(record.Data) == ip {
			return record.Type
		}
	}
	return ""
}
