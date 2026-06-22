package ipdb

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

// v2 key round-trip 边界集合（V4 + V6，含 /0、最小、最大 prefixLen）。
var v2KeyRoundTripPrefixes = []string{
	"0.0.0.0/0",
	"10.0.0.0/8",
	"10.1.0.0/16",
	"10.1.2.0/24",
	"1.0.0.0/32",
	"::/0",
	"2001:db8::/48",
	"2001:db8::/128",
}

func TestEncodePrimaryKeyV2RoundTrip(t *testing.T) {
	for _, s := range v2KeyRoundTripPrefixes {
		p := netip.MustParsePrefix(s)
		key, err := encodePrimaryKeyV2(p)
		if err != nil {
			t.Fatalf("encodePrimaryKeyV2(%s) error = %v", s, err)
		}
		decoded, err := decodePrimaryKeyV2(key)
		if err != nil {
			t.Fatalf("decodePrimaryKeyV2(%s) error = %v", s, err)
		}
		if decoded != p {
			t.Fatalf("primary round-trip mismatch for %s: got %s", s, decoded.String())
		}
	}
}

func TestEncodeCIDRKeyV2RoundTrip(t *testing.T) {
	for _, s := range v2KeyRoundTripPrefixes {
		p := netip.MustParsePrefix(s)
		key, err := encodeCIDRKeyV2(p)
		if err != nil {
			t.Fatalf("encodeCIDRKeyV2(%s) error = %v", s, err)
		}
		decoded, err := decodeCIDRKeyV2(key)
		if err != nil {
			t.Fatalf("decodeCIDRKeyV2(%s) error = %v", s, err)
		}
		if decoded != p {
			t.Fatalf("cidr round-trip mismatch for %s: got %s", s, decoded.String())
		}
	}
}

func TestEncodeOverlayKeyV2RoundTrip(t *testing.T) {
	addrs := []string{
		"1.0.0.0",
		"10.1.2.3",
		"255.255.255.255",
		"2001:db8::1",
		"::1",
	}
	for _, s := range addrs {
		a := netip.MustParseAddr(s)
		key, err := encodeOverlayKeyV2(a)
		if err != nil {
			t.Fatalf("encodeOverlayKeyV2(%s) error = %v", s, err)
		}
		decoded, err := decodeOverlayKeyV2(key)
		if err != nil {
			t.Fatalf("decodeOverlayKeyV2(%s) error = %v", s, err)
		}
		if decoded != a {
			t.Fatalf("overlay round-trip mismatch for %s: got %s", s, decoded.String())
		}
	}
}

// primary ↔ cidr 互相还原不变量：同一 prefix 经两套 key 编码再解码，
// 必须还原出同一个 netip.Prefix（builder 同 batch 双写的前提）。
func TestPrimaryCIDRMutualRoundTrip(t *testing.T) {
	for _, s := range v2KeyRoundTripPrefixes {
		p := netip.MustParsePrefix(s)
		pk, err := encodePrimaryKeyV2(p)
		if err != nil {
			t.Fatalf("encodePrimaryKeyV2(%s) error = %v", s, err)
		}
		ck, err := encodeCIDRKeyV2(p)
		if err != nil {
			t.Fatalf("encodeCIDRKeyV2(%s) error = %v", s, err)
		}
		fromPrimary, err := decodePrimaryKeyV2(pk)
		if err != nil {
			t.Fatalf("decodePrimaryKeyV2(%s) error = %v", s, err)
		}
		fromCIDR, err := decodeCIDRKeyV2(ck)
		if err != nil {
			t.Fatalf("decodeCIDRKeyV2(%s) error = %v", s, err)
		}
		if fromPrimary != fromCIDR {
			t.Fatalf("primary ↔ cidr 还原不一致 for %s: primary=%s cidr=%s",
				s, fromPrimary.String(), fromCIDR.String())
		}
		if fromPrimary != p {
			t.Fatalf("还原结果与原 prefix 不一致 for %s: got %s", s, fromPrimary.String())
		}
	}
}

