package ipdb

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

var metadataKey = []byte{keyFamilyMeta, 'm', 'e', 't', 'a'}

// encodePrefixKey 将 CIDR 前缀编码为 Pebble key。
func encodePrefixKey(prefix netip.Prefix) ([]byte, error) {
	return encodeAddrKey(prefix.Masked().Addr())
}

// encodeAddrKey 将查询 IP 编码为 Pebble key。
func encodeAddrKey(addr netip.Addr) ([]byte, error) {
	switch {
	case addr.Is4():
		raw := addr.As4()
		key := make([]byte, 1+len(raw))
		key[0] = keyFamilyIPv4
		copy(key[1:], raw[:])
		return key, nil
	case addr.Is6():
		raw := addr.As16()
		key := make([]byte, 1+len(raw))
		key[0] = keyFamilyIPv6
		copy(key[1:], raw[:])
		return key, nil
	default:
		return nil, fmt.Errorf("不支持的 IP family: %s", addr.String())
	}
}

// decodeKeyAddr 从 Pebble key 中还原起始地址。
func decodeKeyAddr(key []byte) (netip.Addr, byte, error) {
	if len(key) == 0 {
		return netip.Addr{}, 0, fmt.Errorf("空 key")
	}

	switch key[0] {
	case keyFamilyIPv4:
		if len(key) != 5 {
			return netip.Addr{}, 0, fmt.Errorf("IPv4 key 长度非法: %d", len(key))
		}
		var raw [4]byte
		copy(raw[:], key[1:])
		return netip.AddrFrom4(raw), key[0], nil
	case keyFamilyIPv6:
		if len(key) != 17 {
			return netip.Addr{}, 0, fmt.Errorf("IPv6 key 长度非法: %d", len(key))
		}
		var raw [16]byte
		copy(raw[:], key[1:])
		return netip.AddrFrom16(raw), key[0], nil
	default:
		return netip.Addr{}, 0, fmt.Errorf("未知 key family: %d", key[0])
	}
}

// boundsForAddr 返回同一 family 的迭代边界。
func boundsForAddr(addr netip.Addr) ([]byte, []byte, error) {
	switch {
	case addr.Is4():
		return []byte{keyFamilyIPv4}, []byte{keyFamilyIPv4 + 1}, nil
	case addr.Is6():
		return []byte{keyFamilyIPv6}, []byte{keyFamilyIPv6 + 1}, nil
	default:
		return nil, nil, fmt.Errorf("不支持的 IP family: %s", addr.String())
	}
}

// encodeRecordValue 编码离线记录。
func encodeRecordValue(prefixLen int, record Record) ([]byte, error) {
	if prefixLen < 0 || prefixLen > 128 {
		return nil, fmt.Errorf("prefix 长度非法: %d", prefixLen)
	}

	value := make([]byte, 0, 128)
	value = append(value, currentFormatVersion, byte(prefixLen))

	fields := []string{
		record.Country,
		record.CountryCode,
		record.Continent,
		record.ContinentCode,
		record.ASN,
		record.ASName,
		record.ASDomain,
	}

	for _, field := range fields {
		value = binary.AppendUvarint(value, uint64(len(field)))
		value = append(value, field...)
	}

	return value, nil
}

// decodeRecordValue 解码离线记录。
func decodeRecordValue(value []byte, startAddr netip.Addr) (Record, error) {
	if len(value) < 2 {
		return Record{}, fmt.Errorf("value 长度非法: %d", len(value))
	}
	if value[0] != currentFormatVersion {
		return Record{}, fmt.Errorf("不支持的格式版本: %d", value[0])
	}

	prefixLen := int(value[1])
	maxBits := 128
	if startAddr.Is4() {
		maxBits = 32
	}
	if prefixLen > maxBits {
		return Record{}, fmt.Errorf("prefix 长度非法: %d", prefixLen)
	}
	offset := 2
	fields := make([]string, 0, 7)

	for len(fields) < 7 {
		fieldLen, n := binary.Uvarint(value[offset:])
		if n <= 0 {
			return Record{}, fmt.Errorf("value 长度编码非法")
		}
		offset += n

		if offset+int(fieldLen) > len(value) {
			return Record{}, fmt.Errorf("value 内容截断")
		}

		fields = append(fields, string(value[offset:offset+int(fieldLen)]))
		offset += int(fieldLen)
	}

	prefix := netip.PrefixFrom(startAddr, prefixLen)
	return Record{
		Network:       prefix.String(),
		Country:       fields[0],
		CountryCode:   fields[1],
		Continent:     fields[2],
		ContinentCode: fields[3],
		ASN:           fields[4],
		ASName:        fields[5],
		ASDomain:      fields[6],
	}, nil
}

