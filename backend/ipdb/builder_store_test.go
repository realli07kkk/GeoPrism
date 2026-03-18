package ipdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildFromCSVAndLookupIP(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
		`1.0.1.0/24,China,CN,Asia,AS,,,`,
		`2001:db8::/48,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
		`2001:db8:1::/48,Testland,TT,Test,TS,,,`,
	}, "\n"))

	builtAt := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	meta, err := BuildFromCSV(rootDir, BuildOptions{
		CSVPath: csvPath,
		BuildID: "test-build",
		BuiltAt: builtAt,
	})
	if err != nil {
		t.Fatalf("BuildFromCSV() error = %v", err)
	}

	if meta.RowCount != 4 || meta.IPv4Count != 2 || meta.IPv6Count != 2 {
		t.Fatalf("metadata counts mismatch: %#v", meta)
	}

	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil {
		t.Fatalf("read CURRENT error = %v", err)
	}
	if string(currentData) != "test-build" {
		t.Fatalf("CURRENT = %q, want test-build", string(currentData))
	}

	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent() error = %v", err)
	}
	defer store.Close()

	assertLookup(t, store, "1.0.0.1", true, "1.0.0.0/24")
	assertLookup(t, store, "1.0.1.0", true, "1.0.1.0/24")
	assertLookup(t, store, "1.0.2.1", false, "")
	assertLookup(t, store, "2001:db8::1", true, "2001:db8::/48")
	assertLookup(t, store, "2001:db8:1::1", true, "2001:db8:1::/48")
	assertLookup(t, store, "2001:db8:2::1", false, "")
}

func TestBuildFromCSVRejectsOverlappingCIDR(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
		`1.0.0.128/25,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
	}, "\n"))

	_, err := BuildFromCSV(rootDir, BuildOptions{
		CSVPath: csvPath,
		BuildID: "bad-build",
	})
	if err == nil {
		t.Fatal("BuildFromCSV() error = nil, want overlap error")
	}
	if !strings.Contains(err.Error(), "重叠网段") {
		t.Fatalf("error = %v, want overlap message", err)
	}

	if _, statErr := os.Stat(filepath.Join(rootDir, currentFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("CURRENT should not exist, statErr = %v", statErr)
	}
}

func TestBuildFromCSVRequiresAbsolutePath(t *testing.T) {
	_, err := BuildFromCSV(t.TempDir(), BuildOptions{
		CSVPath: "relative.csv",
	})
	if err == nil {
		t.Fatal("BuildFromCSV() error = nil, want absolute path error")
	}
}

func TestBuildFromCSVAcceptsSingleIPRows(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.7.168.172,India,IN,Asia,AS,AS9583,Sify Limited,sifycorp.com`,
		`1.7.168.173,Singapore,SG,Asia,AS,AS9583,Sify Limited,sifycorp.com`,
		`2001:db8::1,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
	}, "\n"))

	meta, err := BuildFromCSV(rootDir, BuildOptions{
		CSVPath: csvPath,
		BuildID: "single-ip-build",
	})
	if err != nil {
		t.Fatalf("BuildFromCSV() error = %v", err)
	}
	if meta.RowCount != 3 || meta.IPv4Count != 2 || meta.IPv6Count != 1 {
		t.Fatalf("metadata counts mismatch: %#v", meta)
	}

	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent() error = %v", err)
	}
	defer store.Close()

	assertLookup(t, store, "1.7.168.172", true, "1.7.168.172/32")
	assertLookup(t, store, "1.7.168.173", true, "1.7.168.173/32")
	assertLookup(t, store, "1.7.168.174", false, "")
	assertLookup(t, store, "2001:db8::1", true, "2001:db8::1/128")
	assertLookup(t, store, "2001:db8::2", false, "")
}

func assertLookup(t *testing.T, store *Store, ip string, wantMatched bool, wantNetwork string) {
	t.Helper()

	match, err := store.LookupIP(ip)
	if err != nil {
		t.Fatalf("LookupIP(%q) error = %v", ip, err)
	}
	if match.Matched != wantMatched {
		t.Fatalf("LookupIP(%q) matched = %v, want %v", ip, match.Matched, wantMatched)
	}
	if match.Record.Network != wantNetwork {
		t.Fatalf("LookupIP(%q) network = %q, want %q", ip, match.Record.Network, wantNetwork)
	}
}

func writeCSVFixture(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "fixture.csv")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write CSV fixture error = %v", err)
	}
	return path
}
