package main

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

// printIPDBWarning 按需输出离线库告警。
func (a *App) printIPDBWarning() {
	if a == nil || a.ipdbWarning == "" || a.ipdbWarningShown {
		return
	}
	fmt.Fprintf(os.Stderr, "警告: %s\n", a.ipdbWarning)
	a.ipdbWarningShown = true
}

// collectIPMatches 收集所有 A/AAAA 记录的离线匹配结果。
func (a *App) collectIPMatches(answers []QueryAnswer) []IPMatchView {
	if a == nil || a.ensureIPDBStore() == nil {
		return nil
	}

	matches := make([]IPMatchView, 0)
	for _, answer := range answers {
		if !answer.Success {
			continue
		}

		for _, ipText := range extractAnswerIPs(answer.Answers) {
			match, err := a.ipdbStore.LookupIP(ipText)
			if err != nil {
				a.setIPDBWarning(fmt.Sprintf("离线 IP 匹配失败，已跳过后续匹配: %v", err))
				return matches
			}

			matches = append(matches, IPMatchView{
				Provider:   answer.Provider,
				RecordType: answerRecordType(answer.Answers, ipText),
				IP:         match.IP,
				Matched:    match.Matched,
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
