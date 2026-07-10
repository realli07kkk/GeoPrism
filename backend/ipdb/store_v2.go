package ipdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"

	"github.com/cockroachdb/pebble/v2"
)

// v2 base 查询的对外错误（sentinel，可被 errors.Is 判定）。
// ErrLegacyFormat：OpenCurrent 读到 metadata 且 FormatVersion != 2（旧版库）。
// ErrIncompleteSchema：v2 库缺 SchemaFeaturePrimaryLPM|SchemaFeatureCIDRStartIdx。
// ErrCorruptIndex：cidr 索引存在但对应 primary 索引缺失（cidr value 零长度，需回查 primary）。
var (
	ErrLegacyFormat     = errors.New("离线库为旧版格式，请重建")
	ErrIncompleteSchema = errors.New("离线库缺少必要索引能力，请重建")
	ErrCorruptIndex     = errors.New("离线库索引不一致")
)

// BaseStore 表示一个 v2 不可变 base 离线库（运行期 ReadOnly 打开，永不被回写改动）。
// 承载 metadata 读取、Close、以及真查询 LookupIP/LookupCIDR（ipdb-v2-query 落地）。
// rootDir/buildID 是 base 库的版本定位上下文，供 OpenCurrent（过渡壳）确认 v2 后
// 经 openBaseV2 复用时展示版本 / 拼路径 / 输出重建提示。
type BaseStore struct {
	rootDir      string
	buildID      string
	dbDirPath    string
	db           *pebble.DB
	metadata     Metadata
	versionsLock *fileLock
}

// openBaseV2 以 ReadOnly 方式打开 rootDir 下 buildID 版本的 v2 base 库并读出 metadata
// （内部入口）。dbDir 由 rootDir/versions/{buildID}/db 拼出（与 v1 OpenCurrent 一致）。
//
// 调用方：OpenCurrent（公开过渡壳）读 CURRENT 得 buildID、自己 probe metadata 判版本/capability
// 后，确认为完整 v2 库再调本函数（此时 sanity 必过），零改动复用 BaseStore 构造。
//
// 校验范围：ReadOnly 打开 + 读 metadata + sanity 校验 FormatVersion==formatVersionV2
// （不符返回 fmt.Errorf 包装错误，**非 sentinel**）。**不**做 SchemaFeatures capability
// 拒绝、**不**做 v1 识别——ErrLegacyFormat / ErrIncompleteSchema 归 OpenCurrent（query 收口）。
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
	if s == nil {
		return nil
	}

	var closeErrs []error
	db := s.db
	s.db = nil
	if db != nil {
		closeErrs = append(closeErrs, db.Close())
	}
	// 必须先关闭 Pebble，再释放共享生命周期锁；否则 builder 可能在 DB 仍使用
	// 旧版本文件时获得独占锁并删除该版本。
	versionsLock := s.versionsLock
	s.versionsLock = nil
	if versionsLock != nil {
		closeErrs = append(closeErrs, versionsLock.Close())
	}
	return errors.Join(closeErrs...)
}

// LookupIP 对单 IP 做真正的最长前缀匹配（LPM ladder）：
// 从 maxBits(addr) 递减到 0，对每个前缀长度 L 用 primary key 做 point Get，
// 首个命中即为最长前缀。ladder 算法本身对重叠记录正确，不依赖"网段不重叠"前提。
//
// 返回：(rec, true, nil) 命中最具体网段；(Record{}, false, nil) 全程未命中。
// 命中结果的 Record.Network 由本方法据命中的 primary key 还原 prefix 后回填
// （decodeBaseRecordValueV2 返回的 Network 为空）。
func (s *BaseStore) LookupIP(addr netip.Addr) (Record, bool, error) {
	if s == nil || s.db == nil {
		return Record{}, false, ErrNoCurrentDatabase
	}

	maxBits := 128
	if addr.Is4() {
		maxBits = 32
	}

	for l := maxBits; l >= 0; l-- {
		// ladder key = encodePrimaryKeyV2(PrefixFrom(addr, l).Masked())。
		// Masked() 是必需的：encodePrimaryKeyV2 要求入参已 Masked（addr == masked.Addr），
		// 否则拒绝（如 10.1.2.3/31 未 Masked，需先 Masked 成 10.1.2.2/31）。
		prefix := netip.PrefixFrom(addr, l).Masked()
		key, err := encodePrimaryKeyV2(prefix)
		if err != nil {
			// ladder 中的 prefix 必然合法（addr 合法 + L 在 [0,maxBits]），出错视为内部错误。
			return Record{}, false, fmt.Errorf("编码 primary key 失败 (L=%d): %w", l, err)
		}

		value, closer, err := s.db.Get(key)
		if err != nil {
			// pebble.ErrNotFound 表示该前缀长度无记录，继续 ladder；其余错误上抛。
			if !errors.Is(err, pebble.ErrNotFound) {
				return Record{}, false, fmt.Errorf("查询 primary key 失败: %w", err)
			}
			continue
		}

		record, decodeErr := decodeBaseRecordValueV2(value)
		closeErr := closer.Close()
		if decodeErr != nil {
			return Record{}, false, fmt.Errorf("解析 base value 失败: %w", decodeErr)
		}
		if closeErr != nil {
			return Record{}, false, fmt.Errorf("关闭 base value 读取器失败: %w", closeErr)
		}

		// 据 key 还原 prefix 回填 Network（decode 返回的 Network 为空）。
		// prefix 已 Masked，与构建时写入的 prefix 一致。
		record.Network = prefix.String()
		return record, true, nil
	}

	return Record{}, false, nil
}

