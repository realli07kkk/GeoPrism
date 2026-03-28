package cli

import (
	"context"
	"log"
	"time"

	"geoprism/backend/ipdb"
	"geoprism/backend/ipinfo"
	"geoprism/backend/settings"
)

// recordsDiffer 比较两条 Record 的 7 个数据字段是否不同。
func recordsDiffer(a, b ipdb.Record) bool {
	return a.Country != b.Country ||
		a.CountryCode != b.CountryCode ||
		a.Continent != b.Continent ||
		a.ContinentCode != b.ContinentCode ||
		a.ASN != b.ASN ||
		a.ASName != b.ASName ||
		a.ASDomain != b.ASDomain
}

// mergeIPInfo 根据 priority 合并 ipdb 和 ipinfo 结果。
// 返回最终 Match 和数据来源标识（"ipdb" / "ipinfo" / "none"）。
func (a *App) mergeIPInfo(ip string, ipdbMatch ipdb.Match, ipinfoResp *ipinfo.Response) (ipdb.Match, string) {
	isIPInfoFirst := a.settings != nil && a.settings.DataSourcePriority() == settings.PriorityIPInfoFirst

	// ipinfo 无数据，直接返回 ipdb 结果
	if ipinfoResp == nil {
		if ipdbMatch.Matched {
			return ipdbMatch, "ipdb"
		}
		return ipdbMatch, "none"
	}

	ipinfoRecord := ipinfoResp.ToRecord()

	// ipinfo-first：ipinfo 优先
	if isIPInfoFirst {
		return ipdb.Match{
			IP:      ip,
			Matched: true,
			Record:  ipinfoRecord,
		}, "ipinfo"
	}

	// ipdb-first：ipdb 有数据时用 ipdb，无数据时用 ipinfo
	if ipdbMatch.Matched {
		return ipdbMatch, "ipdb"
	}

	// ipdb 未命中，用 ipinfo
	return ipdb.Match{
		IP:      ip,
		Matched: true,
		Record:  ipinfoRecord,
	}, "ipinfo"
}

// maybeWriteBack 如果 ipinfo 数据与 ipdb 不同，异步写入 /32 或 /128 记录。
func (a *App) maybeWriteBack(ip string, ipinfoResp *ipinfo.Response, ipdbMatch ipdb.Match) {
	if ipinfoResp == nil || a.ipdbStore == nil {
		return
	}

	ipinfoRecord := ipinfoResp.ToRecord()

	// ipdb 未命中，直接写入
	if !ipdbMatch.Matched {
		go a.writeIPInfoRecord(ipinfoRecord)
		return
	}

	// ipdb 有数据但与 ipinfo 不同，写入覆盖记录
	if recordsDiffer(ipdbMatch.Record, ipinfoRecord) {
		go a.writeIPInfoRecord(ipinfoRecord)
	}
}

// writeIPInfoRecord 异步写入单条 ipinfo 记录到 ipdb。
func (a *App) writeIPInfoRecord(record ipdb.Record) {
	if err := a.ipdbStore.WriteRecord(record); err != nil {
		log.Printf("ipinfo 回写 ipdb 失败: %v", err)
	}
}

// lookupIPInfoSync 同步调用 ipinfo API，超时 5 秒。
func (a *App) lookupIPInfoSync(ip string) *ipinfo.Response {
	if a.ipinfoClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := a.ipinfoClient.LookupIP(ctx, ip)
	if err != nil {
		log.Printf("ipinfo 查询失败 (%s): %v", ip, err)
		return nil
	}
	return resp
}
