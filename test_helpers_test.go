package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"geoprism/backend/ipdb"
)

func buildTestIPDB(t *testing.T, rootDir string) {
	t.Helper()

	csvPath := writeTestCSVFixture(t, t.TempDir(), strings.Join([]string{
		"network,country,country_code,continent,continent_code,asn,as_name,as_domain",
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
		`2001:db8::/48,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
	}, "\n"))

	if _, err := ipdb.BuildFromCSV(rootDir, ipdb.BuildOptions{
		CSVPath: csvPath,
		BuildID: "test-build",
	}); err != nil {
		t.Fatalf("BuildFromCSV() error = %v", err)
	}
}

func writeTestCSVFixture(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "fixture.csv")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write CSV fixture error = %v", err)
	}
	return path
}