// LookupCIDR 查询与 query 相交的所有 v2 base 记录，返回确定性排序结果。
//
// 三段合并（ancestors + self + descendants）：
//   - ancestors：L=0..query.Bits()-1，对 query.Addr() 逐 L mask 后用 primary key 精确 Get，
//     覆盖所有"包含 query.Addr() 的超集"（含起始<query 起始的左相交超集）。
//   - self + descendants：扫 cidr 索引起始地址 [query.Addr(), queryLastAddr]，保留
//     prefixLen >= query.Bits() 的记录（self 与被 query 包含的后代）。
//
// cidr value 零长度，每条命中回查 primary 取 value；cidr 有 primary 无 → ErrCorruptIndex。
// 结果按 encodeCIDRKeyV2 字节序（= startAddr 升序 + prefixLen 升序）确定性排序。
func (s *BaseStore) LookupCIDR(query netip.Prefix) ([]Record, error) {
	if s == nil || s.db == nil {
		return nil, ErrNoCurrentDatabase
	}
	query = query.Masked()

	// ancestors + descendants 收集 prefix（含 self，self 由 descendants 区间扫描覆盖）。
	seen := make(map[string]struct{})
	var prefixes []netip.Prefix
	addPrefix := func(p netip.Prefix) {
		key := p.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		prefixes = append(prefixes, p)
	}

	// 1. ancestors 段。
	ancestorPrefixes, err := s.lookupCIDRAncestors(query)
	if err != nil {
		return nil, err
	}
	for _, p := range ancestorPrefixes {
		addPrefix(p)
	}

	// 2. self + descendants 段（扫 cidr 索引）。
	descendantPrefixes, err := s.lookupCIDRDescendants(query)
	if err != nil {
		return nil, err
	}
	for _, p := range descendantPrefixes {
		addPrefix(p)
	}

	if len(prefixes) == 0 {
		return nil, nil
	}

	// 3. 确定性排序（encodeCIDRKeyV2 字节序 = startAddr 升序 + prefixLen 升序）。
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int {
		ka, _ := encodeCIDRKeyV2(a)
		kb, _ := encodeCIDRKeyV2(b)
		return bytes.Compare(ka, kb)
	})

	// 4. 逐条回查 primary 取 value + 回填 Network。
	records := make([]Record, 0, len(prefixes))
	for _, p := range prefixes {
		rec, err := s.fetchRecordByPrefix(p)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// lookupCIDRAncestors 返回覆盖 query.Addr() 的所有祖先网段（L=0..query.Bits()-1）。
// 对 query.Addr() 逐 L mask 后用 primary key 精确 Get，命中即收集。
// 这天然覆盖所有"起始≤query.Addr 且与 query 相交的超集"，含起始<query 起始的情况。
func (s *BaseStore) lookupCIDRAncestors(query netip.Prefix) ([]netip.Prefix, error) {
	var ancestors []netip.Prefix
	for l := 0; l < query.Bits(); l++ {
		prefix := netip.PrefixFrom(query.Addr(), l).Masked()
		key, err := encodePrimaryKeyV2(prefix)
		if err != nil {
			return nil, fmt.Errorf("编码 ancestors primary key 失败 (L=%d): %w", l, err)
		}
		exists, err := s.primaryKeyExists(key)
		if err != nil {
			return nil, err
		}
		if exists {
			ancestors = append(ancestors, prefix)
		}
	}
	return ancestors, nil
}

// lookupCIDRDescendants 扫 cidr 索引起始地址 [query.Addr(), queryLastAddr]，
// 收集 prefixLen >= query.Bits() 的记录（self + 被 query 包含的后代）。
func (s *BaseStore) lookupCIDRDescendants(query netip.Prefix) ([]netip.Prefix, error) {
	lower, upper, err := cidrScanBounds(query)
	if err != nil {
		return nil, err
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("创建 cidr 迭代器失败: %w", err)
	}
	defer iter.Close()

	var descendants []netip.Prefix
	for valid := iter.First(); valid; valid = iter.Next() {
		prefix, err := decodeCIDRKeyV2(iter.Key())
		if err != nil {
			return nil, fmt.Errorf("解析 cidr key 失败: %w", err)
		}
		// 仅保留 self（prefixLen==query.Bits）与后代（prefixLen>query.Bits）。
		if prefix.Bits() < query.Bits() {
			continue
		}
		descendants = append(descendants, prefix)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("迭代 cidr 索引失败: %w", err)
	}
	return descendants, nil
}

// fetchRecordByPrefix 用 primary key 取 value 并回填 Network。
// cidr value 零长度，所有记录的 canonical value 都在 primary；cidr 命中但 primary 缺失
// → ErrCorruptIndex（不静默跳过）。
func (s *BaseStore) fetchRecordByPrefix(prefix netip.Prefix) (Record, error) {
	key, err := encodePrimaryKeyV2(prefix)
	if err != nil {
		return Record{}, fmt.Errorf("编码 primary key 失败: %w", err)
	}
	value, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return Record{}, fmt.Errorf("%w: cidr 索引存在 %s 但 primary 索引缺失", ErrCorruptIndex, prefix.String())
		}
		return Record{}, fmt.Errorf("回查 primary 失败: %w", err)
	}
	record, decodeErr := decodeBaseRecordValueV2(value)
	closeErr := closer.Close()
	if decodeErr != nil {
		return Record{}, fmt.Errorf("解析 base value 失败: %w", decodeErr)
	}
	if closeErr != nil {
		return Record{}, fmt.Errorf("关闭 base value 读取器失败: %w", closeErr)
	}
	record.Network = prefix.String()
	return record, nil
}