// === v2 codec（primary / cidr / overlay 三套 key + 两套 value 协议） ===
//
// v2 key 布局（首字节 kind 区分索引类型）：
//
//	primary key（LPM 主索引）：[kind][prefixLen:1B][maskedAddr: 4B|16B]
//	cidr key（二级索引）   ：[kind][maskedAddr: 4B|16B][prefixLen:1B]   value 零长度
//	overlay key            ：[kind][addr: 4B|16B]                       仅 /32、/128
//
// 流程级约束：
//   - encode 入参必须已 Masked()（primary/cidr），否则返回 error；codec 不内部 Mask。
//   - invalid Prefix（PrefixFrom 返回 Bits()==-1，如越界 prefixLen）视为越界返回 error。
//   - decode 时 key 长度必须精确匹配，截断或多余字节返回 error。
//   - primary ↔ cidr 对同一网段必须可互相还原出同一个 netip.Prefix。

// addrBytes 返回纯地址字节（不含 kind 前缀），供 v2 key 组装复用。
func addrBytes(addr netip.Addr) ([]byte, error) {
	switch {
	case addr.Is4():
		raw := addr.As4()
		return raw[:], nil
	case addr.Is6():
		raw := addr.As16()
		return raw[:], nil
	default:
		return nil, fmt.Errorf("不支持的 IP family: %s", addr.String())
	}
}

// validatePrefixV2 校验 primary/cidr key encode 的前置条件：
// prefix 合法、已 Masked、prefixLen 在单字节范围内、addr 非 IPv4-mapped IPv6。
// IPv4-mapped IPv6（::ffff:x.x.x.x，Is4In6()==true）被拒绝：family 二义会让
// LookupIP(v4addr) 用 V4 ladder 永远查不到被 encode 成 V6 key 的记录。
func validatePrefixV2(p netip.Prefix) error {
	if !p.IsValid() {
		// PrefixFrom 返回 Bits()==-1 的 invalid Prefix（如越界 prefixLen）。
		return fmt.Errorf("prefix 非法或 prefixLen 越界: %s", p.String())
	}
	if p.Addr().Is4In6() {
		return fmt.Errorf("不支持 IPv4-mapped IPv6: %s", p.String())
	}
	masked := p.Masked()
	if p.Addr() != masked.Addr() {
		return fmt.Errorf("prefix 未 Masked: %s", p.String())
	}
	maxBits := 128
	if p.Addr().Is4() {
		maxBits = 32
	}
	if bits := p.Bits(); bits < 0 || bits > maxBits {
		return fmt.Errorf("prefixLen 越界: %d", bits)
	}
	return nil
}

// decodePrefixV2 校验 decoder 从 key 字节还原出的 (addr, prefixLen)：
// family 与 kind 推断一致、Prefix 合法（IsValid）、prefixLen 在 family 上限内、
// addr 已 masked（key 布局契约要求 maskedAddr，带 host bits 视为损坏）。
// 与 encode 的 validatePrefixV2 对称：encode 校验调用方传入的 Prefix，
// decode 校验从 key 字节还原出的 addr+prefixLen。
func decodePrefixV2(addr netip.Addr, prefixLen int, isV4 bool) (netip.Prefix, error) {
	// family 与 kind 推断一致。
	if addr.Is4In6() || addr.Is4() != isV4 {
		return netip.Prefix{}, fmt.Errorf("addr family 与 key kind 不一致: addr=%s isV4=%v", addr.String(), isV4)
	}
	maxBits := 128
	if isV4 {
		maxBits = 32
	}
	if prefixLen < 0 || prefixLen > maxBits {
		return netip.Prefix{}, fmt.Errorf("prefixLen 越界: %d", prefixLen)
	}
	p := netip.PrefixFrom(addr, prefixLen)
	if !p.IsValid() {
		return netip.Prefix{}, fmt.Errorf("还原的 prefix 非法: %s", p.String())
	}
	// key 布局契约要求 maskedAddr；带 host bits 视为损坏 key。
	if p.Addr() != p.Masked().Addr() {
		return netip.Prefix{}, fmt.Errorf("key 中 addr 带 host bits（非 masked）: %s", p.String())
	}
	return p, nil
}

