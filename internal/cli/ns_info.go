package cli

import (
	"sort"
	"sync"
	"time"

	"geoprism/backend/ipdb"
	"geoprism/backend/resolver"
)

// NSRecordView 单个 NS 服务器信息
type NSRecordView struct {
	Name      string     `json:"name"`            // NS 名称
	IPs       []NSIPInfo `json:"ips"`             // IP 地址列表
	QueryTime int64      `json:"query_time_ms"`   // 查询耗时
	Error     string     `json:"error,omitempty"` // 错误信息
}

// NSIPInfo NS 服务器的 IP 信息
type NSIPInfo struct {
	IP         string `json:"ip"`
	RecordType string `json:"record_type"` // A 或 AAAA
	Matched    bool   `json:"matched"`
	ipdb.Record
}

// NSInfoView NS 信息汇总视图
type NSInfoView struct {
	Servers   []NSRecordView `json:"servers"`
	QueryTime int64          `json:"query_time_ms"`
	Available bool           `json:"available"`
	Error     string         `json:"error,omitempty"`
}

// NSInfoSource 接口实现：用于渲染

// ServerCount 返回 NS 服务器数量
func (v NSInfoView) ServerCount() int {
	return len(v.Servers)
}

// ServerAt 返回指定 NS 服务器
func (v NSInfoView) ServerAt(i int) any {
	return v.Servers[i]
}

// QueryTimeMs 返回查询耗时
func (v NSInfoView) QueryTimeMs() int64 {
	return v.QueryTime
}

// IsAvailable 返回是否可用
func (v NSInfoView) IsAvailable() bool {
	return v.Available
}

// ErrorText 返回错误信息
func (v NSInfoView) ErrorText() string {
	return v.Error
}

// NSServerSource 接口实现

// NameText 返回 NS 名称
func (v NSRecordView) NameText() string {
	return v.Name
}

// IPCount 返回 IP 数量
func (v NSRecordView) IPCount() int {
	return len(v.IPs)
}

// IPAt 返回指定 IP
func (v NSRecordView) IPAt(i int) any {
	return v.IPs[i]
}

// HasError 返回是否有错误
func (v NSRecordView) HasError() bool {
	return v.Error != ""
}

// ErrorText 返回错误信息
func (v NSRecordView) ErrorText() string {
	return v.Error
}

// NSIPSource 接口实现

// IPText 返回 IP 地址
func (i NSIPInfo) IPText() string {
	return i.IP
}

// RecordTypeText 返回记录类型
func (i NSIPInfo) RecordTypeText() string {
	return i.RecordType
}

// MatchedState 返回是否匹配
func (i NSIPInfo) MatchedState() bool {
	return i.Matched
}

// CountryText 返回国家
func (i NSIPInfo) CountryText() string {
	return i.Country
}

// ASNText 返回 ASN
func (i NSIPInfo) ASNText() string {
	return i.ASN
}

// ASNameText 返回 AS 名称
func (i NSIPInfo) ASNameText() string {
	return i.ASName
}

// queryNSInfo 查询域名的 NS 服务器信息
func (a *App) queryNSInfo(domain string, providers []resolver.ProviderInfo, timeout int) NSInfoView {
	start := time.Now()

	result := NSInfoView{}

	// 在串行路径上一次性初始化离线库，避免并发 data race
	ipdbStore := a.ensureIPDBStore()

	// 1. 查询 NS 记录
	nsAnswer := a.resolver.QueryMulti(providers, resolver.DNSQuery{
		Domain:     domain,
		RecordType: resolver.RecordTypeNS,
		Timeout:    timeout,
	})

	// 提取 NS 名称（去重并排序）
	nsNames := extractNSNames(nsAnswer.Answers)
	if len(nsNames) == 0 {
		// 收集所有错误，按优先级选择一个确定的错误
		result.Error = selectNSQueryError(nsAnswer.Answers)
		result.Available = false
		result.QueryTime = time.Since(start).Milliseconds()
		return result
	}

	// 2. 并行查询每个 NS 的 A/AAAA 记录，传递稳定的 store 引用
	result.Servers = a.queryNSIPs(nsNames, providers, timeout, ipdbStore)
	result.Available = true
	result.QueryTime = time.Since(start).Milliseconds()

	return result
}

