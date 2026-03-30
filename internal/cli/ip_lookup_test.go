package cli

import (
	"strings"
	"testing"

	"geoprism/backend/resolver"
)

func TestParseIPInput(t *testing.T) {
	tests := []struct {
		input    string
		wantText string
		wantCIDR bool
		wantOK   bool
	}{
		{input: "1.1.1.1", wantText: "1.1.1.1", wantCIDR: false, wantOK: true},
		{input: "2606:4700:4700::1111", wantText: "2606:4700:4700::1111", wantCIDR: false, wantOK: true},
		{input: "47.101.108.7/24", wantText: "47.101.108.0/24", wantCIDR: true, wantOK: true},
		{input: "2001:db8::1/32", wantText: "2001:db8::/32", wantCIDR: true, wantOK: true},
		{input: "example.com", wantText: "", wantCIDR: false, wantOK: false},
		{input: "999.999.999.999", wantText: "", wantCIDR: false, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotText, gotCIDR, gotOK := parseIPInput(tt.input)
			if gotOK != tt.wantOK || gotCIDR != tt.wantCIDR || gotText != tt.wantText {
				t.Fatalf("parseIPInput(%q) = (%q, %v, %v), want (%q, %v, %v)", tt.input, gotText, gotCIDR, gotOK, tt.wantText, tt.wantCIDR, tt.wantOK)
			}
		})
	}
}

func TestAppLookupIP(t *testing.T) {
	t.Run("命中 IPv4 记录", func(t *testing.T) {
		rootDir := t.TempDir()
		buildTestIPDB(t, rootDir)

		app := &App{ipdbRootDir: rootDir}
		defer app.Close()
		result, err := app.LookupIP("1.0.0.1")
		if err != nil {
			t.Fatalf("LookupIP() error = %v", err)
		}
		if !result.Matched {
			t.Fatal("LookupIP() should hit offline IP DB")
		}
		if result.Network != "1.0.0.0/24" {
			t.Fatalf("Network = %q, want %q", result.Network, "1.0.0.0/24")
		}
		if result.Country != "Australia" {
			t.Fatalf("Country = %q, want %q", result.Country, "Australia")
		}
	})

	t.Run("未命中记录", func(t *testing.T) {
		rootDir := t.TempDir()
		buildTestIPDB(t, rootDir)

		app := &App{ipdbRootDir: rootDir}
		defer app.Close()
		result, err := app.LookupIP("8.8.8.8")
		if err != nil {
			t.Fatalf("LookupIP() error = %v", err)
		}
		if result.Matched {
			t.Fatal("LookupIP() should miss offline IP DB")
		}
		if result.Network != "" {
			t.Fatalf("Network = %q, want empty string", result.Network)
		}
	})

	t.Run("非法 IP", func(t *testing.T) {
		app := &App{ipdbRootDir: t.TempDir()}
		_, err := app.LookupIP("not-an-ip")
		if err == nil {
			t.Fatal("LookupIP() error = nil, want invalid IP error")
		}
		if !strings.Contains(err.Error(), "IP 格式非法") {
			t.Fatalf("error = %v, want invalid IP message", err)
		}
	})

	t.Run("缺少离线库", func(t *testing.T) {
		app := &App{ipdbRootDir: t.TempDir()}
		_, err := app.LookupIP("1.1.1.1")
		if err == nil {
			t.Fatal("LookupIP() error = nil, want missing DB error")
		}
		if err.Error() != missingIPDBErrorMessage {
			t.Fatalf("error = %q, want %q", err.Error(), missingIPDBErrorMessage)
		}
	})
}

func TestCollectIPMatchesWithoutIPDBKeepsSoftFailure(t *testing.T) {
	app := &App{ipdbRootDir: t.TempDir()}
	answers := []QueryAnswer{
		{
			Provider: "Cloudflare",
			Success:  true,
			Answers: []resolver.DNSRecord{
				{Type: "A", Data: "example.com 300 IN A 1.1.1.1"},
			},
		},
	}

	matches := app.collectIPMatches(answers)
	if len(matches) != 0 {
		t.Fatalf("len(matches) = %d, want 0", len(matches))
	}
	if !strings.Contains(app.ipdbWarning, "未找到可用的离线 IP 库") {
		t.Fatalf("ipdbWarning = %q, want missing DB warning", app.ipdbWarning)
	}
}
