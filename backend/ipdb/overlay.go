package ipdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

const (
	overlayFormatVersion = 1
	overlayDirName       = "overlay"
	overlayLockFileName  = "OVERLAY.lock"
)

var (
	// ErrOverlayLocked 表示 overlay 已由其它进程持有。
	ErrOverlayLocked = errors.New("overlay 已被其它进程占用")
	// ErrOverlayClosed 表示 Get/Put 使用了 nil 或已关闭的 overlay 句柄。
	ErrOverlayClosed = errors.New("overlay 已关闭")

	errOverlayNeedsQuarantine = errors.New("overlay 需要隔离重建")
)

// OverlayMetadata 是 overlay 独立的库级元数据。
type OverlayMetadata struct {
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
}

type overlayDB interface {
	Get(key []byte) ([]byte, io.Closer, error)
	Set(key, value []byte, opts *pebble.WriteOptions) error
	Delete(key []byte, opts *pebble.WriteOptions) error
	Close() error
}

type overlayDependencies struct {
	openDB      func(string, *pebble.Options) (overlayDB, error)
	rename      func(string, string) error
	now         func() time.Time
	acquireLock func(string) (io.Closer, bool, error)
}

func defaultOverlayDependencies() overlayDependencies {
	return overlayDependencies{
		openDB: func(path string, opts *pebble.Options) (overlayDB, error) {
			return pebble.Open(path, opts)
		},
		rename: os.Rename,
		now:    time.Now,
		acquireLock: func(path string) (io.Closer, bool, error) {
			return tryAcquireFileLock(path, true)
		},
	}
}

// OverlayStore 表示独立于 base 版本目录的单 IP 缓存。
type OverlayStore struct {
	mu         sync.Mutex
	overlayDir string
	dbDir      string
	db         overlayDB
	metadata   OverlayMetadata
	lock       io.Closer
	// beforeLock 仅供包内测试确定性观测锁竞争边界；生产句柄始终为 nil。
	beforeLock func()
}

// OpenOverlay 打开或首次创建 rootDir/overlay/db，并在整个句柄生命周期内
// 非阻塞持有 overlay/OVERLAY.lock 独占锁。
func OpenOverlay(rootDir string) (*OverlayStore, error) {
	return openOverlay(rootDir, defaultOverlayDependencies())
}

func openOverlay(rootDir string, deps overlayDependencies) (*OverlayStore, error) {
	overlayDir := filepath.Join(rootDir, overlayDirName)
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		return nil, fmt.Errorf("创建 overlay 目录失败: %w", err)
	}

	lock, busy, err := deps.acquireLock(filepath.Join(overlayDir, overlayLockFileName))
	if err != nil {
		return nil, fmt.Errorf("获取 overlay 生命周期锁失败: %w", err)
	}
	if busy {
		if lock != nil {
			return nil, errors.Join(ErrOverlayLocked,
				wrapOverlayCloseError("释放意外取得的 overlay 生命周期锁失败", lock.Close()))
		}
		return nil, ErrOverlayLocked
	}

	dbDir := filepath.Join(overlayDir, dbDirName)
	_, statErr := os.Stat(dbDir)
	switch {
	case statErr == nil:
		return openExistingOverlay(overlayDir, dbDir, lock, deps)
	case os.IsNotExist(statErr):
		return createFreshOverlay(overlayDir, dbDir, lock, deps)
	default:
		return nil, closeOverlayLockError(lock,
			fmt.Errorf("检查 overlay 数据库失败: %w", statErr))
	}
}

func openExistingOverlay(
	overlayDir, dbDir string,
	lock io.Closer,
	deps overlayDependencies,
) (*OverlayStore, error) {
	db, err := deps.openDB(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		if pebble.IsCorruptionError(err) {
			return quarantineAndCreateOverlay(overlayDir, dbDir, lock, deps,
				fmt.Errorf("打开 overlay 数据库时发现损坏: %w", err))
		}
		return nil, closeOverlayLockError(lock,
			fmt.Errorf("打开 overlay 数据库失败: %w", err))
	}

	metadata, err := readOverlayMetadata(db)
	if err == nil {
		return newOverlayStore(overlayDir, dbDir, db, metadata, lock), nil
	}
	if !errors.Is(err, errOverlayNeedsQuarantine) {
		return nil, closeOverlayAfterOpenError(db, lock, err)
	}

	if closeErr := db.Close(); closeErr != nil {
		return nil, closeOverlayLockError(lock, errors.Join(
			err,
			fmt.Errorf("关闭待隔离 overlay 数据库失败: %w", closeErr),
		))
	}
	return quarantineAndCreateOverlay(overlayDir, dbDir, lock, deps, err)
}

func createFreshOverlay(
	overlayDir, dbDir string,
	lock io.Closer,
	deps overlayDependencies,
) (*OverlayStore, error) {
	db, err := deps.openDB(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		return nil, closeOverlayLockError(lock,
			fmt.Errorf("创建 overlay 数据库失败: %w", err))
	}

	metadata, err := writeOverlayMetadata(db, deps.now().UTC())
	if err != nil {
		return nil, closeOverlayAfterOpenError(db, lock, err)
	}
	return newOverlayStore(overlayDir, dbDir, db, metadata, lock), nil
}

