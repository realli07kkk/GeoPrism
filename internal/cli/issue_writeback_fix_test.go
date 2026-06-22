package cli

import (
	"strings"
	"testing"

	"geoprism/backend/ipdb"
	"geoprism/backend/ipinfo"
	"geoprism/backend/resolver"
)

// 验证 issue 2026-06-20-ipdb-writeback-breaks-lpm 紧急止血：
// 配置 ipinfo、查询命中/未命中 IP 后，base keyspace 不应被写入新的 /32 /128。
func TestIssueWritebackDisabled_NoBaseMutation(t *testing.T) {
	t.Run("单 IP 路径：ipinfo 返回不同数据也不写 base", func(t *testing.T) {
		rootDir := t.TempDir()
		buildTestIPDB(t, rootDir)

		// 注入会返回与 base 不同数据的 ipinfo mock（旧逻辑会触发回写覆盖）
		app := &App{
			ipdbRootDir: rootDir,
			ipinfoLookup: func(ip string) *ipinfo.Response {
				return &ipinfo.Response{
					IP:            ip,
					Country:       "Different",
					CountryCode:   "XX",
					Continent:     "Other",
					ContinentCode: "OO",
					ASN:           "AS99999",
					ASName:        "Other AS",
					ASDomain:      "other.example",
				}
			},
		}
		defer app.Close()

		// 触发现有 /24 的 base 命中路径（旧逻辑因 recordsDiffer 会回写 1.0.0.1/32 覆盖）
		if _, err := app.LookupIP("1.0.0.1"); err != nil {
			t.Fatalf("LookupIP() error = %v", err)
		}
		// 触发 base 未命中路径（旧逻辑会回写 8.8.8.8/32）
		if _, err := app.LookupIP("8.8.8.8"); err != nil {
			t.Fatalf("LookupIP() error = %v", err)
		}

		// 关闭 app 持有的 store，释放 Pebble 文件锁，再独立打开 base 确认未被污染
		if err := app.Close(); err != nil {
			t.Fatalf("app.Close() error = %v", err)
		}

		store, err := ipdb.OpenCurrent(rootDir)
		if err != nil {
			t.Fatalf("OpenCurrent() error = %v", err)
		}
		defer store.Close()

		// 1.0.0.5 仍落在原 /24 内，应继续命中 /24（若是被回写污染，最近前驱可能变成不包含它的 /32）
		m, err := store.LookupIP("1.0.0.5")
		if err != nil {
			t.Fatalf("LookupIP(1.0.0.5) error = %v", err)
		}
		if !m.Matched || m.Record.Network != "1.0.0.0/24" {
			t.Fatalf("1.0.0.5 should still hit 1.0.0.0/24, got matched=%v network=%q", m.Matched, m.Record.Network)
		}
		// 8.8.8.8 base 不含数据，应仍未命中（旧逻辑会命中回写的 /32）
		m2, err := store.LookupIP("8.8.8.8")
		if err != nil {
			t.Fatalf("LookupIP(8.8.8.8) error = %v", err)
		}
		if m2.Matched {
			t.Fatalf("8.8.8.8 should NOT be persisted into base, got matched network=%q", m2.Record.Network)
		}
	})

	t.Run("域名路径：ipinfo 结果也不写 base", func(t *testing.T) {
		rootDir := t.TempDir()
		buildTestIPDB(t, rootDir)

		app := &App{
			ipdbRootDir: rootDir,
			ipinfoLookup: func(ip string) *ipinfo.Response {
				return &ipinfo.Response{
					IP:          ip,
					Country:     "Different",
					CountryCode: "XX",
				}
			},
		}
		defer app.Close()

		answers := []QueryAnswer{
			{
				Provider: "Cloudflare",
				Success:  true,
				Answers: []resolver.DNSRecord{
					{Type: "A", Data: "example.com 300 IN A 1.0.0.1"},
					{Type: "A", Data: "example.com 300 IN A 8.8.8.8"},
				},
			},
		}
		matches := app.collectIPMatches(answers)
		if len(matches) != 2 {
			t.Fatalf("len(matches) = %d, want 2", len(matches))
		}

		if err := app.Close(); err != nil {
			t.Fatalf("app.Close() error = %v", err)
		}

		store, err := ipdb.OpenCurrent(rootDir)
		if err != nil {
			t.Fatalf("OpenCurrent() error = %v", err)
		}
		defer store.Close()

		// 域名路径同样不应污染 base：8.8.8.8 不应被持久化
		m, err := store.LookupIP("8.8.8.8")
		if err != nil {
			t.Fatalf("LookupIP(8.8.8.8) error = %v", err)
		}
		if m.Matched {
			t.Fatalf("8.8.8.8 should NOT be persisted into base via domain path, got network=%q", m.Record.Network)
		}
	})

	t.Run("收口后 v2 库正常打开（v1 硬拒绝见 backend TestOpenCurrentRejectsLegacyFormat）", func(t *testing.T) {
		// ipdb-v2-query 收口后 OpenCurrent 打开 v1 库直接返回 ErrLegacyFormat（不再软警告）。
		// buildTestIPDB 经公开 BuildFromCSV 现产 v2 库，无法直接造 v1 库；
		// v1 硬拒绝逻辑由 backend/ipdb 的 TestOpenCurrentRejectsLegacyFormat 覆盖。
		// 本子测试改为验证：v2 库下 ensureIPDBStore 正常打开、无 v1 warning。
		rootDir := t.TempDir()
		buildTestIPDB(t, rootDir)

		app := &App{ipdbRootDir: rootDir}
		defer app.Close()

		store := app.ensureIPDBStore()
		if store == nil {
			t.Fatal("ensureIPDBStore() = nil, want non-nil v2 store")
		}
		if strings.Contains(app.ipdbWarning, "旧版离线库格式") {
			t.Fatalf("v2 库不应触发 v1 warning, ipdbWarning = %q", app.ipdbWarning)
		}
	})
}
