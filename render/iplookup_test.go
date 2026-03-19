package render

import (
	"bytes"
	"strings"
	"testing"
)

type ipLookupStub struct {
	ip            string
	matched       bool
	network       string
	country       string
	countryCode   string
	continent     string
	continentCode string
	asn           string
	asName        string
	asDomain      string
}

func (s ipLookupStub) IPText() string {
	return s.ip
}

func (s ipLookupStub) MatchedState() bool {
	return s.matched
}

func (s ipLookupStub) NetworkText() string {
	return s.network
}

func (s ipLookupStub) CountryText() string {
	return s.country
}

func (s ipLookupStub) CountryCodeText() string {
	return s.countryCode
}

func (s ipLookupStub) ContinentText() string {
	return s.continent
}

func (s ipLookupStub) ContinentCodeText() string {
	return s.continentCode
}

func (s ipLookupStub) ASNText() string {
	return s.asn
}

func (s ipLookupStub) ASNameText() string {
	return s.asName
}

func (s ipLookupStub) ASDomainText() string {
	return s.asDomain
}

func TestWriteIPLookup(t *testing.T) {
	var buffer bytes.Buffer

	WriteIPLookup(&buffer, ipLookupStub{
		ip:            "1.1.1.1",
		matched:       true,
		network:       "1.1.1.0/24",
		country:       "Australia",
		countryCode:   "AU",
		continent:     "Oceania",
		continentCode: "OC",
		asn:           "AS13335",
		asName:        "Cloudflare, Inc.",
		asDomain:      "cloudflare.com",
	})

	output := buffer.String()
	for _, token := range []string{
		"IP 查询结果",
		"1.1.1.1",
		"HIT",
		"1.1.1.0/24",
		"Cloudflare, Inc.",
	} {
		if !strings.Contains(output, token) {
			t.Fatalf("output missing token %q:\n%s", token, output)
		}
	}
}
