package ipdb

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime/debug"
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

// BuildFromCSV 将 CSV 构建为 Pebble 离线库（ipdb-v2-query 收口：委托 v2 builder）。
//
// 收口后产出 v2 格式库（primary/cidr 双索引 + base value v2 + FormatVersion=2 +
// SchemaFeatures=PrimaryLPM|CIDRStartIdx）。v1 构建内部（单索引/overlap reject）
// 已由 buildV2FromCSV 取代。
func BuildFromCSV(rootDir string, opts BuildOptions) (Metadata, error) {
	return buildV2FromCSV(rootDir, opts)
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