func encodePrimaryKeyV2(p netip.Prefix) ([]byte, error) {
	if err := validatePrefixV2(p); err != nil {
		return nil, err
	}
	var kind byte
	switch {
	case p.Addr().Is4():
		kind = keyKindPrimaryV4
	case p.Addr().Is6():
		kind = keyKindPrimaryV6
	default:
		return nil, fmt.Errorf("不支持的 IP family: %s", p.Addr().String())
	}
	addrRaw, err := addrBytes(p.Addr())
	if err != nil {
		return nil, err
	}
	key := make([]byte, 1+1+len(addrRaw))
	key[0] = kind
	key[1] = byte(p.Bits())
	copy(key[2:], addrRaw)
	return key, nil
}

func decodePrimaryKeyV2(key []byte) (netip.Prefix, error) {
	if len(key) == 0 {
		return netip.Prefix{}, fmt.Errorf("空 key")
	}
	kind := key[0]
	var addrLen int
	var isV4 bool
	switch kind {
	case keyKindPrimaryV4:
		addrLen = 4
		isV4 = true
	case keyKindPrimaryV6:
		addrLen = 16
	default:
		return netip.Prefix{}, fmt.Errorf("primary key kind 未知: %d", kind)
	}
	// primary key = [kind:1][prefixLen:1][addr:addrLen]
	if len(key) != 1+1+addrLen {
		return netip.Prefix{}, fmt.Errorf("primary key 长度非法: %d", len(key))
	}
	prefixLen := int(key[1])
	addr, err := addrFromBytes(key[2:2+addrLen], isV4)
	if err != nil {
		return netip.Prefix{}, err
	}
	return decodePrefixV2(addr, prefixLen, isV4)
}

func encodeCIDRKeyV2(p netip.Prefix) ([]byte, error) {
	if err := validatePrefixV2(p); err != nil {
		return nil, err
	}
	var kind byte
	switch {
	case p.Addr().Is4():
		kind = keyKindCIDRV4
	case p.Addr().Is6():
		kind = keyKindCIDRV6
	default:
		return nil, fmt.Errorf("不支持的 IP family: %s", p.Addr().String())
	}
	addrRaw, err := addrBytes(p.Addr())
	if err != nil {
		return nil, err
	}
	// cidr key = [kind:1][addr][prefixLen:1]，与 primary 顺序相反。
	key := make([]byte, 1+len(addrRaw)+1)
	key[0] = kind
	copy(key[1:1+len(addrRaw)], addrRaw)
	key[1+len(addrRaw)] = byte(p.Bits())
	return key, nil
}

func decodeCIDRKeyV2(key []byte) (netip.Prefix, error) {
	if len(key) == 0 {
		return netip.Prefix{}, fmt.Errorf("空 key")
	}
	kind := key[0]
	var addrLen int
	var isV4 bool
	switch kind {
	case keyKindCIDRV4:
		addrLen = 4
		isV4 = true
	case keyKindCIDRV6:
		addrLen = 16
	default:
		return netip.Prefix{}, fmt.Errorf("cidr key kind 未知: %d", kind)
	}
	// cidr key = [kind:1][addr:addrLen][prefixLen:1]
	if len(key) != 1+addrLen+1 {
		return netip.Prefix{}, fmt.Errorf("cidr key 长度非法: %d", len(key))
	}
	prefixLen := int(key[1+addrLen])
	addr, err := addrFromBytes(key[1:1+addrLen], isV4)
	if err != nil {
		return netip.Prefix{}, err
	}
	return decodePrefixV2(addr, prefixLen, isV4)
}

func encodeOverlayKeyV2(a netip.Addr) ([]byte, error) {
	// 与 validatePrefixV2 一致：拒绝 IPv4-mapped IPv6，避免 family 二义。
	if a.Is4In6() {
		return nil, fmt.Errorf("不支持 IPv4-mapped IPv6: %s", a.String())
	}
	var kind byte
	switch {
	case a.Is4():
		kind = keyKindOverlayV4
	case a.Is6():
		kind = keyKindOverlayV6
	default:
		return nil, fmt.Errorf("不支持的 IP family: %s", a.String())
	}
	addrRaw, err := addrBytes(a)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 1+len(addrRaw))
	key[0] = kind
	copy(key[1:], addrRaw)
	return key, nil
}