func newOverlayStore(
	overlayDir, dbDir string,
	db overlayDB,
	metadata OverlayMetadata,
	lock io.Closer,
) *OverlayStore {
	return &OverlayStore{
		overlayDir: overlayDir,
		dbDir:      dbDir,
		db:         db,
		metadata:   metadata,
		lock:       lock,
	}
}

func writeOverlayMetadata(db overlayDB, createdAt time.Time) (OverlayMetadata, error) {
	if createdAt.IsZero() {
		return OverlayMetadata{}, errors.New("overlay metadata CreatedAt 不得为零值")
	}
	metadata := OverlayMetadata{
		FormatVersion: overlayFormatVersion,
		CreatedAt:     createdAt,
	}
	value, err := json.Marshal(metadata)
	if err != nil {
		return OverlayMetadata{}, fmt.Errorf("编码 overlay 元数据失败: %w", err)
	}
	if err := db.Set(metadataKey, value, pebble.Sync); err != nil {
		return OverlayMetadata{}, fmt.Errorf("写入 overlay 元数据失败: %w", err)
	}
	return metadata, nil
}

func readOverlayMetadata(db overlayDB) (OverlayMetadata, error) {
	value, closer, err := db.Get(metadataKey)
	if err != nil {
		cause := fmt.Errorf("读取 overlay 元数据失败: %w", err)
		if errors.Is(err, pebble.ErrNotFound) || pebble.IsCorruptionError(err) {
			return OverlayMetadata{}, errors.Join(errOverlayNeedsQuarantine, cause)
		}
		return OverlayMetadata{}, cause
	}
	valueCopy := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return OverlayMetadata{}, fmt.Errorf("关闭 overlay 元数据读取器失败: %w", err)
	}

	var metadata OverlayMetadata
	if err := json.Unmarshal(valueCopy, &metadata); err != nil {
		return OverlayMetadata{}, errors.Join(
			errOverlayNeedsQuarantine,
			fmt.Errorf("解析 overlay 元数据失败: %w", err),
		)
	}
	if metadata.FormatVersion != overlayFormatVersion {
		return OverlayMetadata{}, errors.Join(
			errOverlayNeedsQuarantine,
			fmt.Errorf("overlay 格式版本不兼容: FormatVersion=%d, want %d",
				metadata.FormatVersion, overlayFormatVersion),
		)
	}
	if metadata.CreatedAt.IsZero() {
		return OverlayMetadata{}, errors.Join(
			errOverlayNeedsQuarantine,
			errors.New("overlay 元数据缺少 CreatedAt"),
		)
	}
	return metadata, nil
}

func quarantineAndCreateOverlay(
	overlayDir, dbDir string,
	lock io.Closer,
	deps overlayDependencies,
	cause error,
) (*OverlayStore, error) {
	quarantineDir, err := nextOverlayQuarantineDir(overlayDir, deps.now().UTC())
	if err != nil {
		return nil, closeOverlayLockError(lock, errors.Join(cause, err))
	}
	if err := deps.rename(dbDir, quarantineDir); err != nil {
		return nil, closeOverlayLockError(lock, errors.Join(
			cause,
			fmt.Errorf("隔离 overlay 数据库失败: %w", err),
		))
	}
	store, err := createFreshOverlay(overlayDir, dbDir, lock, deps)
	if err != nil {
		return nil, errors.Join(
			cause,
			fmt.Errorf("overlay 已隔离到 %s，但创建新库失败: %w", quarantineDir, err),
		)
	}
	return store, nil
}

func nextOverlayQuarantineDir(overlayDir string, now time.Time) (string, error) {
	baseName := "quarantine-" + now.Format("20060102T150405.000000000Z")
	for suffix := 0; ; suffix++ {
		name := baseName
		if suffix > 0 {
			name = fmt.Sprintf("%s-%d", baseName, suffix)
		}
		candidate := filepath.Join(overlayDir, name)
		// Lstat 把 dangling symlink 也视为已占用，避免误选一个无法 rename 的名称。
		_, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("检查 overlay quarantine 目录失败: %w", err)
		}
	}
}

func closeOverlayAfterOpenError(db overlayDB, lock io.Closer, cause error) error {
	dbErr := wrapOverlayCloseError("关闭 overlay 数据库失败", db.Close())
	lockErr := wrapOverlayCloseError("释放 overlay 生命周期锁失败", lock.Close())
	return errors.Join(cause, dbErr, lockErr)
}

func closeOverlayLockError(lock io.Closer, cause error) error {
	return errors.Join(cause,
		wrapOverlayCloseError("释放 overlay 生命周期锁失败", lock.Close()))
}

func wrapOverlayCloseError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func (o *OverlayStore) lockOperation() {
	if o.beforeLock != nil {
		o.beforeLock()
	}
	o.mu.Lock()
}