// primary 与 cidr key 字节布局不同（prefixLen 位置不同），
// 但 kind 不同不会混；编码出的字节不能相同。
func TestPrimaryKeyAndCIDRKeyByteLayoutDiffer(t *testing.T) {
	p := netip.MustParsePrefix("10.1.0.0/16")
	pk, _ := encodePrimaryKeyV2(p)
	ck, _ := encodeCIDRKeyV2(p)
	if pk[0] == ck[0] {
		t.Fatalf("primary kind 与 cidr kind 不应相同: %d", pk[0])
	}
	// primary: [0x14][0x10][0a 01 00 00]
	if pk[0] != keyKindPrimaryV4 || pk[1] != 16 {
		t.Fatalf("primary key 布局错误: % x", pk)
	}
	// cidr: [0x24][0a 01 00 00][0x10]
	if ck[0] != keyKindCIDRV4 || ck[5] != 16 {
		t.Fatalf("cidr key 布局错误: % x", ck)
	}
}

func TestEncodePrimaryKeyV2NotMasked(t *testing.T) {
	// 10.1.2.3/16：addr 10.1.2.3 ≠ masked.Addr 10.1.0.0
	p := netip.MustParsePrefix("10.1.2.3/16")
	if _, err := encodePrimaryKeyV2(p); err == nil {
		t.Fatal("encodePrimaryKeyV2 未 Masked 应返回 error")
	}
	if _, err := encodeCIDRKeyV2(p); err == nil {
		t.Fatal("encodeCIDRKeyV2 未 Masked 应返回 error")
	}
}

func TestEncodePrefixV2InvalidPrefixLen(t *testing.T) {
	// PrefixFrom 不 panic，返回 Bits()==-1 的 invalid Prefix（越界 prefixLen）。
	v4Invalid := netip.PrefixFrom(netip.MustParseAddr("10.1.0.0"), 33)
	if _, err := encodePrimaryKeyV2(v4Invalid); err == nil {
		t.Fatal("encodePrimaryKeyV2 越界 prefixLen=33 应返回 error")
	}
	if _, err := encodeCIDRKeyV2(v4Invalid); err == nil {
		t.Fatal("encodeCIDRKeyV2 越界 prefixLen=33 应返回 error")
	}
	v6Invalid := netip.PrefixFrom(netip.MustParseAddr("2001:db8::"), 129)
	if _, err := encodePrimaryKeyV2(v6Invalid); err == nil {
		t.Fatal("encodePrimaryKeyV2 越界 prefixLen=129 应返回 error")
	}
	if _, err := encodeCIDRKeyV2(v6Invalid); err == nil {
		t.Fatal("encodeCIDRKeyV2 越界 prefixLen=129 应返回 error")
	}
}

// CR-4: IPv4-mapped IPv6（::ffff:x.x.x.x，Is4In6()==true）所有 encode 入口都必须拒绝。
// family 二义会让 LookupIP(v4addr) 用 V4 ladder 永远查不到被 encode 成 V6 key 的记录。
func TestEncodeV2RejectsIPv4MappedIPv6(t *testing.T) {
	// primary
	if _, err := encodePrimaryKeyV2(netip.MustParsePrefix("::ffff:0:0/96")); err == nil {
		t.Fatal("encodePrimaryKeyV2 应拒绝 IPv4-mapped IPv6")
	}
	// cidr
	if _, err := encodeCIDRKeyV2(netip.MustParsePrefix("::ffff:0:0/96")); err == nil {
		t.Fatal("encodeCIDRKeyV2 应拒绝 IPv4-mapped IPv6")
	}
	// overlay（单 IP）
	if _, err := encodeOverlayKeyV2(netip.MustParseAddr("::ffff:1.2.3.4")); err == nil {
		t.Fatal("encodeOverlayKeyV2 应拒绝 IPv4-mapped IPv6")
	}
}