// primaryKeyExists 判断 primary key 是否存在（仅用于 ancestors 段，不需取 value）。
func (s *BaseStore) primaryKeyExists(key []byte) (bool, error) {
	_, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("查询 ancestors primary key 失败: %w", err)
	}
	if err := closer.Close(); err != nil {
		return false, fmt.Errorf("关闭 ancestors primary 读取器失败: %w", err)
	}
	return true, nil
}

// cidrScanBounds 构造 cidr 索引区间扫描的 [LowerBound, UpperBound]。
// cidr key = [kind][addr][prefixLen]，同 family 内按 addr 升序、同 addr 按 prefixLen 升序。
// 下界 = [kind][query.Addr()]（query.Addr 已 Masked，覆盖该 addr 的所有 prefixLen）。
// 上界 = [kind][queryLastAddr] + 0xFF（覆盖 queryLastAddr 的所有 prefixLen，排除更高 addr）。
func cidrScanBounds(query netip.Prefix) (lower, upper []byte, err error) {
	var kind byte
	switch {
	case query.Addr().Is4():
		kind = keyKindCIDRV4
	case query.Addr().Is6():
		kind = keyKindCIDRV6
	default:
		return nil, nil, fmt.Errorf("不支持的 IP family: %s", query.Addr().String())
	}

	startBytes, err := addrBytes(query.Addr())
	if err != nil {
		return nil, nil, err
	}
	// 下界 = [kind][startAddr]（不含 prefixLen，作为该 addr 的扫描起点）。
	lower = make([]byte, 0, 1+len(startBytes))
	lower = append(lower, kind)
	lower = append(lower, startBytes...)

	lastAddr, err := prefixLastAddr(query)
	if err != nil {
		return nil, nil, fmt.Errorf("计算 query 区间末尾失败: %w", err)
	}
	endBytes, err := addrBytes(lastAddr)
	if err != nil {
		return nil, nil, err
	}
	// 上界 = [kind][endAddr][0xFF]：覆盖 endAddr 的所有 prefixLen，且排除更高 addr。
	upper = make([]byte, 0, 1+len(endBytes)+1)
	upper = append(upper, kind)
	upper = append(upper, endBytes...)
	upper = append(upper, 0xFF)

	return lower, upper, nil
}
