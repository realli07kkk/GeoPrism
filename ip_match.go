package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"text/tabwriter"

	"geoprism/backend/ipdb"
	"geoprism/backend/resolver"
)

// IPMatchView 表示 DNS 查询结果里的单条 IP 匹配详情。
type IPMatchView struct {
	Provider   string `json:"provider"`
	RecordType string `json:"record_type"`
	IP         string `json:"ip"`
	Matched    bool   `json:"matched"`
	ipdb.Record
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

// writeIPMatches 输出 IP 匹配详情表格。
func writeIPMatches(output io.Writer, matches []IPMatchView) {
	if len(matches) == 0 {
		return
	}

	fmt.Fprint(output, "\nIP 匹配详情\n\n")

	w := tabwriter.NewWriter(output, 0, 0, 4, ' ', 0)
	fmt.Fprintln(w, "Provider\tType\tIP\tMatch\tNetwork\tCountry\tCountryCode\tContinent\tContinentCode\tASN\tASName\tASDomain")
	fmt.Fprintln(w, "--------\t----\t--\t-----\t-------\t-------\t-----------\t---------\t-------------\t---\t------\t--------")

	for _, match := range matches {
		matchStatus := "MISS"
		if match.Matched {
			matchStatus = "HIT"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			match.Provider,
			match.RecordType,
			match.IP,
			matchStatus,
			match.Network,
			match.Country,
			match.CountryCode,
			match.Continent,
			match.ContinentCode,
			match.ASN,
			match.ASName,
			match.ASDomain,
		)
	}
	w.Flush()
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