func TestDecodeV2KeyUnknownKind(t *testing.T) {
	// kind 字节未知（0x99）→ 返回 error
	badKey := []byte{0x99, 0x10, 0x0a, 0x01, 0x00, 0x00}
	if _, err := decodePrimaryKeyV2(badKey); err == nil {
		t.Fatal("decodePrimaryKeyV2 未知 kind 应返回 error")
	}
	if _, err := decodeCIDRKeyV2(badKey); err == nil {
		t.Fatal("decodeCIDRKeyV2 未知 kind 应返回 error")
	}
	if _, err := decodeOverlayKeyV2(badKey); err == nil {
		t.Fatal("decodeOverlayKeyV2 未知 kind 应返回 error")
	}
}

func TestDecodeV2KeyWrongLength(t *testing.T) {
	// primary V4 应为 6B，给 5B（截断）/ 7B（多余）
	short := []byte{keyKindPrimaryV4, 0x10, 0x0a, 0x01, 0x00}
	long := []byte{keyKindPrimaryV4, 0x10, 0x0a, 0x01, 0x00, 0x00, 0x00}
	if _, err := decodePrimaryKeyV2(short); err == nil {
		t.Fatal("decodePrimaryKeyV2 截断 key 应返回 error")
	}
	if _, err := decodePrimaryKeyV2(long); err == nil {
		t.Fatal("decodePrimaryKeyV2 多余字节 key 应返回 error")
	}
	// cidr V4 应为 6B
	cidrShort := []byte{keyKindCIDRV4, 0x0a, 0x01, 0x00}
	if _, err := decodeCIDRKeyV2(cidrShort); err == nil {
		t.Fatal("decodeCIDRKeyV2 截断 key 应返回 error")
	}
	// overlay V4 应为 5B
	overlayLong := []byte{keyKindOverlayV4, 0x0a, 0x01, 0x00, 0x00, 0x00}
	if _, err := decodeOverlayKeyV2(overlayLong); err == nil {
		t.Fatal("decodeOverlayKeyV2 多余字节 key 应返回 error")
	}
}

func TestDecodeV2KeyEmpty(t *testing.T) {
	empty := []byte{}
	if _, err := decodePrimaryKeyV2(empty); err == nil {
		t.Fatal("decodePrimaryKeyV2 空 key 应返回 error")
	}
	if _, err := decodeCIDRKeyV2(empty); err == nil {
		t.Fatal("decodeCIDRKeyV2 空 key 应返回 error")
	}
	if _, err := decodeOverlayKeyV2(empty); err == nil {
		t.Fatal("decodeOverlayKeyV2 空 key 应返回 error")
	}
}

// CR-001: decoder 必须拒绝非法 prefixLen（family 越界）。
// 构造损坏 key：合法 kind/长度/addr，但 prefixLen 字节 = 33（V4）/ 129（V6）。
func TestDecodeV2KeyRejectsInvalidPrefixLen(t *testing.T) {
	// primary V4：[0x14][0x21=33][0a 00 00 00]
	badPrimaryV4 := []byte{keyKindPrimaryV4, 33, 0x0a, 0x00, 0x00, 0x00}
	if _, err := decodePrimaryKeyV2(badPrimaryV4); err == nil {
		t.Fatal("decodePrimaryKeyV2 prefixLen=33 应返回 error")
	}
	// primary V6：[0x16][0x81=129][16B addr]
	badPrimaryV6 := make([]byte, 18)
	badPrimaryV6[0] = keyKindPrimaryV6
	badPrimaryV6[1] = 129
	if _, err := decodePrimaryKeyV2(badPrimaryV6); err == nil {
		t.Fatal("decodePrimaryKeyV2 prefixLen=129 应返回 error")
	}
	// cidr V4：[0x24][0a 00 00 00][0x21=33]
	badCIDRV4 := []byte{keyKindCIDRV4, 0x0a, 0x00, 0x00, 0x00, 33}
	if _, err := decodeCIDRKeyV2(badCIDRV4); err == nil {
		t.Fatal("decodeCIDRKeyV2 prefixLen=33 应返回 error")
	}
	// cidr V6：[0x26][16B addr][0x81=129]
	badCIDRV6 := make([]byte, 18)
	badCIDRV6[0] = keyKindCIDRV6
	badCIDRV6[17] = 129
	if _, err := decodeCIDRKeyV2(badCIDRV6); err == nil {
		t.Fatal("decodeCIDRKeyV2 prefixLen=129 应返回 error")
	}
}

