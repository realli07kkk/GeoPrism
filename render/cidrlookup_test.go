package render

import (
	"bytes"
	"strings"
	"testing"
)

type cidrLookupMatchStub struct {
	network       string
	country       string
	countryCode   string
	continent     string
	continentCode string
	asn           string
	asName        string
	asDomain      string
	source        string
}

func (s cidrLookupMatchStub) NetworkText() string {
	return s.network
}

func (s cidrLookupMatchStub) CountryText() string {
	return s.country
}

func (s cidrLookupMatchStub) CountryCodeText() string {
	return s.countryCode
}

func (s cidrLookupMatchStub) ContinentText() string {
	return s.continent
}

func (s cidrLookupMatchStub) ContinentCodeText() string {
	return s.continentCode
}

func (s cidrLookupMatchStub) ASNText() string {
	return s.asn
}

func (s cidrLookupMatchStub) ASNameText() string {
	return s.asName
}

func (s cidrLookupMatchStub) ASDomainText() string {
	return s.asDomain
}

func (s cidrLookupMatchStub) SourceText() string {
	return s.source
}

type cidrLookupStub struct {
	queryCIDR string
	matched   bool
	matches   []cidrLookupMatchStub
	fallback  IPLookupSource
}

func (s cidrLookupStub) QueryCIDRText() string {
	return s.queryCIDR
}

func (s cidrLookupStub) CIDRMatchedState() bool {
	return s.matched
}

func (s cidrLookupStub) CIDRMatchCountValue() int {
	return len(s.matches)
}

func (s cidrLookupStub) CIDRMatchCount() int {
	return len(s.matches)
}

func (s cidrLookupStub) CIDRMatchAt(i int) CIDRLookupMatchSource {
	return s.matches[i]
}

func (s cidrLookupStub) FallbackLookup() IPLookupSource {
	return s.fallback
}

func TestWriteCIDRLookup(t *testing.T) {
	var buffer bytes.Buffer

	WriteCIDRLookup(&buffer, cidrLookupStub{
		queryCIDR: "1.0.0.0/23",
		matched:   true,
		matches: []cidrLookupMatchStub{
			{
				network:  "1.0.0.0/24",
				country:  "Australia",
				asn:      "AS13335",
				asName:   "Cloudflare, Inc.",
				asDomain: "cloudflare.com",
				source:   "ipdb",
			},
			{
				network: "1.0.1.0/24",
				country: "China",
				source:  "ipdb",
			},
		},
		fallback: ipLookupStub{
			ip:      "1.0.0.0",
			matched: true,
			network: "1.0.0.0/32",
			source:  "ipinfo",
		},
	})

	output := buffer.String()
	for _, token := range []string{
		"CIDR 查询结果",
		"1.0.0.0/23",
		"MatchCount: 2",
		"1.0.0.0/24",
		"Cloudflare, Inc.",
		"回退到单 IP 查询",
		"1.0.0.0/32",
		"ipinfo",
	} {
		if !strings.Contains(output, token) {
			t.Fatalf("output missing token %q:\n%s", token, output)
		}
	}
}
