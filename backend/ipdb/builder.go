package ipdb

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
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
// 调用方若持有 OpenCurrent 返回的 Store，必须先 Close；builder 发布阶段会等待所有
// reader 释放版本生命周期共享锁后再切换 CURRENT 和回收旧版本。
func BuildFromCSV(rootDir string, opts BuildOptions) (Metadata, error) {
	return buildV2FromCSV(rootDir, opts)
}

func writeCurrentVersion(rootDir, buildID string) error {
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return err
	}

	currentPath := filepath.Join(rootDir, currentFileName)
	tmpFile, err := os.CreateTemp(rootDir, currentFileName+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if err := tmpFile.Chmod(0644); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.WriteString(buildID); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, currentPath)
}

// cleanupStagingDirectories 回收上次进程异常退出遗留的 staging。
// 调用方必须持有 BUILD.lock 独占锁，确保不会删除仍在构建的目录。
func cleanupStagingDirectories(rootDir string) error {
	versionsDir := filepath.Join(rootDir, versionsDirName)
	currentBuildID := ""
	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err == nil {
		currentBuildID = strings.TrimSpace(string(currentData))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("清理 staging 前读取 CURRENT 失败: %w", err)
	}

	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return err
	}

	var cleanupErrs []error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == currentBuildID || !strings.HasPrefix(entry.Name(), stagingDirPrefix) {
			continue
		}
		path := filepath.Join(versionsDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("删除遗留 staging %q 失败: %w", path, err))
		}
	}
	return errors.Join(cleanupErrs...)
}

// cleanupOldVersions 只保留当前正式版本，并一并回收异常遗留的 staging。
// 调用方必须同时持有 BUILD.lock 与 VERSIONS.lock 独占锁：前者排除其他 builder，
// 后者等待旧版本 reader 关闭并阻止新 reader 在清理期间进入。
func cleanupOldVersions(rootDir, currentBuildID string) error {
	versionsDir := filepath.Join(rootDir, versionsDirName)
	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil {
		return fmt.Errorf("清理前读取 CURRENT 失败: %w", err)
	}
	diskCurrent := strings.TrimSpace(string(currentData))
	if diskCurrent == "" || diskCurrent != currentBuildID {
		return fmt.Errorf("清理前 CURRENT 校验失败: disk=%q expected=%q", diskCurrent, currentBuildID)
	}
	currentDBDir := filepath.Join(versionsDir, diskCurrent, dbDirName)
	info, err := os.Stat(currentDBDir)
	if err != nil {
		return fmt.Errorf("清理前检查当前数据库失败: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("清理前检查当前数据库失败: %q 不是目录", currentDBDir)
	}

	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return err
	}

	var cleanupErrs []error
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == currentBuildID {
			continue
		}
		path := filepath.Join(versionsDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("删除旧版本 %q 失败: %w", path, err))
		}
	}
	return errors.Join(cleanupErrs...)
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