// CR-001: decoder 必须拒绝带 host bits 的 addr（违反 maskedAddr 契约）。
// 合法 prefixLen 但 addr 末位非 0 → PrefixFrom 产出 valid 但未 masked 的 prefix。
func TestDecodeV2KeyRejectsNonMaskedAddr(t *testing.T) {
	// primary V4：[0x14][0x10=16][0a 01 02 03] —— addr=10.1.2.3，/16 应 masked 为 10.1.0.0
	badPrimaryV4 := []byte{keyKindPrimaryV4, 16, 0x0a, 0x01, 0x02, 0x03}
	if _, err := decodePrimaryKeyV2(badPrimaryV4); err == nil {
		t.Fatal("decodePrimaryKeyV2 addr 带 host bits 应返回 error")
	}
	// cidr V4：[0x24][0a 01 02 03][0x10=16]
	badCIDRV4 := []byte{keyKindCIDRV4, 0x0a, 0x01, 0x02, 0x03, 16}
	if _, err := decodeCIDRKeyV2(badCIDRV4); err == nil {
		t.Fatal("decodeCIDRKeyV2 addr 带 host bits 应返回 error")
	}
}

// CR-001: family 与 kind 一致性。合法 addr + 合法 prefixLen，但 decode 出的
// prefix 必须能正常 round-trip（回归保护，确保新校验不误杀合法 key）。
func TestDecodeV2KeyAcceptsValidMaskedPrefixAllBits(t *testing.T) {
	// V4 /0 到 /32 每个边界
	v4Prefixes := []struct {
		bits int
		addr [4]byte
	}{
		{0, [4]byte{0, 0, 0, 0}},
		{8, [4]byte{10, 0, 0, 0}},
		{16, [4]byte{10, 1, 0, 0}},
		{24, [4]byte{10, 1, 2, 0}},
		{32, [4]byte{10, 1, 2, 3}}, // /32 时 host bits 即全 addr，masked==addr
	}
	for _, c := range v4Prefixes {
		// primary
		pk := []byte{keyKindPrimaryV4, byte(c.bits)}
		pk = append(pk, c.addr[:]...)
		got, err := decodePrimaryKeyV2(pk)
		if err != nil {
			t.Fatalf("decodePrimaryKeyV4 bits=%d 合法 key 应成功: %v", c.bits, err)
		}
		if got.Bits() != c.bits {
			t.Fatalf("bits=%d round-trip 失败: got %d", c.bits, got.Bits())
		}
	}
}

// 防回归：v2 key 不与 v1 key 冲突（kind 字节不同）。
func TestV2KeyKindDiffersFromV1(t *testing.T) {
	v1Key, err := encodePrefixKey(netip.MustParsePrefix("10.1.0.0/16"))
	if err != nil {
		t.Fatalf("encodePrefixKey error = %v", err)
	}
	v2Key, err := encodePrimaryKeyV2(netip.MustParsePrefix("10.1.0.0/16"))
	if err != nil {
		t.Fatalf("encodePrimaryKeyV2 error = %v", err)
	}
	if v1Key[0] == v2Key[0] {
		t.Fatalf("v1/v2 kind 不应相同: v1=%d v2=%d", v1Key[0], v2Key[0])
	}
	if bytes.Equal(v1Key, v2Key) {
		t.Fatalf("v1 与 v2 key 不应完全相同: % x", v1Key)
	}
}

// === v2 value 测试 ===

func fullRecord() Record {
	return Record{
		Network:       "ignored-by-encode", // Network 不进 value，encode 忽略
		Country:       "Australia",
		CountryCode:   "AU",
		Continent:     "Oceania",
		ContinentCode: "OC",
		ASN:           "AS13335",
		ASName:        "Cloudflare, Inc.",
		ASDomain:      "cloudflare.com",
	}
}

