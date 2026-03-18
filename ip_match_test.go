package main

import (
	"bytes"
	"strings"
	"testing"

	"geoprism/backend/ipdb"
	"geoprism/backend/resolver"
)

func TestExtractAnswerIPs(t *testing.T) {
	records := []resolver.DNSRecord{
		{Type: "A", Data: "example.com 300 IN A 1.1.1.1"},
		{Type: "AAAA", Data: "example.com 300 IN AAAA 2606:4700:4700::1111"},
		{Type: "TXT", Data: "example.com 300 IN TXT \"hello\""},
		{Type: "A", Data: "example.com 300 IN A invalid-ip"},
	}

	got := extractAnswerIPs(records)
	want := []string{"1.1.1.1", "2606:4700:4700::1111"}

	if len(got) != len(want) {
		t.Fatalf("len(extractAnswerIPs()) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extractAnswerIPs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriteIPMatches(t *testing.T) {
	var buffer bytes.Buffer

	writeIPMatches(&buffer, []IPMatchView{
		{
			Provider:   "Cloudflare",
			RecordType: "A",
			IP:         "1.1.1.1",
			Matched:    true,
			Record: ipdb.Record{
				Network:       "1.1.1.0/24",
				Country:       "Australia",
				CountryCode:   "AU",
				Continent:     "Oceania",
				ContinentCode: "OC",
				ASN:           "AS13335",
				ASName:        "Cloudflare, Inc.",
				ASDomain:      "cloudflare.com",
			},
		},
		{
			Provider:   "Google",
			RecordType: "AAAA",
			IP:         "2001:db8::1",
			Matched:    false,
		},
	})

	output := buffer.String()
	for _, token := range []string{
		"IP 匹配详情",
		"Cloudflare",
		"HIT",
		"1.1.1.0/24",
		"Google",
		"MISS",
	} {
		if !strings.Contains(output, token) {
			t.Fatalf("output missing token %q:\n%s", token, output)
		}
	}
}
