package cli

import (
	"context"
	"log"
	"time"

	"geoprism/backend/ipdb"
	"geoprism/backend/ipinfo"
	"geoprism/backend/settings"
)

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

// lookupIPInfoSync 同步调用 ipinfo API，超时 5 秒。
func (a *App) lookupIPInfoSync(ip string) *ipinfo.Response {
	if a != nil && a.ipinfoLookup != nil {
		return a.ipinfoLookup(ip)
	}
	if a == nil || a.ipinfoClient == nil {
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