func TestBaseRecordValueV2RoundTripFull(t *testing.T) {
	rec := fullRecord()
	value, err := encodeBaseRecordValueV2(rec)
	if err != nil {
		t.Fatalf("encodeBaseRecordValueV2 error = %v", err)
	}
	decoded, err := decodeBaseRecordValueV2(value)
	if err != nil {
		t.Fatalf("decodeBaseRecordValueV2 error = %v", err)
	}
	// Network 不进 value，decode 返回空（回填责任在 Store）。
	if decoded.Network != "" {
		t.Fatalf("decode 返回 Network 应为空，got %q", decoded.Network)
	}
	want := rec
	want.Network = ""
	if decoded != want {
		t.Fatalf("round-trip mismatch:\n got  %#v\n want %#v", decoded, want)
	}
}

func TestBaseRecordValueV2RoundTripEmptyFields(t *testing.T) {
	rec := Record{} // 全 7 字段为空
	value, err := encodeBaseRecordValueV2(rec)
	if err != nil {
		t.Fatalf("encodeBaseRecordValueV2 error = %v", err)
	}
	decoded, err := decodeBaseRecordValueV2(value)
	if err != nil {
		t.Fatalf("decodeBaseRecordValueV2 error = %v", err)
	}
	if decoded != (Record{}) {
		t.Fatalf("全空字段 round-trip 失败: %#v", decoded)
	}
}

func TestBaseRecordValueV2VersionMismatch(t *testing.T) {
	value, _ := encodeBaseRecordValueV2(fullRecord())
	value[0] = 3 // 篡改 version
	if _, err := decodeBaseRecordValueV2(value); err == nil {
		t.Fatal("version=3 应返回 error")
	}
}

func TestBaseRecordValueV2FlagsNonZero(t *testing.T) {
	value, _ := encodeBaseRecordValueV2(fullRecord())
	value[1] = 1 // 篡改 flags（unknown flags）
	if _, err := decodeBaseRecordValueV2(value); err == nil {
		t.Fatal("flags=1 应返回 error")
	}
}

func TestBaseRecordValueV2TruncatedUvarint(t *testing.T) {
	value, _ := encodeBaseRecordValueV2(fullRecord())
	// 在某字段长度 uvarint 中间截断（保留头 2B + 部分）
	if _, err := decodeBaseRecordValueV2(value[:3]); err == nil {
		t.Fatal("截断 uvarint 应返回 error")
	}
}

func TestBaseRecordValueV2TruncatedContent(t *testing.T) {
	value, _ := encodeBaseRecordValueV2(fullRecord())
	// 声明字段长度但内容被截断
	if _, err := decodeBaseRecordValueV2(value[:5]); err == nil {
		t.Fatal("截断字段内容应返回 error")
	}
}

func TestBaseRecordValueV2ExtraTrailingBytes(t *testing.T) {
	value, _ := encodeBaseRecordValueV2(fullRecord())
	value = append(value, 0xFF, 0xFF) // 多余尾部
	if _, err := decodeBaseRecordValueV2(value); err == nil {
		t.Fatal("多余尾部字节应返回 error")
	}
}

// CR-002: 超大字段长度（超过剩余 value）必须返回 error 而非 panic/溢出。
// 构造合法 version/flags + 一个声明了巨大 fieldLen 但实际内容不足的 value。
func TestBaseRecordValueV2OverlargeFieldLen(t *testing.T) {
	// [ver=2][flags=0][uvarint: 巨大长度][少量内容]
	// uvarint 编码 0xFFFFFFFFFFFFFF（接近 uint64 上限，int 转换会溢出）
	overlarge := []byte{baseValueVersionV2, valueFlagsNone}
	// uvarint of 0xFFFFFFFFFFFFFF = 0xFF 0xFF 0xFF 0xFF 0xFF 0xFF 0xFF 0x7F
	overlarge = append(overlarge, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F)
	overlarge = append(overlarge, 'x') // 实际只有 1 字节内容
	if _, err := decodeBaseRecordValueV2(overlarge); err == nil {
		t.Fatal("超大 fieldLen 应返回 error，不应 panic 或成功")
	}
}