// selectNSQueryError 从 NS 查询结果中按优先级选择一个确定的错误
// 优先级：NXDOMAIN > SERVFAIL/REFUSED > 其他 ERROR > "未找到 NS 记录"
func selectNSQueryError(answers []resolver.DNSAnswer) string {
	// 收集所有错误类型
	var hasNXDOMAIN, hasSERVFAIL, hasREFUSED bool
	var otherErrors []string

	for _, ans := range answers {
		if ans.Success {
			continue
		}
		switch ans.RCodeName {
		case "NXDOMAIN":
			hasNXDOMAIN = true
		case "SERVFAIL":
			hasSERVFAIL = true
		case "REFUSED":
			hasREFUSED = true
		default:
			if ans.Error != "" {
				otherErrors = append(otherErrors, ans.Error)
			} else if ans.RCodeName != "" {
				otherErrors = append(otherErrors, ans.RCodeName)
			}
		}
	}

	// 按优先级返回错误
	if hasNXDOMAIN {
		return "域名不存在 (NXDOMAIN)"
	}
	if hasSERVFAIL {
		return "服务器错误 (SERVFAIL)"
	}
	if hasREFUSED {
		return "查询被拒绝 (REFUSED)"
	}
	if len(otherErrors) > 0 {
		// 去重并排序，取第一个
		sort.Strings(otherErrors)
		return otherErrors[0]
	}

	return "未找到 NS 记录"
}

// extractNSNames 从 NS 查询结果中提取 NS 名称（去重并按名称排序）
func extractNSNames(answers []resolver.DNSAnswer) []string {
	seen := make(map[string]bool)
	var names []string

	for _, ans := range answers {
		if !ans.Success {
			continue
		}
		for _, record := range ans.Answers {
			if record.Type == "NS" {
				nsName := extractRecordData(record.Data)
				if nsName != "" && !seen[nsName] {
					seen[nsName] = true
					names = append(names, nsName)
				}
			}
		}
	}

	// 按 NS 名称排序，保证输出稳定
	sort.Strings(names)
	return names
}

// queryNSIPs 并行查询多个 NS 服务器的 IP
func (a *App) queryNSIPs(nsNames []string, providers []resolver.ProviderInfo, timeout int, ipdbStore *ipdb.Store) []NSRecordView {
	var wg sync.WaitGroup
	results := make([]NSRecordView, len(nsNames))

	for i, name := range nsNames {
		wg.Add(1)
		go func(idx int, nsName string) {
			defer wg.Done()
			results[idx] = a.querySingleNS(nsName, providers, timeout, ipdbStore)
		}(i, name)
	}

	wg.Wait()

	// 结果已按 nsNames 顺序填充（nsNames 已排序），无需再次排序
	return results
}

