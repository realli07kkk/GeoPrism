package ipdb

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ErrDuplicatePrefix 表示 v2 base 构建时出现重复 prefix：
// 同一 address family 内，两条记录经 Masked() 后得到完全相同的 netip.Prefix
// （即相同 primary key），无论业务字段是否相同，构建一律失败。
// 决策见 .codestable/compound/2026-06-22-decision-ipdb-base-reject-duplicate-prefix.md。
var ErrDuplicatePrefix = errors.New("base 构建出现重复 prefix")

// v2BatchCommitSize 是 v2 builder 的批量提交阈值（按"行"计，每行写 primary+cidr 两个 key）。
// 用包级 var 而非 const，便于测试注入极小值验证"同一行的 primary+cidr 不跨 batch 边界拆开"。
var v2BatchCommitSize = batchCommitSize

// buildIDNow 仅作为默认 BuildID 的时钟来源，测试可固定时间验证同名重分配。
var buildIDNow = time.Now

// buildV2FromCSV 将 CSV 构建为 v2 格式的 Pebble 离线 base 库（内部入口，未激活）。
//
// 与 v1 BuildFromCSV 的差异：
//   - 同一 batch 双写 primary（含 canonical value）+ cidr（零长度 value）双索引；
//   - 删除 v1 的"任何区间重叠都拒绝"，改为"相同 prefix 严格拒绝"（ErrDuplicatePrefix），
//     允许不同 prefix 重叠；保留"每个 family 内起始地址非递减"输入契约（乱序 reject）；
//   - metadata 写 FormatVersion=formatVersionV2 + SchemaFeatures；
//   - staging 原子构建：每次构建使用独立 staging 目录，关库后 rename 为正式目录
//     versions/{buildID}，再用独立临时文件切 CURRENT；任何错误路径只清理自己的目录；
//   - BUILD.lock 串行化 builder；VERSIONS.lock 在发布/回收阶段取独占锁，与 OpenCurrent
//     持有的共享锁协调，保证只保留当前正式版本时不会删除并发 reader 正在使用的库。
func buildV2FromCSV(rootDir string, opts BuildOptions) (Metadata, error) {
	if !filepath.IsAbs(opts.CSVPath) {
		return Metadata{}, fmt.Errorf("CSV 路径必须是绝对路径: %s", opts.CSVPath)
	}

	csvFile, err := os.Open(opts.CSVPath)
	if err != nil {
		return Metadata{}, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer csvFile.Close()

	builtAt := opts.BuiltAt
	if builtAt.IsZero() {
		builtAt = time.Now().UTC()
	}

	versionsDir := filepath.Join(rootDir, versionsDirName)
	if err := os.MkdirAll(versionsDir, 0755); err != nil {
		return Metadata{}, fmt.Errorf("创建版本目录失败: %w", err)
	}
	buildLock, err := acquireFileLock(filepath.Join(rootDir, buildLockFileName), true)
	if err != nil {
		return Metadata{}, fmt.Errorf("获取 IPDB 构建锁失败: %w", err)
	}
	defer func() {
		if err := buildLock.Close(); err != nil {
			log.Printf("释放 IPDB 构建锁失败: %v", err)
		}
	}()

	// BUILD.lock 保证当前没有其他 builder，因而可以安全回收上次崩溃遗留的 staging。
	if err := cleanupStagingDirectories(rootDir); err != nil {
		return Metadata{}, fmt.Errorf("清理遗留 staging 失败: %w", err)
	}

	buildID := opts.BuildID
	if buildID == "" {
		buildID, err = allocateDefaultBuildID(versionsDir)
		if err != nil {
			return Metadata{}, fmt.Errorf("分配默认 BuildID 失败: %w", err)
		}
	}
	finalDir := filepath.Join(versionsDir, buildID)

	// staging 必须是本次构建独占目录。不能复用或删除按 buildID 拼出的固定路径，
	// 否则同一 rootDir 下的并发构建会互相删除仍在使用的 Pebble 目录。
	stagingDir, err := os.MkdirTemp(versionsDir, stagingDirPrefix+buildID+"-")
	if err != nil {
		return Metadata{}, fmt.Errorf("创建 staging 目录失败: %w", err)
	}
	// MkdirTemp 默认 0700；正式版本沿用既有 0755 目录权限契约。
	if err := os.Chmod(stagingDir, 0755); err != nil {
		_ = os.RemoveAll(stagingDir)
		return Metadata{}, fmt.Errorf("设置 staging 目录权限失败: %w", err)
	}
	stagingDBDir := filepath.Join(stagingDir, dbDirName)
	if err := os.MkdirAll(stagingDBDir, 0755); err != nil {
		_ = os.RemoveAll(stagingDir)
		return Metadata{}, fmt.Errorf("创建 staging 目录失败: %w", err)
	}

	db, err := pebble.Open(stagingDBDir, &pebble.Options{
		Logger: silentLogger{},
	})
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return Metadata{}, fmt.Errorf("打开 Pebble 数据库失败: %w", err)
	}

	dbClosed := false
	closeDB := func() error {
		if dbClosed {
			return nil
		}
		dbClosed = true
		return db.Close()
	}

	// 失败时清理中间目录：rename 前指向 staging，rename 后指向 finalDir。
	// defer 兜底关库 + 清理，保证任何错误路径都不留下半成品目录与悬空 CURRENT。
	success := false
	cleanupDir := stagingDir
	defer func() {
		_ = closeDB()
		if !success {
			_ = os.RemoveAll(cleanupDir)
		}
	}()

	meta, err := writeV2Records(db, csvFile, opts.CSVPath, builtAt)
	if err != nil {
		return Metadata{}, err
	}

	if err := closeDB(); err != nil {
		return Metadata{}, fmt.Errorf("关闭 Pebble 数据库失败: %w", err)
	}

	// 发布与回收必须在 VERSIONS.lock 独占区间内完成。OpenCurrent 的 reader 在 Store.Close
	// 前持续持有共享锁，因此这里会先等待旧 reader 退出，再切 CURRENT 和删除旧版本。
	versionsLock, err := acquireFileLock(filepath.Join(rootDir, versionsLockFileName), true)
	if err != nil {
		return Metadata{}, fmt.Errorf("获取 IPDB 版本生命周期锁失败: %w", err)
	}
	defer func() {
		if err := versionsLock.Close(); err != nil {
			log.Printf("释放 IPDB 版本生命周期锁失败: %v", err)
		}
	}()

	// 关库后 rename 为正式目录，保证 CURRENT 永远指向已 Flush 关闭的完整目录。
	if err := os.Rename(stagingDir, finalDir); err != nil {
		return Metadata{}, fmt.Errorf("rename 正式目录失败: %w", err)
	}
	cleanupDir = finalDir

	if err := writeCurrentVersion(rootDir, buildID); err != nil {
		return Metadata{}, fmt.Errorf("切换 CURRENT 失败: %w", err)
	}

	// CURRENT 已成功切换，后续即使旧版本回收失败，也不能再由失败 defer 删除当前版本。
	success = true
	if err := cleanupOldVersions(rootDir, buildID); err != nil {
		// 当前版本已经可用，清理失败降级为可观察告警，避免把成功发布误报为失败。
		log.Printf("IPDB 已切换到版本 %s，但清理旧版本失败: %v", buildID, err)
	}
	return meta, nil
}