func TestOverlayRecordValueV1OverlargeSourceLen(t *testing.T) {
	// 先 encode 一个合法 overlay value（全空 record + 空 source + 永不过期），
	// 然后把 sourceLen 字段篡改为巨大值。
	base, err := encodeOverlayRecordValueV1(Record{}, OverlayMeta{Source: ""})
	if err != nil {
		t.Fatalf("encode error = %v", err)
	}
	// 全空 record 的 7 字段各占 1B uvarint(0) = 7B；ver+flags=2B；sourceLen=1B uvarint(0)。
	// sourceLen 位置 = offset 9。篡改它为巨大 uvarint。
	// 替换 [offset 9 .. 10) 这 1 字节的 uvarint(0) 为 8 字节巨大 uvarint。
	head := append([]byte{}, base[:9]...)  // ver..第7字段
	tail := append([]byte{}, base[10:]...) // source 内容(空) + 时间戳
	malformed := append(head, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F)
	malformed = append(malformed, tail...)
	if _, _, err := decodeOverlayRecordValueV1(malformed); err == nil {
		t.Fatal("超大 sourceLen 应返回 error，不应 panic 或成功")
	}
}

func TestOverlayRecordValueV1RoundTrip(t *testing.T) {
	rec := fullRecord()
	meta := OverlayMeta{
		Source:    "ipinfo",
		FetchedAt: time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Unix(1_700_086_400, 0).UTC(),
	}
	value, err := encodeOverlayRecordValueV1(rec, meta)
	if err != nil {
		t.Fatalf("encodeOverlayRecordValueV1 error = %v", err)
	}
	gotRec, gotMeta, err := decodeOverlayRecordValueV1(value)
	if err != nil {
		t.Fatalf("decodeOverlayRecordValueV1 error = %v", err)
	}
	want := rec
	want.Network = ""
	if gotRec != want {
		t.Fatalf("record round-trip mismatch:\n got  %#v\n want %#v", gotRec, want)
	}
	if gotMeta.Source != meta.Source {
		t.Fatalf("source mismatch: got %q want %q", gotMeta.Source, meta.Source)
	}
	if !gotMeta.FetchedAt.Equal(meta.FetchedAt) {
		t.Fatalf("fetchedAt mismatch: got %v want %v", gotMeta.FetchedAt, meta.FetchedAt)
	}
	if gotMeta.ExpiresAt.IsZero() {
		t.Fatal("expiresAt 不应为零值（永不过期）")
	}
	if !gotMeta.ExpiresAt.Equal(meta.ExpiresAt) {
		t.Fatalf("expiresAt mismatch: got %v want %v", gotMeta.ExpiresAt, meta.ExpiresAt)
	}
}

// expiresAtUnix==0 表示永不过期：OverlayMeta{ExpiresAt: time.Time{}} 编码后
// expiresAtUnix 为整型 0，decode 还原回零值（永不过期）。
func TestOverlayRecordValueV1NeverExpires(t *testing.T) {
	rec := fullRecord()
	meta := OverlayMeta{
		Source:    "",
		FetchedAt: time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Time{}, // 零值 = 永不过期
	}
	value, err := encodeOverlayRecordValueV1(rec, meta)
	if err != nil {
		t.Fatalf("encodeOverlayRecordValueV1 error = %v", err)
	}

	// 校验磁盘上 expiresAt 字段确为整型 0。
	// 布局：[ver:1][flags:1] + 7字段 + [sourceLen uvarint][source] + [fetchedAt:8][expiresAt:8]
	// source="" 时 sourceLen=0，占 1B。expiresAt 是最后 8B。
	expiresAtUnix := binary.BigEndian.Uint64(value[len(value)-8:])
	if expiresAtUnix != 0 {
		t.Fatalf("expiresAtUnix 应为 0（永不过期），got %d", expiresAtUnix)
	}

	gotRec, gotMeta, err := decodeOverlayRecordValueV1(value)
	if err != nil {
		t.Fatalf("decodeOverlayRecordValueV1 error = %v", err)
	}
	if !gotMeta.ExpiresAt.IsZero() {
		t.Fatalf("永不过期 decode 后 ExpiresAt 应为零值，got %v", gotMeta.ExpiresAt)
	}
	if gotMeta.Source != "" {
		t.Fatalf("空 source round-trip 失败: %q", gotMeta.Source)
	}
	want := rec
	want.Network = ""
	if gotRec != want {
		t.Fatalf("record mismatch: %#v", gotRec)
	}
}

