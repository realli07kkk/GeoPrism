package ipdb

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

var expectedCSVHeader = []string{
	"network",
	"country",
	"country_code",
	"continent",
	"continent_code",
	"asn",
	"as_name",
	"as_domain",
}

const batchCommitSize = 10000

// BuildFromCSV 将 CSV 构建为 Pebble 离线库。
func BuildFromCSV(rootDir string, opts BuildOptions) (Metadata, error) {
	if !filepath.IsAbs(opts.CSVPath) {
		return Metadata{}, fmt.Errorf("CSV 路径必须是绝对路径: %s", opts.CSVPath)
	}

	csvFile, err := os.Open(opts.CSVPath)
	if err != nil {
		return Metadata{}, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer csvFile.Close()

	buildID := opts.BuildID
	if buildID == "" {
		buildID = time.Now().UTC().Format("20060102T150405")
	}

	builtAt := opts.BuiltAt
	if builtAt.IsZero() {
		builtAt = time.Now().UTC()
	}

	versionsDir := filepath.Join(rootDir, versionsDirName)
	buildDir := filepath.Join(versionsDir, buildID)
	dbDir := filepath.Join(buildDir, dbDirName)

	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return Metadata{}, fmt.Errorf("创建离线库目录失败: %w", err)
	}

	db, err := pebble.Open(dbDir, &pebble.Options{
		Logger: silentLogger{},
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("打开 Pebble 数据库失败: %w", err)
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()

	reader := csv.NewReader(csvFile)
	reader.FieldsPerRecord = len(expectedCSVHeader)
	reader.ReuseRecord = true

	header, err := reader.Read()
	if err != nil {
		return Metadata{}, fmt.Errorf("读取 CSV 头失败: %w", err)
	}
	if !slices.Equal(header, expectedCSVHeader) {
		return Metadata{}, fmt.Errorf("CSV 头不匹配: %v", header)
	}

	type familyState struct {
		prevStart []byte
		prevEnd   []byte
	}

	states := map[byte]*familyState{
		keyFamilyIPv4: {},
		keyFamilyIPv6: {},
	}

	var (
		rowCount    int64
		ipv4Count   int64
		ipv6Count   int64
		pendingRows int
	)

	batch := db.NewBatch()
	defer func() {
		if batch != nil {
			_ = batch.Close()
		}
	}()

	commitBatch := func() error {
		if pendingRows == 0 {
			return nil
		}
		if err := batch.Commit(pebble.NoSync); err != nil {
			return err
		}
		if err := batch.Close(); err != nil {
			return err
		}
		batch = db.NewBatch()
		pendingRows = 0
		return nil
	}

	for lineNo := 2; ; lineNo++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Metadata{}, fmt.Errorf("读取 CSV 第 %d 行失败: %w", lineNo, err)
		}

		prefix, err := parseNetworkField(record[0])
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行 network 非法: %w", lineNo, err)
		}
		if prefix.Addr() != prefix.Masked().Addr() {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行 network 不是规范网段: %s", lineNo, record[0])
		}

		key, err := encodePrefixKey(prefix)
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行编码 key 失败: %w", lineNo, err)
		}

		endAddr, err := prefixLastAddr(prefix)
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行计算区间末尾失败: %w", lineNo, err)
		}
		endKey, err := encodeAddrKey(endAddr)
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行编码区间末尾失败: %w", lineNo, err)
		}

		family := key[0]
		state := states[family]
		startBytes := key[1:]
		endBytes := endKey[1:]

		if len(state.prevStart) > 0 && bytes.Compare(startBytes, state.prevStart) < 0 {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行出现乱序网段: %s", lineNo, record[0])
		}
		if len(state.prevEnd) > 0 && bytes.Compare(startBytes, state.prevEnd) <= 0 {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行出现重叠网段: %s", lineNo, record[0])
		}

		decoded := Record{
			Country:       strings.TrimSpace(record[1]),
			CountryCode:   strings.TrimSpace(record[2]),
			Continent:     strings.TrimSpace(record[3]),
			ContinentCode: strings.TrimSpace(record[4]),
			ASN:           strings.TrimSpace(record[5]),
			ASName:        strings.TrimSpace(record[6]),
			ASDomain:      strings.TrimSpace(record[7]),
		}

		value, err := encodeRecordValue(prefix.Bits(), decoded)
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行编码 value 失败: %w", lineNo, err)
		}

		if err := batch.Set(key, value, nil); err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行写入 Pebble 失败: %w", lineNo, err)
		}

		state.prevStart = append(state.prevStart[:0], startBytes...)
		state.prevEnd = append(state.prevEnd[:0], endBytes...)

		rowCount++
		pendingRows++
		if family == keyFamilyIPv4 {
			ipv4Count++
		} else {
			ipv6Count++
		}

		if pendingRows >= batchCommitSize {
			if err := commitBatch(); err != nil {
				return Metadata{}, fmt.Errorf("提交 Pebble 批次失败: %w", err)
			}
		}
	}

	if err := commitBatch(); err != nil {
		return Metadata{}, fmt.Errorf("提交最后一个 Pebble 批次失败: %w", err)
	}
	if err := batch.Close(); err != nil {
		return Metadata{}, fmt.Errorf("关闭最后一个 Pebble 批次失败: %w", err)
	}
	batch = nil

	pebbleModulePath, pebbleVersion := currentPebbleBuildInfo()

	meta := Metadata{
		FormatVersion: int(currentFormatVersion),
		SourceCSV:     opts.CSVPath,
		BuiltAt:       builtAt,
		RowCount:      rowCount,
		IPv4Count:     ipv4Count,
		IPv6Count:     ipv6Count,
		Builder:       "geoprism",
		PebbleModule:  pebbleModulePath,
		PebbleVersion: pebbleVersion,
	}

	metaValue, err := json.Marshal(meta)
	if err != nil {
		return Metadata{}, fmt.Errorf("编码元数据失败: %w", err)
	}

	if err := db.Set(metadataKey, metaValue, pebble.NoSync); err != nil {
		return Metadata{}, fmt.Errorf("写入元数据失败: %w", err)
	}
	if err := db.Flush(); err != nil {
		return Metadata{}, fmt.Errorf("刷新 Pebble 数据失败: %w", err)
	}
	if err := db.Close(); err != nil {
		return Metadata{}, fmt.Errorf("关闭 Pebble 数据库失败: %w", err)
	}
	db = nil

	if err := writeCurrentVersion(rootDir, buildID); err != nil {
		return Metadata{}, fmt.Errorf("切换 CURRENT 失败: %w", err)
	}

	cleanupOldVersions(rootDir, buildID)
	return meta, nil
}