func decodeOverlayKeyV2(key []byte) (netip.Addr, error) {
	if len(key) == 0 {
		return netip.Addr{}, fmt.Errorf("空 key")
	}
	kind := key[0]
	var addrLen int
	var isV4 bool
	switch kind {
	case keyKindOverlayV4:
		addrLen = 4
		isV4 = true
	case keyKindOverlayV6:
		addrLen = 16
	default:
		return netip.Addr{}, fmt.Errorf("overlay key kind 未知: %d", kind)
	}
	// overlay key = [kind:1][addr:addrLen]
	if len(key) != 1+addrLen {
		return netip.Addr{}, fmt.Errorf("overlay key 长度非法: %d", len(key))
	}
	return addrFromBytes(key[1:1+addrLen], isV4)
}

// addrFromBytes 从纯地址字节还原 netip.Addr。
func addrFromBytes(b []byte, isV4 bool) (netip.Addr, error) {
	if isV4 {
		var raw [4]byte
		copy(raw[:], b)
		return netip.AddrFrom4(raw), nil
	}
	var raw [16]byte
	copy(raw[:], b)
	return netip.AddrFrom16(raw), nil
}

// === v2 value 协议（base value v2 与 overlay value v1 两套独立协议） ===
//
// 与库级 FormatVersion 解耦：这是 value 自身的协议版本号。
// base value v2：[ver:1=2][flags:1=0] + 7×(uvarint len + UTF-8 字段)
//
//	7 字段顺序固定（去掉 prefixLen，已进 key；不含 Network，由 Store 据 key 回填）：
//	Country, CountryCode, Continent, ContinentCode, ASN, ASName, ASDomain
//
// overlay value v1：[ver:1=1][flags:1=0] + 7 字段（同上顺序）
//
//   - [sourceLen uvarint][source bytes]
//   - [fetchedAtUnix: int64 BE][expiresAtUnix: int64 BE]   expiresAtUnix==0 永不过期
//
// 两套协议各自独立演进 version/flags，不共用 flagOverlayMeta。
const (
	baseValueVersionV2    byte = 2
	overlayValueVersionV1 byte = 1
	valueFlagsNone        byte = 0
)

// recordFields 返回 7 个业务字段（不含 Network，顺序固定）。
// base 与 overlay value 共用同一字段顺序，但 encode 函数各自独立组装协议头。
func recordFields(rec Record) [7]string {
	return [7]string{
		rec.Country,
		rec.CountryCode,
		rec.Continent,
		rec.ContinentCode,
		rec.ASN,
		rec.ASName,
		rec.ASDomain,
	}
}

// encodeRecordFields 将 7 字段以 uvarint 长度前缀 + UTF-8 编码追加到 dst。
func encodeRecordFields(dst []byte, fields [7]string) []byte {
	for _, f := range fields {
		dst = binary.AppendUvarint(dst, uint64(len(f)))
		dst = append(dst, f...)
	}
	return dst
}

// decodeRecordFields 从 value 的 offset 起解码 7 字段，返回 (fields, nextOffset, err)。
func decodeRecordFields(value []byte, offset int) ([7]string, int, error) {
	var fields [7]string
	for i := 0; i < 7; i++ {
		fieldLen, n := binary.Uvarint(value[offset:])
		if n <= 0 {
			return fields, 0, fmt.Errorf("字段长度编码非法（uvarint 截断）")
		}
		offset += n
		// 防止 fieldLen 超过 int 上限导致 int(fieldLen) 溢出为负、边界检查失真。
		// 先用 uint64 比较，再转 int。
		next, ok := addLenOffset(offset, fieldLen, len(value))
		if !ok {
			return fields, 0, fmt.Errorf("字段长度超过剩余 value（可能溢出或截断）")
		}
		fields[i] = string(value[offset:next])
		offset = next
	}
	return fields, offset, nil
}

// addLenOffset 安全计算 offset + int(fieldLen)，并校验不溢出 int 且不越过 value 末尾。
// 返回 (nextOffset, true) 或 (0, false)。decodeRecordFields 与 overlay source decode 共用。
func addLenOffset(offset int, fieldLen uint64, valueLen int) (int, bool) {
	if fieldLen > uint64(valueLen-offset) {
		return 0, false
	}
	return offset + int(fieldLen), true
}

func recordFromFields(fields [7]string) Record {
	return Record{
		// Network 留空：由 Store 据 key 的 prefix 回填（decode 返回空 Network）。
		Country:       fields[0],
		CountryCode:   fields[1],
		Continent:     fields[2],
		ContinentCode: fields[3],
		ASN:           fields[4],
		ASName:        fields[5],
		ASDomain:      fields[6],
	}
}