func TestOverlayRecordValueV1VersionMismatch(t *testing.T) {
	value, _, _ := encodeOverlayRecordValueV1AndDecode(t, fullRecord())
	value[0] = 9
	if _, _, err := decodeOverlayRecordValueV1(value); err == nil {
		t.Fatal("overlay version=9 应返回 error")
	}
}

func TestOverlayRecordValueV1FlagsNonZero(t *testing.T) {
	value, _, _ := encodeOverlayRecordValueV1AndDecode(t, fullRecord())
	value[1] = 2
	if _, _, err := decodeOverlayRecordValueV1(value); err == nil {
		t.Fatal("overlay flags=2 应返回 error")
	}
}

func TestOverlayRecordValueV1Truncated(t *testing.T) {
	value, _ := encodeOverlayRecordValueV1(fullRecord(), OverlayMeta{
		Source:    "ipinfo",
		FetchedAt: time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Time{},
	})
	// 截断到时间戳字段前
	if _, _, err := decodeOverlayRecordValueV1(value[:len(value)-10]); err == nil {
		t.Fatal("时间戳截断应返回 error")
	}
}

func TestOverlayRecordValueV1ExtraTrailingBytes(t *testing.T) {
	value, _ := encodeOverlayRecordValueV1(fullRecord(), OverlayMeta{
		Source:    "ipinfo",
		FetchedAt: time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Time{},
	})
	value = append(value, 0xAA, 0xBB)
	if _, _, err := decodeOverlayRecordValueV1(value); err == nil {
		t.Fatal("多余尾部字节应返回 error")
	}
}

func encodeOverlayRecordValueV1AndDecode(t *testing.T, rec Record) ([]byte, Record, OverlayMeta) {
	t.Helper()
	meta := OverlayMeta{
		Source:    "ipinfo",
		FetchedAt: time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Unix(1_700_086_400, 0).UTC(),
	}
	value, err := encodeOverlayRecordValueV1(rec, meta)
	if err != nil {
		t.Fatalf("encode error = %v", err)
	}
	return value, rec, meta
}

// CR-003: 验收第 12 条——overlay value 全 7 个业务字段为空字符串时编解码正确。
// 原有 overlay 测试都用 fullRecord()，未覆盖空 record。
func TestOverlayRecordValueV1AllEmptyFields(t *testing.T) {
	emptyRec := Record{} // 全 7 业务字段为空
	meta := OverlayMeta{
		Source:    "ipinfo",
		FetchedAt: time.Unix(1_700_000_000, 0).UTC(),
		ExpiresAt: time.Unix(1_700_086_400, 0).UTC(),
	}
	value, err := encodeOverlayRecordValueV1(emptyRec, meta)
	if err != nil {
		t.Fatalf("encode error = %v", err)
	}
	gotRec, gotMeta, err := decodeOverlayRecordValueV1(value)
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}
	// 全字段应为空，Network 同样为空（不进 value）。
	if gotRec != (Record{}) {
		t.Fatalf("全空业务字段 round-trip 失败: %#v", gotRec)
	}
	if gotMeta.Source != meta.Source || !gotMeta.FetchedAt.Equal(meta.FetchedAt) {
		t.Fatalf("meta round-trip 失败: %#v", gotMeta)
	}
}