// querySingleNS 查询单个 NS 服务器的 A/AAAA 记录
func (a *App) querySingleNS(nsName string, providers []resolver.ProviderInfo, timeout int, ipdbStore *ipdb.Store) NSRecordView {
	start := time.Now()
	result := NSRecordView{
		Name: nsName,
		IPs:  make([]NSIPInfo, 0),
	}

	// 并行查询 A 和 AAAA 记录
	var wg sync.WaitGroup
	var mu sync.Mutex

	// 收集错误信息（保留 RCodeName 级别）
	var queryErrors []string

	queryTypes := []resolver.RecordType{resolver.RecordTypeA, resolver.RecordTypeAAAA}

	for _, rt := range queryTypes {
		wg.Add(1)
		go func(recordType resolver.RecordType) {
			defer wg.Done()

			ans := a.resolver.QueryMulti(providers, resolver.DNSQuery{
				Domain:     nsName,
				RecordType: recordType,
				Timeout:    timeout,
			})

			// 合并所有成功 provider 的 IP 并去重
			seenIPs := make(map[string]bool)

			for _, answer := range ans.Answers {
				if !answer.Success {
					// 保留详细的失败信息
					var errMsg string
					if answer.Error != "" {
						errMsg = answer.Error
					} else {
						errMsg = answer.RCodeName
					}
					if errMsg != "" {
						mu.Lock()
						queryErrors = append(queryErrors, errMsg)
						mu.Unlock()
					}
					continue
				}

				for _, record := range answer.Answers {
					ip := extractRecordData(record.Data)
					if ip == "" {
						continue
					}

					// 去重：同一 IP 只记录一次
					mu.Lock()
					if seenIPs[ip] {
						mu.Unlock()
						continue
					}
					seenIPs[ip] = true
					mu.Unlock()

					ipInfo := NSIPInfo{
						IP:         ip,
						RecordType: record.Type,
						Matched:    false,
					}

					// 使用传入的 store 进行匹配（已确保线程安全）
					if ipdbStore != nil {
						match, err := ipdbStore.LookupIP(ip)
						if err == nil && match.Matched {
							ipInfo.Matched = true
							ipInfo.Record = match.Record
						}
					}

					mu.Lock()
					result.IPs = append(result.IPs, ipInfo)
					mu.Unlock()
				}
			}
		}(rt)
	}

	wg.Wait()

	// 对 IP 进行排序，保证输出稳定：先 A 后 AAAA，再按 IP 字符串排序
	sort.Slice(result.IPs, func(i, j int) bool {
		if result.IPs[i].RecordType != result.IPs[j].RecordType {
			// A 记录排在前面
			return result.IPs[i].RecordType < result.IPs[j].RecordType
		}
		return result.IPs[i].IP < result.IPs[j].IP
	})

	result.QueryTime = time.Since(start).Milliseconds()

	// 如果没有 IP，按优先级选择一个确定的错误
	if len(result.IPs) == 0 {
		result.Error = selectNSError(queryErrors)
	}

	return result
}

// selectNSError 从查询错误列表中按优先级选择一个确定的错误
// 优先级：NXDOMAIN > SERVFAIL/REFUSED > 其他 ERROR > "未找到 IP 地址"
func selectNSError(errors []string) string {
	if len(errors) == 0 {
		return "未找到 IP 地址"
	}

	// 按优先级分类
	var hasNXDOMAIN, hasSERVFAIL, hasREFUSED bool
	var otherErrors []string

	for _, err := range errors {
		switch {
		case err == "NXDOMAIN" || containsNXDOMAIN(err):
			hasNXDOMAIN = true
		case err == "SERVFAIL" || containsSERVFAIL(err):
			hasSERVFAIL = true
		case err == "REFUSED" || containsREFUSED(err):
			hasREFUSED = true
		default:
			otherErrors = append(otherErrors, err)
		}
	}

	// 按优先级返回错误
	if hasNXDOMAIN {
		return "域名不存在 (NXDOMAIN)"
	}
	if hasSERVFAIL {
		return "服务器错误 (SERVFAIL)"
	}
	if hasREFUSED {
		return "查询被拒绝 (REFUSED)"
	}
	if len(otherErrors) > 0 {
		// 去重并排序，取第一个
		sort.Strings(otherErrors)
		uniqueErrors := make([]string, 0, len(otherErrors))
		for i, err := range otherErrors {
			if i == 0 || err != otherErrors[i-1] {
				uniqueErrors = append(uniqueErrors, err)
			}
		}
		return uniqueErrors[0]
	}

	return "未找到 IP 地址"
}

// containsNXDOMAIN 检查错误信息是否包含 NXDOMAIN
func containsNXDOMAIN(err string) bool {
	return len(err) >= 8 && (err[:8] == "NXDOMAIN" || containsSubstring(err, "NXDOMAIN"))
}

// containsSERVFAIL 检查错误信息是否包含 SERVFAIL
func containsSERVFAIL(err string) bool {
	return len(err) >= 8 && (err[:8] == "SERVFAIL" || containsSubstring(err, "SERVFAIL"))
}

// containsREFUSED 检查错误信息是否包含 REFUSED
func containsREFUSED(err string) bool {
	return len(err) >= 7 && (err[:7] == "REFUSED" || containsSubstring(err, "REFUSED"))
}

// containsSubstring 检查字符串是否包含子串
func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
