package ipdb

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// countV2Keys 直接打开 dbDir 扫描 Pebble，按 kind 字节统计 primary / cidr key 数；
// 顺带校验每个 cidr key 的 value 为零长度。
func countV2Keys(t *testing.T, dbDir string) (primary, cidr int) {
	t.Helper()

	db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open(%q) error = %v", dbDir, err)
	}
	defer db.Close()

	iter, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("NewIter error = %v", err)
	}
	defer iter.Close()

	for valid := iter.First(); valid; valid = iter.Next() {
		key := iter.Key()
		if len(key) == 0 {
			continue
		}
		switch key[0] {
		case keyKindPrimaryV4, keyKindPrimaryV6:
			primary++
		case keyKindCIDRV4, keyKindCIDRV6:
			cidr++
			if len(iter.Value()) != 0 {
				t.Errorf("cidr key value 应为零长度，实际 %d 字节", len(iter.Value()))
			}
		}
	}
	return primary, cidr
}

func dbDirFor(rootDir, buildID string) string {
	return filepath.Join(rootDir, versionsDirName, buildID, dbDirName)
}

func TestBuildV2FromCSVDualIndex(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
		`1.0.1.0/24,China,CN,Asia,AS,,,`,
		`2001:db8::/48,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
		`2001:db8:1::/48,Testland,TT,Test,TS,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "v2-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	if meta.FormatVersion != int(formatVersionV2) {
		t.Fatalf("FormatVersion = %d, want %d", meta.FormatVersion, formatVersionV2)
	}
	if want := SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx; meta.SchemaFeatures != want {
		t.Fatalf("SchemaFeatures = %d, want %d", meta.SchemaFeatures, want)
	}
	if meta.RowCount != 4 || meta.IPv4Count != 2 || meta.IPv6Count != 2 {
		t.Fatalf("counts mismatch: %#v", meta)
	}

	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "v2-build"))
	if int64(primary) != meta.RowCount || int64(cidr) != meta.RowCount {
		t.Fatalf("primaryCount=%d cidrCount=%d, want both == RowCount=%d", primary, cidr, meta.RowCount)
	}
}

func TestBuildV2RejectsDuplicatePrefix(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,A,A,A,A,,,`,
		`10.0.0.0/16,B,B,B,B,,,`,
		`10.0.0.0/8,C,C,C,C,,,`,
	}, "\n"))

	_, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "dup-build"})
	if !errors.Is(err, ErrDuplicatePrefix) {
		t.Fatalf("error = %v, want ErrDuplicatePrefix", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.0/8") ||
		!strings.Contains(err.Error(), "首次出现于第 2 行") ||
		!strings.Contains(err.Error(), "第 4 行") {
		t.Fatalf("error message missing canonical prefix / line numbers: %v", err)
	}
}

func TestBuildV2AllowsDistinctOverlap(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,A,A,A,A,,,`,
		`10.0.0.0/16,B,B,B,B,,,`,
		`10.1.0.0/16,C,C,C,C,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "overlap-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v, want success (overlap allowed)", err)
	}
	if meta.RowCount != 3 {
		t.Fatalf("RowCount = %d, want 3", meta.RowCount)
	}

	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "overlap-build"))
	if primary != 3 || cidr != 3 {
		t.Fatalf("primaryCount=%d cidrCount=%d, want 3/3", primary, cidr)
	}
}

func TestBuildV2DualWriteNotSplitByBatch(t *testing.T) {
	// 注入极小 commitSize，使 commit 切点落在行边界上，验证同一行的 primary+cidr
	// 不被拆到不同 batch 而出现计数不等 / 半写。
	orig := v2BatchCommitSize
	v2BatchCommitSize = 2
	defer func() { v2BatchCommitSize = orig }()

	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`1.0.1.0/24,B,B,B,B,,,`,
		`1.0.2.0/24,C,C,C,C,,,`,
		`2001:db8::/48,D,D,D,D,,,`,
		`2001:db8:1::/48,E,E,E,E,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "batch-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "batch-build"))
	if int64(primary) != meta.RowCount || int64(cidr) != meta.RowCount {
		t.Fatalf("primaryCount=%d cidrCount=%d, want both == RowCount=%d", primary, cidr, meta.RowCount)
	}
}

func TestBuildV2StagingSuccess(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`1.0.1.0/24,B,B,B,B,,,`,
	}, "\n"))

	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "ok-build"}); err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	versionsDir := filepath.Join(rootDir, versionsDirName)
	if _, err := os.Stat(filepath.Join(versionsDir, "ok-build", dbDirName)); err != nil {
		t.Fatalf("正式目录缺失: %v", err)
	}
	if _, err := os.Stat(filepath.Join(versionsDir, stagingDirPrefix+"ok-build")); !os.IsNotExist(err) {
		t.Fatalf("staging 目录应已 rename 消失，statErr = %v", err)
	}
	cur, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil || string(cur) != "ok-build" {
		t.Fatalf("CURRENT = %q (err=%v), want ok-build", string(cur), err)
	}
}