// allocateDefaultBuildID 在 BUILD.lock 保护下分配当前 versions/ 中不存在的默认 ID。
// wall clock 只提供可读前缀；同一 tick、时钟回拨或已有同名目录均通过数字后缀消歧。
func allocateDefaultBuildID(versionsDir string) (string, error) {
	base := buildIDNow().UTC().Format("20060102T150405.000000000")
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		_, err := os.Stat(filepath.Join(versionsDir, candidate))
		if os.IsNotExist(err) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("检查候选 BuildID %q 失败: %w", candidate, err)
		}
	}
}

// writeV2Records 解析 CSV 并把每条记录同 batch 双写进 db，最后写 metadata 并 Flush。
// 不负责目录管理 / 关库 / 切 CURRENT（由调用方处理），便于 staging 原子化时复用。
func writeV2Records(db *pebble.DB, csvFile io.Reader, csvPath string, builtAt time.Time) (Metadata, error) {
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

	// 乱序检查：每个 family（primary kind 字节）内起始地址非递减（输入契约 + 性能优化）。
	prevStart := map[byte][]byte{}
	// 重复检查：完整 prefix（= primary key，含 kind+prefixLen+addr）→ 首次出现行号。
	seen := map[string]int{}

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

		// primary key = [kind][prefixLen][addr]；cidr key = [kind][addr][prefixLen]。
		// codec 拒绝未 Masked / 越界 / IPv4-mapped IPv6，错误向上传播不静默跳过。
		primaryKey, err := encodePrimaryKeyV2(prefix)
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行编码 primary key 失败: %w", lineNo, err)
		}
		cidrKey, err := encodeCIDRKeyV2(prefix)
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行编码 cidr key 失败: %w", lineNo, err)
		}

		// 重复 prefix 检查：完整 prefix 相同（同 primary key）即重复，与业务字段无关。
		if firstLine, ok := seen[string(primaryKey)]; ok {
			return Metadata{}, fmt.Errorf("%w: CSV 第 %d 行出现重复网段 %s，首次出现于第 %d 行",
				ErrDuplicatePrefix, lineNo, prefix.Masked().String(), firstLine)
		}
		seen[string(primaryKey)] = lineNo

		// 乱序检查：仅同 family 内起始地址严格变小算乱序；相同起始地址、不同 prefixLen 合法。
		family := primaryKey[0]
		startBytes := primaryKey[2:]
		if prev := prevStart[family]; len(prev) > 0 && bytes.Compare(startBytes, prev) < 0 {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行出现乱序网段: %s", lineNo, record[0])
		}
		prevStart[family] = append(prevStart[family][:0], startBytes...)

		decoded := Record{
			Country:       strings.TrimSpace(record[1]),
			CountryCode:   strings.TrimSpace(record[2]),
			Continent:     strings.TrimSpace(record[3]),
			ContinentCode: strings.TrimSpace(record[4]),
			ASN:           strings.TrimSpace(record[5]),
			ASName:        strings.TrimSpace(record[6]),
			ASDomain:      strings.TrimSpace(record[7]),
		}

		value, err := encodeBaseRecordValueV2(decoded)
		if err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行编码 value 失败: %w", lineNo, err)
		}

		// 同一 batch 双写：primary→canonical value，cidr→零长度 value。
		// 两个 Set 之间不判 commit，保证同一行不跨 batch 边界被拆开。
		if err := batch.Set(primaryKey, value, nil); err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行写入 primary 失败: %w", lineNo, err)
		}
		if err := batch.Set(cidrKey, []byte{}, nil); err != nil {
			return Metadata{}, fmt.Errorf("CSV 第 %d 行写入 cidr 失败: %w", lineNo, err)
		}

		rowCount++
		pendingRows++
		if family == keyKindPrimaryV4 {
			ipv4Count++
		} else {
			ipv6Count++
		}

		if pendingRows >= v2BatchCommitSize {
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
		FormatVersion:  int(formatVersionV2),
		SourceCSV:      csvPath,
		BuiltAt:        builtAt,
		RowCount:       rowCount,
		IPv4Count:      ipv4Count,
		IPv6Count:      ipv6Count,
		Builder:        "geoprism",
		PebbleModule:   pebbleModulePath,
		PebbleVersion:  pebbleVersion,
		SchemaFeatures: SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx,
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

	return meta, nil
}