func writeCurrentVersion(rootDir, buildID string) error {
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return err
	}

	currentPath := filepath.Join(rootDir, currentFileName)
	tmpPath := currentPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(buildID), 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, currentPath)
}

func cleanupOldVersions(rootDir, currentBuildID string) {
	versionsDir := filepath.Join(rootDir, versionsDirName)
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == currentBuildID {
			continue
		}
		_ = os.RemoveAll(filepath.Join(versionsDir, entry.Name()))
	}
}

func prefixLastAddr(prefix netip.Prefix) (netip.Addr, error) {
	masked := prefix.Masked()
	bits := prefix.Bits()

	switch {
	case masked.Addr().Is4():
		addrBytes := masked.Addr().As4()
		last := addrBytes
		for bit := bits; bit < 32; bit++ {
			byteIdx := bit / 8
			mask := byte(1 << (7 - (bit % 8)))
			last[byteIdx] |= mask
		}
		return netip.AddrFrom4(last), nil
	case masked.Addr().Is6():
		addrBytes := masked.Addr().As16()
		last := addrBytes
		for bit := bits; bit < 128; bit++ {
			byteIdx := bit / 8
			mask := byte(1 << (7 - (bit % 8)))
			last[byteIdx] |= mask
		}
		return netip.AddrFrom16(last), nil
	default:
		return netip.Addr{}, fmt.Errorf("不支持的 IP family: %s", masked.Addr().String())
	}
}

func currentPebbleBuildInfo() (string, string) {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return defaultPebbleModulePath, defaultPebbleVersion
	}

	for _, dep := range buildInfo.Deps {
		if dep == nil || dep.Path != defaultPebbleModulePath {
			continue
		}
		version := dep.Version
		if version == "" {
			version = defaultPebbleVersion
		}
		return dep.Path, version
	}

	return defaultPebbleModulePath, defaultPebbleVersion
}

func parseNetworkField(network string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(network)
	if err == nil {
		return prefix, nil
	}

	addr, addrErr := netip.ParseAddr(network)
	if addrErr != nil {
		return netip.Prefix{}, err
	}

	switch {
	case addr.Is4():
		return netip.PrefixFrom(addr, 32), nil
	case addr.Is6():
		return netip.PrefixFrom(addr, 128), nil
	default:
		return netip.Prefix{}, err
	}
}