func TestBuildV2StagingCleanupOnFailure(t *testing.T) {
	rootDir := t.TempDir()
	// 第二行 network 带 host bits（非规范网段），writeV2Records 在构建中途失败。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`10.0.0.1/24,B,B,B,B,,,`,
	}, "\n"))

	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "fail-build"}); err == nil {
		t.Fatal("buildV2FromCSV() error = nil, want failure on non-canonical network")
	}

	versionsDir := filepath.Join(rootDir, versionsDirName)
	if _, err := os.Stat(filepath.Join(versionsDir, "fail-build")); !os.IsNotExist(err) {
		t.Fatalf("失败构建不应留下正式目录，statErr = %v", err)
	}
	if _, err := os.Stat(filepath.Join(versionsDir, stagingDirPrefix+"fail-build")); !os.IsNotExist(err) {
		t.Fatalf("失败构建不应残留 staging 目录，statErr = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootDir, currentFileName)); !os.IsNotExist(err) {
		t.Fatalf("失败构建不应写 CURRENT，statErr = %v", err)
	}
}

// collectPrimaryPrefixes 扫库收集所有 primary key 解出的 prefix 字符串。
func collectPrimaryPrefixes(t *testing.T, dbDir string) []string {
	t.Helper()
	db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open(%q) error = %v", dbDir, err)
	}
	defer db.Close()

	iter, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("NewIter error = %v", err)
	}
	defer iter.Close()

	var out []string
	for valid := iter.First(); valid; valid = iter.Next() {
		key := iter.Key()
		if len(key) == 0 {
			continue
		}
		if key[0] == keyKindPrimaryV4 || key[0] == keyKindPrimaryV6 {
			p, err := decodePrimaryKeyV2(key)
			if err != nil {
				t.Fatalf("decodePrimaryKeyV2 error = %v", err)
			}
			out = append(out, p.String())
		}
	}
	return out
}

func TestBuildV2RejectsOutOfOrder(t *testing.T) {
	rootDir := t.TempDir()
	// 同 family 内起始地址递减 → 乱序。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`2.0.0.0/24,A,A,A,A,,,`,
		`1.0.0.0/24,B,B,B,B,,,`,
	}, "\n"))

	_, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "order-build"})
	if err == nil {
		t.Fatal("buildV2FromCSV() error = nil, want out-of-order error")
	}
	if !strings.Contains(err.Error(), "乱序网段") {
		t.Fatalf("error = %v, want 乱序网段", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, currentFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("乱序失败不应写 CURRENT，statErr = %v", statErr)
	}
}

func TestBuildV2SingleIPRows(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.7.168.172,India,IN,Asia,AS,AS9583,Sify Limited,sifycorp.com`,
		`2001:db8::1,Testland,TT,Test,TS,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "singleip-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}
	if meta.RowCount != 2 || meta.IPv4Count != 1 || meta.IPv6Count != 1 {
		t.Fatalf("counts mismatch: %#v", meta)
	}

	prefixes := collectPrimaryPrefixes(t, dbDirFor(rootDir, "singleip-build"))
	if !slices.Contains(prefixes, "1.7.168.172/32") || !slices.Contains(prefixes, "2001:db8::1/128") {
		t.Fatalf("primary prefixes = %v, want 含 /32 与 /128", prefixes)
	}
}

