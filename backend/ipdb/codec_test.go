package ipdb

import (
	"net/netip"
	"testing"
)

func TestEncodePrefixKeyRoundTrip(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		prefix := netip.MustParsePrefix("1.0.0.0/24")

		key, err := encodePrefixKey(prefix)
		if err != nil {
			t.Fatalf("encodePrefixKey() error = %v", err)
		}

		addr, family, err := decodeKeyAddr(key)
		if err != nil {
			t.Fatalf("decodeKeyAddr() error = %v", err)
		}

		if family != keyFamilyIPv4 {
			t.Fatalf("family = %d, want %d", family, keyFamilyIPv4)
		}
		if addr.String() != "1.0.0.0" {
			t.Fatalf("addr = %s, want 1.0.0.0", addr.String())
		}
	})

	t.Run("ipv6", func(t *testing.T) {
		prefix := netip.MustParsePrefix("2001:db8::/48")

		key, err := encodePrefixKey(prefix)
		if err != nil {
			t.Fatalf("encodePrefixKey() error = %v", err)
		}

		addr, family, err := decodeKeyAddr(key)
		if err != nil {
			t.Fatalf("decodeKeyAddr() error = %v", err)
		}

		if family != keyFamilyIPv6 {
			t.Fatalf("family = %d, want %d", family, keyFamilyIPv6)
		}
		if addr.String() != "2001:db8::" {
			t.Fatalf("addr = %s, want 2001:db8::", addr.String())
		}
	})
}

func TestEncodeRecordValueRoundTrip(t *testing.T) {
	addr := netip.MustParseAddr("1.0.0.0")
	record := Record{
		Country:       "Australia",
		CountryCode:   "AU",
		Continent:     "Oceania",
		ContinentCode: "OC",
		ASN:           "AS13335",
		ASName:        "Cloudflare, Inc.",
		ASDomain:      "cloudflare.com",
	}

	value, err := encodeRecordValue(24, record)
	if err != nil {
		t.Fatalf("encodeRecordValue() error = %v", err)
	}

	decoded, err := decodeRecordValue(value, addr)
	if err != nil {
		t.Fatalf("decodeRecordValue() error = %v", err)
	}

	if decoded.Network != "1.0.0.0/24" {
		t.Fatalf("Network = %s, want 1.0.0.0/24", decoded.Network)
	}
	if decoded.Country != record.Country ||
		decoded.CountryCode != record.CountryCode ||
		decoded.Continent != record.Continent ||
		decoded.ContinentCode != record.ContinentCode ||
		decoded.ASN != record.ASN ||
		decoded.ASName != record.ASName ||
		decoded.ASDomain != record.ASDomain {
		t.Fatalf("decoded record = %#v, want %#v", decoded, record)
	}
}

func TestEncodeRecordValueWithEmptyFields(t *testing.T) {
	addr := netip.MustParseAddr("2001:db8::")
	record := Record{
		Country:       "Testland",
		CountryCode:   "TT",
		Continent:     "",
		ContinentCode: "",
	}

	value, err := encodeRecordValue(48, record)
	if err != nil {
		t.Fatalf("encodeRecordValue() error = %v", err)
	}

	decoded, err := decodeRecordValue(value, addr)
	if err != nil {
		t.Fatalf("decodeRecordValue() error = %v", err)
	}

	if decoded.Network != "2001:db8::/48" {
		t.Fatalf("Network = %s, want 2001:db8::/48", decoded.Network)
	}
	if decoded.Continent != "" || decoded.ASN != "" || decoded.ASDomain != "" {
		t.Fatalf("decoded empty fields mismatch: %#v", decoded)
	}
}
