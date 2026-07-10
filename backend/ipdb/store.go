package ipdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

var ErrNoCurrentDatabase = errors.New("未找到可用的离线 IP 库")

// Store 是 v2 base 库的公开过渡壳：内部持有 *BaseStore，公开方法转调 v2 真查询。
//
// ipdb-v2-query 收口时引入此壳，使 cli 5 个调用点的 *ipdb.Store 类型与方法签名
// 零 diff；ipdb-lookup-integration 阶段拆 Store、App 改持 *BaseStore/*OverlayStore
// 时清除本壳（roadmap §9 2026-06-22 实施顺序调整）。
type Store struct {
	base    *BaseStore
	rootDir string
	buildID string
}

// OpenCurrent 读 CURRENT 得 buildID 后打开当前激活的 v2 base 库，包成 *Store。
//
// 版本/capability 判定边界（finding 1）：
//   - 读 metadata 失败 / JSON 损坏 / lock / 打不开 → 关 probe DB 后原样上抛普通 error（不包装）
//   - 读到 metadata 且 FormatVersion != 2            → 关 probe DB 后 ErrLegacyFormat（仅此一类）
//   - FormatVersion == 2 但缺 PrimaryLPM|CIDRStartIdx → 关 probe DB 后 ErrIncompleteSchema
//   - 一切就绪                                        → 关 probe DB → 调 openBaseV2 → 包成 *Store
//
// probe DB 资源管理（finding 2）：OpenCurrent 自己 ReadOnly pebble.Open 得到的 probe DB
// 必须在调用 openBaseV2 之前彻底关闭（openBaseV2 会二次打开同目录）。所有错误分支
// （含 ErrLegacyFormat/ErrIncompleteSchema/普通错误）都关 probe DB 再返回。
func OpenCurrent(rootDir string) (*Store, error) {
	if _, err := os.Stat(rootDir); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCurrentDatabase
		}
		return nil, fmt.Errorf("检查离线库根目录失败: %w", err)
	}

	// reader 在读取 CURRENT 前获取共享生命周期锁，并一直持有到 Store.Close。
	// builder 的发布/回收使用同一锁的独占模式，因此 CURRENT 对应目录在 reader
	// 完成打开及后续查询之前不会被切换后删除。
	versionsLock, err := acquireFileLock(filepath.Join(rootDir, versionsLockFileName), false)
	if err != nil {
		return nil, fmt.Errorf("获取 IPDB 版本生命周期锁失败: %w", err)
	}
	defer func() {
		if versionsLock != nil {
			_ = versionsLock.Close()
		}
	}()

	currentPath := filepath.Join(rootDir, currentFileName)
	currentData, err := os.ReadFile(currentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCurrentDatabase
		}
		return nil, fmt.Errorf("读取 CURRENT 失败: %w", err)
	}

	buildID := strings.TrimSpace(string(currentData))
	if buildID == "" {
		return nil, ErrNoCurrentDatabase
	}

	dbDirPath := filepath.Join(rootDir, versionsDirName, buildID, dbDirName)
	if _, err := os.Stat(dbDirPath); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCurrentDatabase
		}
		return nil, fmt.Errorf("检查离线库目录失败: %w", err)
	}

	// probe DB：ReadOnly 打开 + 读 metadata 判版本。确认 v2 后关闭，再调 openBaseV2。
	probeDB, err := pebble.Open(dbDirPath, &pebble.Options{
		ReadOnly: true,
		Logger:   silentLogger{},
	})
	if err != nil {
		return nil, fmt.Errorf("打开离线库失败: %w", err)
	}

	// 读 metadata 失败时仍需关闭 probe DB。metadata 读取失败属"库打不开"，
	// 不是"旧版格式"（finding 1 边界），原样上抛普通 error。
	metaValue, closer, err := probeDB.Get(metadataKey)
	if err != nil {
		_ = probeDB.Close()
		return nil, fmt.Errorf("读取离线库元数据失败: %w", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(metaValue, &metadata); err != nil {
		_ = closer.Close()
		_ = probeDB.Close()
		return nil, fmt.Errorf("解析离线库元数据失败: %w", err)
	}
	if err := closer.Close(); err != nil {
		_ = probeDB.Close()
		return nil, fmt.Errorf("关闭元数据读取器失败: %w", err)
	}

	// 版本判定（finding 1 边界）：只有成功读到 metadata 且 FormatVersion != 2 才判 legacy。
	if metadata.FormatVersion != int(formatVersionV2) {
		_ = probeDB.Close()
		return nil, fmt.Errorf("%w: FormatVersion=%d, want %d", ErrLegacyFormat, metadata.FormatVersion, formatVersionV2)
	}

	// capability 判定：缺 primary|cidr 索引能力视为不完整 schema，提示重建。
	if metadata.SchemaFeatures&SchemaFeaturePrimaryLPM == 0 || metadata.SchemaFeatures&SchemaFeatureCIDRStartIdx == 0 {
		_ = probeDB.Close()
		return nil, fmt.Errorf("%w: SchemaFeatures=%d, want 包含 PrimaryLPM|CIDRStartIdx",
			ErrIncompleteSchema, metadata.SchemaFeatures)
	}

	// 确认是完整 v2 库，关闭 probe DB 后调 openBaseV2（此时 sanity 必过，二次打开无 lock 冲突）。
	if err := probeDB.Close(); err != nil {
		return nil, fmt.Errorf("关闭 probe 库失败: %w", err)
	}

	base, err := openBaseV2(rootDir, buildID)
	if err != nil {
		return nil, err
	}

	// 生命周期锁归真正持有 Pebble 句柄的 BaseStore 所有，确保后续移除 Store 过渡壳时
	// reader lease 不会随壳层丢失。
	base.versionsLock = versionsLock
	store := &Store{
		base:    base,
		rootDir: rootDir,
		buildID: buildID,
	}
	versionsLock = nil
	return store, nil
}

// LookupIP 转调 v2 BaseStore 真 LPM ladder。签名与 v1 零 diff。
func (s *Store) LookupIP(ip string) (Match, error) {
	if s == nil || s.base == nil {
		return Match{IP: ip}, ErrNoCurrentDatabase
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return Match{IP: ip}, fmt.Errorf("IP 格式非法: %w", err)
	}

	rec, matched, err := s.base.LookupIP(addr)
	if err != nil {
		return Match{}, err
	}
	return Match{IP: ip, Matched: matched, Record: rec}, nil
}

// LookupCIDR 转调 v2 BaseStore 三段查询。签名与 v1 零 diff。
func (s *Store) LookupCIDR(cidr string) ([]Record, error) {
	if s == nil || s.base == nil {
		return nil, ErrNoCurrentDatabase
	}

	queryPrefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("CIDR 格式非法: %w", err)
	}
	return s.base.LookupCIDR(queryPrefix)
}

// Metadata 转调 v2 BaseStore.Metadata。
func (s *Store) Metadata() Metadata {
	if s == nil || s.base == nil {
		return Metadata{}
	}
	return s.base.Metadata()
}

// Close 关闭离线库（幂等）。
func (s *Store) Close() error {
	if s == nil || s.base == nil {
		return nil
	}
	base := s.base
	s.base = nil
	return base.Close()
}

// WriteRecord 公开签名保留（归 ipdb-lookup-integration 删除），但 v2 base 库只读，
// 运行期回写本就不被允许（止血语义）。本方法改为显式返回写入失败 error，
// 不再依赖任何 Pebble 句柄、不触发隐式 ReadOnly 失败（finding 3）。
func (s *Store) WriteRecord(record Record) error {
	return errors.New("base 库只读，不支持运行期写入")
}