func TestBuildV2BoundaryPrefixes(t *testing.T) {
	rootDir := t.TempDir()
	// 各 family 内起始地址非递减；覆盖 /0、/32、/128。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`0.0.0.0/0,A,A,A,A,,,`,
		`255.255.255.255/32,B,B,B,B,,,`,
		`::/0,C,C,C,C,,,`,
		`2001:db8::1/128,D,D,D,D,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "boundary-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}
	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "boundary-build"))
	if int64(primary) != meta.RowCount || int64(cidr) != meta.RowCount {
		t.Fatalf("primaryCount=%d cidrCount=%d, want both == %d", primary, cidr, meta.RowCount)
	}
	prefixes := collectPrimaryPrefixes(t, dbDirFor(rootDir, "boundary-build"))
	for _, want := range []string{"0.0.0.0/0", "255.255.255.255/32", "::/0", "2001:db8::1/128"} {
		if !slices.Contains(prefixes, want) {
			t.Fatalf("primary prefixes = %v, missing %s", prefixes, want)
		}
	}
}

func TestBuildV2PrimaryCidrSameSource(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
	}, "\n"))
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "src-build"}); err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	db, err := pebble.Open(dbDirFor(rootDir, "src-build"), &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open error = %v", err)
	}
	defer db.Close()

	iter, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("NewIter error = %v", err)
	}
	defer iter.Close()

	checked := 0
	for valid := iter.First(); valid; valid = iter.Next() {
		key := iter.Key()
		if len(key) == 0 || (key[0] != keyKindPrimaryV4 && key[0] != keyKindPrimaryV6) {
			continue
		}

		primaryPrefix, err := decodePrimaryKeyV2(key)
		if err != nil {
			t.Fatalf("decodePrimaryKeyV2 error = %v", err)
		}

		// 对应 cidr key 必须存在且 decode 出同一 prefix（同源不变量）。
		cidrKey, err := encodeCIDRKeyV2(primaryPrefix)
		if err != nil {
			t.Fatalf("encodeCIDRKeyV2 error = %v", err)
		}
		cidrVal, closer, err := db.Get(cidrKey)
		if err != nil {
			t.Fatalf("对应 cidr key 缺失: %v", err)
		}
		if len(cidrVal) != 0 {
			t.Fatalf("cidr value 应为零长度，实际 %d", len(cidrVal))
		}
		closer.Close()

		cidrPrefix, err := decodeCIDRKeyV2(cidrKey)
		if err != nil {
			t.Fatalf("decodeCIDRKeyV2 error = %v", err)
		}
		if cidrPrefix != primaryPrefix {
			t.Fatalf("cidr/primary 还原不一致: %v vs %v", cidrPrefix, primaryPrefix)
		}

		// primary value 解出正确业务字段，且 Network 为空（待 query 回填）。
		rec, err := decodeBaseRecordValueV2(iter.Value())
		if err != nil {
			t.Fatalf("decodeBaseRecordValueV2 error = %v", err)
		}
		if rec.Network != "" {
			t.Fatalf("decode 返回的 Network 应为空，实际 %q", rec.Network)
		}
		if rec.Country != "Australia" || rec.ASN != "AS13335" || rec.ASDomain != "cloudflare.com" {
			t.Fatalf("业务字段解码错误: %#v", rec)
		}
		checked++
	}
	if checked != 1 {
		t.Fatalf("应校验 1 条 primary 记录，实际 %d", checked)
	}
}

func TestBuildV2RejectsIPv4MappedIPv6(t *testing.T) {
	rootDir := t.TempDir()
	// ::ffff:1.2.3.4 → IPv4-mapped IPv6，v1 parse 合法但 v2 encode 拒绝（family 二义）。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`::ffff:1.2.3.4,A,A,A,A,,,`,
	}, "\n"))

	_, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "mapped-build"})
	if err == nil {
		t.Fatal("buildV2FromCSV() error = nil, want IPv4-mapped IPv6 rejection")
	}
	if !strings.Contains(err.Error(), "IPv4-mapped") {
		t.Fatalf("error = %v, want IPv4-mapped 拒绝", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, versionsDirName, "mapped-build")); !os.IsNotExist(statErr) {
		t.Fatalf("失败不应留正式目录，statErr = %v", statErr)
	}
}

func TestBuildV2RejectsBadInputs(t *testing.T) {
	t.Run("相对路径", func(t *testing.T) {
		_, err := buildV2FromCSV(t.TempDir(), BuildOptions{CSVPath: "relative.csv"})
		if err == nil || !strings.Contains(err.Error(), "绝对路径") {
			t.Fatalf("error = %v, want 绝对路径", err)
		}
	})

	t.Run("表头不符", func(t *testing.T) {
		csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
			"network,wrong,header,cols,x,y,z,w",
			`1.0.0.0/24,A,A,A,A,,,`,
		}, "\n"))
		_, err := buildV2FromCSV(t.TempDir(), BuildOptions{CSVPath: csvPath, BuildID: "badhdr"})
		if err == nil || !strings.Contains(err.Error(), "CSV 头不匹配") {
			t.Fatalf("error = %v, want CSV 头不匹配", err)
		}
	})

	t.Run("非规范网段带host位", func(t *testing.T) {
		csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
			strings.Join(expectedCSVHeader, ","),
			`10.0.0.1/24,A,A,A,A,,,`,
		}, "\n"))
		_, err := buildV2FromCSV(t.TempDir(), BuildOptions{CSVPath: csvPath, BuildID: "hostbits"})
		if err == nil || !strings.Contains(err.Error(), "不是规范网段") {
			t.Fatalf("error = %v, want 不是规范网段", err)
		}
	})
}
