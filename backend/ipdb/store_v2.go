package ipdb

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/cockroachdb/pebble/v2"
)

// BaseStore 表示一个 v2 不可变 base 离线库（运行期 ReadOnly 打开，永不被回写改动）。
// 本阶段只承载 metadata 读取与 Close；真查询 LookupIP/LookupCIDR 归 ipdb-v2-query。
// rootDir/buildID 是 base 库的版本定位上下文，供 ipdb-v2-query 的 OpenCurrentBase
// 复用时展示版本 / 拼路径 / 输出重建提示，base-build 阶段先随 openBaseV2 入参落地。
type BaseStore struct {
	rootDir   string
	buildID   string
	dbDirPath string
	db        *pebble.DB
	metadata  Metadata
}

// openBaseV2 以 ReadOnly 方式打开 rootDir 下 buildID 版本的 v2 base 库并读出 metadata
// （内部入口）。dbDir 由 rootDir/versions/{buildID}/db 拼出（与 v1 OpenCurrent 一致）。
//
// 本 feature 阶段无生产调用方，仅供构建后校验 / 测试；ipdb-v2-query 的
// OpenCurrentBase(rootDir) 读 CURRENT 得 buildID 后调本函数，零改动复用。
//
// 校验范围：ReadOnly 打开 + 读 metadata + sanity 校验 FormatVersion==formatVersionV2
// （不符返回 fmt.Errorf 包装错误，**非 sentinel**）。**不**做 SchemaFeatures capability
// 拒绝、**不**做 v1 识别——ErrLegacyFormat / ErrIncompleteSchema 归 ipdb-v2-query 的
// OpenCurrentBase。
func openBaseV2(rootDir, buildID string) (*BaseStore, error) {
	dbDir := filepath.Join(rootDir, versionsDirName, buildID, dbDirName)

	db, err := pebble.Open(dbDir, &pebble.Options{
		ReadOnly: true,
		Logger:   silentLogger{},
	})
	if err != nil {
		return nil, fmt.Errorf("打开 base 库失败: %w", err)
	}

	metaValue, closer, err := db.Get(metadataKey)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("读取 base 库元数据失败: %w", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(metaValue, &metadata); err != nil {
		closer.Close()
		db.Close()
		return nil, fmt.Errorf("解析 base 库元数据失败: %w", err)
	}
	if err := closer.Close(); err != nil {
		db.Close()
		return nil, fmt.Errorf("关闭元数据读取器失败: %w", err)
	}

	if metadata.FormatVersion != int(formatVersionV2) {
		db.Close()
		return nil, fmt.Errorf("base 库格式版本不符: FormatVersion=%d, want %d", metadata.FormatVersion, formatVersionV2)
	}

	return &BaseStore{
		rootDir:   rootDir,
		buildID:   buildID,
		dbDirPath: dbDir,
		db:        db,
		metadata:  metadata,
	}, nil
}

// Metadata 返回 base 库元信息。
func (s *BaseStore) Metadata() Metadata {
	if s == nil {
		return Metadata{}
	}
	return s.metadata
}

// Close 关闭 base 库（幂等）。
func (s *BaseStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	db := s.db
	s.db = nil
	return db.Close()
}