// Get 精确查询单个 IP。过期或损坏 value 视为 miss，并机会性删除对应 key。
func (o *OverlayStore) Get(
	addr netip.Addr,
	now time.Time,
) (Record, OverlayMeta, bool, error) {
	if o == nil {
		return Record{}, OverlayMeta{}, false, ErrOverlayClosed
	}

	o.lockOperation()
	defer o.mu.Unlock()

	if o.db == nil {
		return Record{}, OverlayMeta{}, false, ErrOverlayClosed
	}

	key, err := encodeOverlayStoreKey(addr)
	if err != nil {
		return Record{}, OverlayMeta{}, false, fmt.Errorf("编码 overlay key 失败: %w", err)
	}
	value, closer, err := o.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return Record{}, OverlayMeta{}, false, nil
		}
		return Record{}, OverlayMeta{}, false, fmt.Errorf("查询 overlay 失败: %w", err)
	}
	valueCopy := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return Record{}, OverlayMeta{}, false, fmt.Errorf("关闭 overlay value 读取器失败: %w", err)
	}

	record, metadata, err := decodeOverlayRecordValueV1(valueCopy)
	if err != nil {
		decodeErr := fmt.Errorf("解析 overlay value 失败: %w", err)
		if deleteErr := o.deleteOverlayEntry(key); deleteErr != nil {
			return Record{}, OverlayMeta{}, false, errors.Join(decodeErr, deleteErr)
		}
		return Record{}, OverlayMeta{}, false, nil
	}

	expired := !metadata.ExpiresAt.IsZero() && !now.Before(metadata.ExpiresAt)
	if expired {
		if err := o.deleteOverlayEntry(key); err != nil {
			return Record{}, OverlayMeta{}, false, err
		}
		return Record{}, OverlayMeta{}, false, nil
	}

	record.Network = overlayHostPrefix(addr).String()
	return record, metadata, true, nil
}

// Put 同步覆盖单个 IP 的完整 Record 与 OverlayMeta。
func (o *OverlayStore) Put(addr netip.Addr, record Record, metadata OverlayMeta) error {
	if o == nil {
		return ErrOverlayClosed
	}

	o.lockOperation()
	defer o.mu.Unlock()

	if o.db == nil {
		return ErrOverlayClosed
	}

	key, err := encodeOverlayStoreKey(addr)
	if err != nil {
		return fmt.Errorf("编码 overlay key 失败: %w", err)
	}
	if err := validateOverlayRecordNetwork(addr, record.Network); err != nil {
		return err
	}
	if !metadata.ExpiresAt.IsZero() && metadata.ExpiresAt.Unix() == 0 {
		return fmt.Errorf("ExpiresAt 落入保留的 Unix 0：该值表示永不过期")
	}

	value, err := encodeOverlayRecordValueV1(record, metadata)
	if err != nil {
		return fmt.Errorf("编码 overlay value 失败: %w", err)
	}
	if err := o.db.Set(key, value, pebble.Sync); err != nil {
		return fmt.Errorf("写入 overlay 失败: %w", err)
	}
	return nil
}

func encodeOverlayStoreKey(addr netip.Addr) ([]byte, error) {
	if addr.Zone() != "" {
		return nil, fmt.Errorf("不支持带 zone 的 IPv6: %s", addr.String())
	}
	return encodeOverlayKeyV2(addr)
}

func validateOverlayRecordNetwork(addr netip.Addr, network string) error {
	if network == "" {
		return nil
	}
	prefix, err := netip.ParsePrefix(network)
	if err != nil {
		return fmt.Errorf("Record.Network 非法: %w", err)
	}
	expected := overlayHostPrefix(addr)
	if prefix != expected {
		return fmt.Errorf("Record.Network 与 addr 不一致: got %s, want %s", prefix.String(), expected.String())
	}
	return nil
}

func overlayHostPrefix(addr netip.Addr) netip.Prefix {
	bits := 128
	if addr.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(addr, bits)
}

func (o *OverlayStore) deleteOverlayEntry(key []byte) error {
	if err := o.db.Delete(key, pebble.Sync); err != nil {
		return fmt.Errorf("删除 overlay 缓存项失败: %w", err)
	}
	return nil
}

// Close 先关闭 Pebble，再释放 overlay 生命周期锁。nil 与重复 Close 均安全。
func (o *OverlayStore) Close() error {
	if o == nil {
		return nil
	}

	o.lockOperation()
	defer o.mu.Unlock()

	db := o.db
	o.db = nil
	lock := o.lock
	o.lock = nil

	var closeErrs []error
	if db != nil {
		closeErrs = append(closeErrs,
			wrapOverlayCloseError("关闭 overlay 数据库失败", db.Close()))
	}
	if lock != nil {
		closeErrs = append(closeErrs,
			wrapOverlayCloseError("释放 overlay 生命周期锁失败", lock.Close()))
	}
	return errors.Join(closeErrs...)
}
