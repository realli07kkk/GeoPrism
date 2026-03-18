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
		ReadOnly: true,
		Logger:   silentLogger{},
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
