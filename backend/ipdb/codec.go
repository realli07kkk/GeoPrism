package ipdb

import (
	"encoding/binary"
	"fmt"
	"net/netip"
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
