package ipdb

import (
	"bytes"
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

// Store 表示当前启用的离线 IP 库。
type Store struct {
	rootDir   string
	buildID   string
	db        *pebble.DB
	metadata  Metadata
	dbDirPath string
}

// OpenCurrent 打开当前启用的离线 IP 库。
func OpenCurrent(rootDir string) (*Store, error) {
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

	db, err := pebble.Open(dbDirPath, &pebble.Options{
		Logger: silentLogger{},
	})
	if err != nil {
		return nil, fmt.Errorf("打开 Pebble 数据库失败: %w", err)
	}

	metaValue, closer, err := db.Get(metadataKey)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("读取离线库元数据失败: %w", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(metaValue, &metadata); err != nil {
		closer.Close()
		db.Close()
		return nil, fmt.Errorf("解析离线库元数据失败: %w", err)
	}
	if err := closer.Close(); err != nil {
		db.Close()
		return nil, fmt.Errorf("关闭元数据读取器失败: %w", err)
	}

	return &Store{
		rootDir:   rootDir,
		buildID:   buildID,
		db:        db,
		metadata:  metadata,
		dbDirPath: dbDirPath,
	}, nil
}

// WriteRecord 向数据库写入单条 /32 或 /128 记录。
func (s *Store) WriteRecord(record Record) error {
	if s == nil || s.db == nil {
		return ErrNoCurrentDatabase
	}

	prefix, err := netip.ParsePrefix(record.Network)
	if err != nil {
		return fmt.Errorf("解析网段失败: %w", err)
	}

	key, err := encodePrefixKey(prefix)
	if err != nil {
		return fmt.Errorf("编码 key 失败: %w", err)
	}

	value, err := encodeRecordValue(prefix.Bits(), record)
	if err != nil {
		return fmt.Errorf("编码 value 失败: %w", err)
	}

	// 复用已有的读写句柄写入
	if err := s.db.Set(key, value, nil); err != nil {
		return fmt.Errorf("写入记录失败: %w", err)
	}

	return nil
}

// Close 关闭离线库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	db := s.db
	s.db = nil
	return db.Close()
}

// Metadata 返回离线库元信息。
func (s *Store) Metadata() Metadata {
	if s == nil {
		return Metadata{}
	}
	return s.metadata
}

// LookupIP 查询单个 IP 的离线信息。
func (s *Store) LookupIP(ip string) (Match, error) {
	if s == nil || s.db == nil {
		return Match{IP: ip}, ErrNoCurrentDatabase
	}

	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return Match{}, fmt.Errorf("IP 格式非法: %w", err)
	}

	queryKey, err := encodeAddrKey(addr)
	if err != nil {
		return Match{}, err
	}

	lowerBound, upperBound, err := boundsForAddr(addr)
	if err != nil {
		return Match{}, err
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	if err != nil {
		return Match{}, fmt.Errorf("创建迭代器失败: %w", err)
	}
	defer iter.Close()

	valid := iter.SeekGE(queryKey)
	if valid && bytes.Compare(iter.Key(), queryKey) > 0 {
		valid = iter.Prev()
	}
	if !valid {
		valid = iter.Last()
	}
	if !valid {
		return Match{IP: ip}, nil
	}

	startAddr, _, err := decodeKeyAddr(iter.Key())
	if err != nil {
		return Match{}, fmt.Errorf("解析命中 key 失败: %w", err)
	}

	record, err := decodeRecordValue(iter.Value(), startAddr)
	if err != nil {
		return Match{}, fmt.Errorf("解析命中 value 失败: %w", err)
	}

	prefix, err := netip.ParsePrefix(record.Network)
	if err != nil {
		return Match{}, fmt.Errorf("恢复命中网段失败: %w", err)
	}
	if !prefix.Contains(addr) {
		return Match{IP: ip}, nil
	}

	return Match{
		IP:      ip,
		Matched: true,
		Record:  record,
	}, nil
}

// LookupCIDR 查询与指定 CIDR 相交的所有离线记录。
func (s *Store) LookupCIDR(cidr string) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, ErrNoCurrentDatabase
	}

	queryPrefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("CIDR 格式非法: %w", err)
	}
	queryPrefix = queryPrefix.Masked()

	queryStartKey, err := encodeAddrKey(queryPrefix.Addr())
	if err != nil {
		return nil, err
	}

	queryEndAddr, err := prefixLastAddr(queryPrefix)
	if err != nil {
		return nil, fmt.Errorf("计算查询区间末尾失败: %w", err)
	}
	queryEndKey, err := encodeAddrKey(queryEndAddr)
	if err != nil {
		return nil, err
	}

	lowerBound, upperBound, err := boundsForAddr(queryPrefix.Addr())
	if err != nil {
		return nil, err
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, fmt.Errorf("创建迭代器失败: %w", err)
	}
	defer iter.Close()

	results := make([]Record, 0)

	// 先检查 queryStart 前一条记录，避免漏掉覆盖 queryStart 的大网段。
	valid := iter.SeekGE(queryStartKey)
	if !valid {
		if !iter.Last() {
			return results, nil
		}
		record, recordPrefix, err := decodeIterRecord(iter)
		if err != nil {
			return nil, err
		}
		if prefixesOverlap(queryPrefix, recordPrefix) {
			results = append(results, record)
		}
		return results, nil
	}

	if bytes.Compare(iter.Key(), queryStartKey) > 0 {
		if iter.Prev() {
			record, recordPrefix, err := decodeIterRecord(iter)
			if err != nil {
				return nil, err
			}
			if prefixesOverlap(queryPrefix, recordPrefix) {
				results = append(results, record)
			}
		}
		valid = iter.SeekGE(queryStartKey)
	}

	for ; valid; valid = iter.Next() {
		if bytes.Compare(iter.Key(), queryEndKey) > 0 {
			break
		}

		record, recordPrefix, err := decodeIterRecord(iter)
		if err != nil {
			return nil, err
		}
		if prefixesOverlap(queryPrefix, recordPrefix) {
			results = append(results, record)
		}
	}

	return results, nil
}

func decodeIterRecord(iter *pebble.Iterator) (Record, netip.Prefix, error) {
	startAddr, _, err := decodeKeyAddr(iter.Key())
	if err != nil {
		return Record{}, netip.Prefix{}, fmt.Errorf("解析命中 key 失败: %w", err)
	}

	record, err := decodeRecordValue(iter.Value(), startAddr)
	if err != nil {
		return Record{}, netip.Prefix{}, fmt.Errorf("解析命中 value 失败: %w", err)
	}

	prefix, err := netip.ParsePrefix(record.Network)
	if err != nil {
		return Record{}, netip.Prefix{}, fmt.Errorf("恢复命中网段失败: %w", err)
	}

	return record, prefix.Masked(), nil
}

func prefixesOverlap(a, b netip.Prefix) bool {
	a = a.Masked()
	b = b.Masked()

	if !a.IsValid() || !b.IsValid() {
		return false
	}
	if a.Addr().BitLen() != b.Addr().BitLen() {
		return false
	}

	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}