func encodeBaseRecordValueV2(rec Record) ([]byte, error) {
	value := make([]byte, 0, 128)
	value = append(value, baseValueVersionV2, valueFlagsNone)
	value = encodeRecordFields(value, recordFields(rec))
	return value, nil
}

func decodeBaseRecordValueV2(value []byte) (Record, error) {
	if len(value) < 2 {
		return Record{}, fmt.Errorf("value 长度非法: %d", len(value))
	}
	if value[0] != baseValueVersionV2 {
		return Record{}, fmt.Errorf("base value 版本不符: %d", value[0])
	}
	if value[1] != valueFlagsNone {
		return Record{}, fmt.Errorf("base value flags 非零: %d", value[1])
	}
	fields, offset, err := decodeRecordFields(value, 2)
	if err != nil {
		return Record{}, err
	}
	// 消费完所有字节后不得有剩余（防部分写 / 版本串台）。
	if offset != len(value) {
		return Record{}, fmt.Errorf("base value 多余尾部字节: 消费 %d / 总长 %d", offset, len(value))
	}
	return recordFromFields(fields), nil
}

func encodeOverlayRecordValueV1(rec Record, meta OverlayMeta) ([]byte, error) {
	value := make([]byte, 0, 160)
	value = append(value, overlayValueVersionV1, valueFlagsNone)
	value = encodeRecordFields(value, recordFields(rec))
	// source
	value = binary.AppendUvarint(value, uint64(len(meta.Source)))
	value = append(value, meta.Source...)
	// fetchedAt / expiresAt（int64 BE）
	// expiresAtUnix==0 表示永不过期：零值 time.Time 显式写 0，
	// 不得用 time.Time{}.Unix()（后者非 0）。
	value = binary.BigEndian.AppendUint64(value, uint64(meta.FetchedAt.Unix()))
	if meta.ExpiresAt.IsZero() {
		value = binary.BigEndian.AppendUint64(value, 0)
	} else {
		value = binary.BigEndian.AppendUint64(value, uint64(meta.ExpiresAt.Unix()))
	}
	return value, nil
}

func decodeOverlayRecordValueV1(value []byte) (Record, OverlayMeta, error) {
	if len(value) < 2 {
		return Record{}, OverlayMeta{}, fmt.Errorf("value 长度非法: %d", len(value))
	}
	if value[0] != overlayValueVersionV1 {
		return Record{}, OverlayMeta{}, fmt.Errorf("overlay value 版本不符: %d", value[0])
	}
	if value[1] != valueFlagsNone {
		return Record{}, OverlayMeta{}, fmt.Errorf("overlay value flags 非零: %d", value[1])
	}
	fields, offset, err := decodeRecordFields(value, 2)
	if err != nil {
		return Record{}, OverlayMeta{}, err
	}

	// source
	sourceLen, n := binary.Uvarint(value[offset:])
	if n <= 0 {
		return Record{}, OverlayMeta{}, fmt.Errorf("source 长度编码非法（uvarint 截断）")
	}
	offset += n
	// 同 decodeRecordFields：防止 sourceLen 超过 int 上限导致溢出。
	srcEnd, ok := addLenOffset(offset, sourceLen, len(value))
	if !ok {
		return Record{}, OverlayMeta{}, fmt.Errorf("source 长度超过剩余 value（可能溢出或截断）")
	}
	source := string(value[offset:srcEnd])
	offset = srcEnd

	// fetchedAt / expiresAt（各 8B BE）
	if offset+16 > len(value) {
		return Record{}, OverlayMeta{}, fmt.Errorf("时间戳字段截断")
	}
	fetchedAtUnix := int64(binary.BigEndian.Uint64(value[offset : offset+8]))
	offset += 8
	expiresAtUnix := int64(binary.BigEndian.Uint64(value[offset : offset+8]))
	offset += 8

	// 消费完所有字节后不得有剩余。
	if offset != len(value) {
		return Record{}, OverlayMeta{}, fmt.Errorf("overlay value 多余尾部字节: 消费 %d / 总长 %d", offset, len(value))
	}

	meta := OverlayMeta{
		Source:    source,
		FetchedAt: time.Unix(fetchedAtUnix, 0).UTC(),
	}
	if expiresAtUnix != 0 {
		meta.ExpiresAt = time.Unix(expiresAtUnix, 0).UTC()
	}
	// expiresAtUnix==0 → meta.ExpiresAt 保持零值 = 永不过期。
	return recordFromFields(fields), meta, nil
}
