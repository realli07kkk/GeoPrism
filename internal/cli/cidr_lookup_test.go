package cli

import (
	"strings"
	"testing"

	"geoprism/backend/ipdb"
	"geoprism/backend/ipinfo"
)

func TestAppLookupCIDR(t *testing.T) {
	t.Run("命中多条离线记录", func(t *testing.T) {
		rootDir := t.TempDir()
		buildCIDRTestIPDB(t, rootDir)

		lookupCount := 0
		app := &App{
			ipdbRootDir: rootDir,
			ipinfoLookup: func(ip string) *ipinfo.Response {
				lookupCount++
				return &ipinfo.Response{IP: ip}
			},
		}
		defer app.Close()

		result, err := app.LookupCIDR("1.0.0.0/23")
		if err != nil {
			t.Fatalf("LookupCIDR() error = %v", err)
		}
		if !result.Matched {
			t.Fatal("LookupCIDR() should hit offline CIDR records")
		}
		if result.MatchCount != 2 {
			t.Fatalf("MatchCount = %d, want 2", result.MatchCount)
		}
		if result.QueryCIDR != "1.0.0.0/23" {
			t.Fatalf("QueryCIDR = %q, want %q", result.QueryCIDR, "1.0.0.0/23")
		}
		if result.Fallback != nil {
			t.Fatal("Fallback should be nil when ipdb has CIDR hits")
		}

		gotNetworks := []string{result.Matches[0].Network, result.Matches[1].Network}
		wantNetworks := []string{"1.0.0.0/24", "1.0.1.0/24"}
		for i := range wantNetworks {
			if gotNetworks[i] != wantNetworks[i] {
				t.Fatalf("matches[%d] = %q, want %q", i, gotNetworks[i], wantNetworks[i])
			}
		}
		if lookupCount != 0 {
			t.Fatalf("lookupCount = %d, want 0", lookupCount)
		}
	})

	t.Run("ipdb 未命中时回退到单 IP 查询", func(t *testing.T) {
		rootDir := t.TempDir()
		buildCIDRTestIPDB(t, rootDir)

		lookupCount := 0
		app := &App{
			ipdbRootDir: rootDir,
			ipinfoLookup: func(ip string) *ipinfo.Response {
				lookupCount++
				if ip != "8.8.8.0" {
					t.Fatalf("fallback ip = %q, want %q", ip, "8.8.8.0")
				}
				return &ipinfo.Response{
					IP:            ip,
					Country:       "United States",
					CountryCode:   "US",
					Continent:     "North America",
					ContinentCode: "NA",
					ASN:           "AS15169",
					ASName:        "Google LLC",
					ASDomain:      "google.com",
				}
			},
		}
		defer app.Close()

		result, err := app.LookupCIDR("8.8.8.0/24")
		if err != nil {
			t.Fatalf("LookupCIDR() error = %v", err)
		}
		if result.Matched {
			t.Fatal("CIDR result should remain unmatched when only fallback has data")
		}
		if result.MatchCount != 0 {
			t.Fatalf("MatchCount = %d, want 0", result.MatchCount)
		}
		if result.Fallback == nil {
			t.Fatal("Fallback should not be nil")
		}
		if result.Fallback.IP != "8.8.8.0" {
			t.Fatalf("Fallback.IP = %q, want %q", result.Fallback.IP, "8.8.8.0")
		}
		if result.Fallback.Source != "ipinfo" {
			t.Fatalf("Fallback.Source = %q, want %q", result.Fallback.Source, "ipinfo")
		}
		if lookupCount != 1 {
			t.Fatalf("lookupCount = %d, want 1", lookupCount)
		}
	})

	t.Run("缺少离线库且无法回退时返回错误", func(t *testing.T) {
		app := &App{ipdbRootDir: t.TempDir()}
		_, err := app.LookupCIDR("1.1.1.0/24")
		if err == nil {
			t.Fatal("LookupCIDR() error = nil, want missing DB error")
		}
		if err.Error() != missingIPDBErrorMessage {
			t.Fatalf("error = %q, want %q", err.Error(), missingIPDBErrorMessage)
		}
	})

	t.Run("非法 CIDR", func(t *testing.T) {
		app := &App{ipdbRootDir: t.TempDir()}
		_, err := app.LookupCIDR("not-a-cidr")
		if err == nil {
			t.Fatal("LookupCIDR() error = nil, want invalid CIDR error")
		}
		if !strings.Contains(err.Error(), "CIDR 格式非法") {
			t.Fatalf("error = %v, want invalid CIDR message", err)
		}
	})
}

func buildCIDRTestIPDB(t *testing.T, rootDir string) {
	t.Helper()

	csvPath := writeTestCSVFixture(t, t.TempDir(), strings.Join([]string{
		"network,country,country_code,continent,continent_code,asn,as_name,as_domain",
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
		`1.0.1.0/24,China,CN,Asia,AS,AS9808,Guangdong Mobile,gd.10086.cn`,
		`2001:db8::/48,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
	}, "\n"))

	if _, err := ipdb.BuildFromCSV(rootDir, ipdb.BuildOptions{
		CSVPath: csvPath,
		BuildID: "cidr-test-build",
	}); err != nil {
		t.Fatalf("BuildFromCSV() error = %v", err)
	}
}
