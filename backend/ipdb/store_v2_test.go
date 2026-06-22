package ipdb

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// buildV2Fixture 用固定数据构建一个 v2 base 库，返回其 rootDir（buildID 由调用方持有）。
func buildV2Fixture(t *testing.T, buildID string) (rootDir string) {
	t.Helper()
	rootDir = t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`2001:db8::/48,B,B,B,B,,,`,
	}, "\n"))
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: buildID}); err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}
	return rootDir
}

// buildV2FixtureCSV 用给定 CSV 内容构建一个 v2 base 库，返回 (rootDir, buildID)。
func buildV2FixtureCSV(t *testing.T, buildID, csvContent string) (rootDir string) {
	t.Helper()
	rootDir = t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), csvContent)
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: buildID}); err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}
	return rootDir
}

// openV2Fixture 打开 buildV2FixtureCSV 产出的库，返回 *BaseStore（调用方 defer Close）。
func openV2Fixture(t *testing.T, rootDir, buildID string) *BaseStore {
	t.Helper()
	store, err := openBaseV2(rootDir, buildID)
	if err != nil {
		t.Fatalf("openBaseV2() error = %v", err)
	}
	return store
}

func TestOpenBaseV2Success(t *testing.T) {
	rootDir := buildV2Fixture(t, "ob-build")
	store, err := openBaseV2(rootDir, "ob-build")
	if err != nil {
		t.Fatalf("openBaseV2() error = %v", err)
	}

	// 版本定位上下文正确填充（SDD 结构契约：rootDir/buildID/dbDirPath）。
	if store.rootDir != rootDir || store.buildID != "ob-build" {
		t.Fatalf("版本上下文未正确填充: rootDir=%q buildID=%q", store.rootDir, store.buildID)
	}
	if store.dbDirPath != dbDirFor(rootDir, "ob-build") {
		t.Fatalf("dbDirPath = %q, want %q", store.dbDirPath, dbDirFor(rootDir, "ob-build"))
	}

	if store.Metadata().FormatVersion != int(formatVersionV2) {
		t.Fatalf("FormatVersion = %d, want %d", store.Metadata().FormatVersion, formatVersionV2)
	}
	if want := SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx; store.Metadata().SchemaFeatures != want {
		t.Fatalf("SchemaFeatures = %d, want %d", store.Metadata().SchemaFeatures, want)
	}

	// Close 幂等：连续两次都不报错。
	if err := store.Close(); err != nil {
		t.Fatalf("第一次 Close error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("第二次 Close 应幂等，error = %v", err)
	}
}

func TestOpenBaseV2IsReadOnly(t *testing.T) {
	rootDir := buildV2Fixture(t, "ro-build")
	store, err := openBaseV2(rootDir, "ro-build")
	if err != nil {
		t.Fatalf("openBaseV2() error = %v", err)
	}
	defer store.Close()

	// 直接对底层句柄写入应失败（base 运行期只读）。
	if err := store.db.Set([]byte{keyKindOverlayV4, 1, 2, 3, 4}, []byte("x"), nil); err == nil {
		t.Fatal("ReadOnly base 库写入应失败，实际成功")
	}
}

func TestOpenBaseV2RejectsEmptyDir(t *testing.T) {
	rootDir := t.TempDir()
	dbDir := dbDirFor(rootDir, "empty-build")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	// 建一个空 Pebble 库（无 metadata key），再关闭。
	db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close error = %v", err)
	}

	if _, err := openBaseV2(rootDir, "empty-build"); err == nil {
		t.Fatal("openBaseV2() 打开无 metadata 的空库应失败")
	}
}

func TestOpenBaseV2RejectsV1Format(t *testing.T) {
	// 用 buildV2FixtureCSV 建 v2 库后覆盖 metadata 为 FormatVersion=1，
	// 构造 v1 格式库（公开 BuildFromCSV 收口后产 v2，不能再用来造 v1 库）。
	rootDir := buildV2FixtureCSV(t, "v1-build", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
	}, "\n"))
	writeStoreMetadata(t, rootDir, "v1-build", Metadata{FormatVersion: 1, RowCount: 1, SchemaFeatures: 0})

	_, err := openBaseV2(rootDir, "v1-build")
	if err == nil {
		t.Fatal("openBaseV2() 打开 v1 格式库应失败")
	}
	if !strings.Contains(err.Error(), "格式版本") {
		t.Fatalf("error = %v, want 格式版本不符", err)
	}
}

// TestBaseStoreLookupIPLPM 验证真 LPM ladder：被大网段覆盖、同时存在更具体重叠记录时，
// 单 IP 查询返回最长前缀。这是 roadmap 核心正确性目标。
func TestBaseStoreLookupIPLPM(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,Root,R,R,R,AS1,Root AS,root.example`,
		`10.1.0.0/16,Mid,M,M,M,AS2,Mid AS,mid.example`,
		`10.1.2.0/24,Leaf,L,L,L,AS3,Leaf AS,leaf.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "lpm-build", csv)
	store := openV2Fixture(t, rootDir, "lpm-build")
	defer store.Close()

	// 10.1.2.3 被 /8、/16、/24 同时覆盖，应命中最长前缀 /24。
	rec, matched, err := store.LookupIP(mustParseAddr(t, "10.1.2.3"))
	if err != nil {
		t.Fatalf("LookupIP(10.1.2.3) error = %v", err)
	}
	if !matched {
		t.Fatal("LookupIP(10.1.2.3) matched = false, want true")
	}
	if rec.Network != "10.1.2.0/24" {
		t.Fatalf("LookupIP(10.1.2.3) Network = %q, want 10.1.2.0/24", rec.Network)
	}
	if rec.ASN != "AS3" {
		t.Fatalf("LookupIP(10.1.2.3) ASN = %q, want AS3", rec.ASN)
	}

	// 10.1.5.5 被 /8、/16 覆盖（/24 不含），应命中 /16。
	rec, matched, err = store.LookupIP(mustParseAddr(t, "10.1.5.5"))
	if err != nil {
		t.Fatalf("LookupIP(10.1.5.5) error = %v", err)
	}
	if !matched || rec.Network != "10.1.0.0/16" {
		t.Fatalf("LookupIP(10.1.5.5) = (matched=%v, %q), want (true, 10.1.0.0/16)", matched, rec.Network)
	}

	// 20.0.0.1 无覆盖，matched=false。
	_, matched, err = store.LookupIP(mustParseAddr(t, "20.0.0.1"))
	if err != nil {
		t.Fatalf("LookupIP(20.0.0.1) error = %v", err)
	}
	if matched {
		t.Fatal("LookupIP(20.0.0.1) matched = true, want false")
	}
}

// TestBaseStoreLookupIPNetworkBackfill 验证命中结果 Network 非空且回填正确（/32 边界）。
func TestBaseStoreLookupIPNetworkBackfill(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.7.168.172,India,IN,Asia,AS,AS9583,Sify Limited,sifycorp.com`,
		`2001:db8::1,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "backfill-build", csv)
	store := openV2Fixture(t, rootDir, "backfill-build")
	defer store.Close()

	// /32（IPv4）：单 IP 网段，Network 回填为 1.7.168.172/32。
	rec, matched, err := store.LookupIP(mustParseAddr(t, "1.7.168.172"))
	if err != nil || !matched {
		t.Fatalf("LookupIP(1.7.168.172) = (err=%v, matched=%v), want hit", err, matched)
	}
	if rec.Network != "1.7.168.172/32" {
		t.Fatalf("LookupIP(1.7.168.172) Network = %q, want 1.7.168.172/32", rec.Network)
	}

	// /128（IPv6）：单 IP 网段，Network 回填为 2001:db8::1/128。
	rec, matched, err = store.LookupIP(mustParseAddr(t, "2001:db8::1"))
	if err != nil || !matched {
		t.Fatalf("LookupIP(2001:db8::1) = (err=%v, matched=%v), want hit", err, matched)
	}
	if rec.Network != "2001:db8::1/128" {
		t.Fatalf("LookupIP(2001:db8::1) Network = %q, want 2001:db8::1/128", rec.Network)
	}
}

func mustParseAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("netip.ParseAddr(%q) error = %v", s, err)
	}
	return addr
}

func mustParsePrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("netip.ParsePrefix(%q) error = %v", s, err)
	}
	return p
}

// recordNetworks 把 []Record 转成 Network 字符串列表，便于断言。
func recordNetworks(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Network)
	}
	return out
}

// assertNetworks 比对 records 的 Network 列表（顺序敏感，验证确定性排序）。
func assertNetworks(t *testing.T, got []Record, want []string) {
	t.Helper()
	gotNetworks := recordNetworks(got)
	if len(gotNetworks) != len(want) {
		t.Fatalf("networks = %v, want %v", gotNetworks, want)
	}
	for i := range want {
		if gotNetworks[i] != want[i] {
			t.Fatalf("networks[%d] = %q, want %q (got=%v)", i, gotNetworks[i], want[i], gotNetworks)
		}
	}
}

// TestBaseStoreLookupCIDRThreePhase 验证三段 CIDR 查询：
// ancestors（多层祖先）+ self + descendants 合并，结果确定性排序。
func TestBaseStoreLookupCIDRThreePhase(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,Root,R,R,R,AS1,Root AS,root.example`,
		`10.1.0.0/16,Mid,M,M,M,AS2,Mid AS,mid.example`,
		`10.1.2.0/24,Leaf,L,L,L,AS3,Leaf AS,leaf.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "cidr3-build", csv)
	store := openV2Fixture(t, rootDir, "cidr3-build")
	defer store.Close()

	// 查 10.1.2.0/24：ancestors 命中 /8 + /16（L=8、L=16），self 命中 /24，descendants 无。
	// 三段合并去重排序 → [/8, /16, /24]。
	records, err := store.LookupCIDR(mustParsePrefix(t, "10.1.2.0/24"))
	if err != nil {
		t.Fatalf("LookupCIDR(10.1.2.0/24) error = %v", err)
	}
	assertNetworks(t, records, []string{"10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24"})
}

// TestBaseStoreLookupCIDRMultiAncestors 验证多层祖先：
// 库只含 /8 + /16，查 /24 应返回两条（v1 单次 Prev 只能补一条会漏 /8）。
func TestBaseStoreLookupCIDRMultiAncestors(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,Root,R,R,R,AS1,Root AS,root.example`,
		`10.1.0.0/16,Mid,M,M,M,AS2,Mid AS,mid.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "multi-anc-build", csv)
	store := openV2Fixture(t, rootDir, "multi-anc-build")
	defer store.Close()

	records, err := store.LookupCIDR(mustParsePrefix(t, "10.1.2.0/24"))
	if err != nil {
		t.Fatalf("LookupCIDR error = %v", err)
	}
	assertNetworks(t, records, []string{"10.0.0.0/8", "10.1.0.0/16"})
}

// TestBaseStoreLookupCIDRAncestorsSupersetStart 验证 ancestors 捕获超集起始<query 的情况
// （防 ancestors 退化成纯 query mask 链）。
func TestBaseStoreLookupCIDRAncestorsSupersetStart(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,Net24,N,N,N,AS1,AS,as.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "superset-build", csv)
	store := openV2Fixture(t, rootDir, "superset-build")
	defer store.Close()

	// 查 1.0.0.128/25：1.0.0.0/24 起始 < query 起始但包含 query.Addr()=1.0.0.128。
	// ancestors L=24 经 PrefixFrom(1.0.0.128,24).Masked()=1.0.0.0/24 命中。
	records, err := store.LookupCIDR(mustParsePrefix(t, "1.0.0.128/25"))
	if err != nil {
		t.Fatalf("LookupCIDR(1.0.0.128/25) error = %v", err)
	}
	assertNetworks(t, records, []string{"1.0.0.0/24"})
}

// TestBaseStoreLookupCIDRDescendants 验证 self + descendants 段。
func TestBaseStoreLookupCIDRDescendants(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
		`10.0.0.0/25,P,P,P,P,AS1,AS,as.example`,
		`10.0.0.128/25,P,P,P,P,AS1,AS,as.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "desc-build", csv)
	store := openV2Fixture(t, rootDir, "desc-build")
	defer store.Close()

	// 查 10.0.0.0/24：self(/24) + 2 个 descendants(/25)，排序后 [/24, 10.0.0.0/25, 10.0.0.128/25]。
	records, err := store.LookupCIDR(mustParsePrefix(t, "10.0.0.0/24"))
	if err != nil {
		t.Fatalf("LookupCIDR(10.0.0.0/24) error = %v", err)
	}
	assertNetworks(t, records, []string{"10.0.0.0/24", "10.0.0.0/25", "10.0.0.128/25"})
}

// TestBaseStoreLookupCIDRNoMatch 验证无相交记录返回 nil。
func TestBaseStoreLookupCIDRNoMatch(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "nomatch-build", csv)
	store := openV2Fixture(t, rootDir, "nomatch-build")
	defer store.Close()

	records, err := store.LookupCIDR(mustParsePrefix(t, "20.0.0.0/24"))
	if err != nil {
		t.Fatalf("LookupCIDR error = %v", err)
	}
	if records != nil && len(records) != 0 {
		t.Fatalf("LookupCIDR(20.0.0.0/24) = %v, want nil/empty", recordNetworks(records))
	}
}

// TestBaseStoreLookupCIDRIPv6 验证 IPv6 三段查询。
func TestBaseStoreLookupCIDRIPv6(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`2001:db8::/32,V6Root,R,R,R,AS1,AS,as.example`,
		`2001:db8:1::/48,V6Mid,M,M,M,AS2,AS,as.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "v6cidr-build", csv)
	store := openV2Fixture(t, rootDir, "v6cidr-build")
	defer store.Close()

	records, err := store.LookupCIDR(mustParsePrefix(t, "2001:db8:1::/64"))
	if err != nil {
		t.Fatalf("LookupCIDR error = %v", err)
	}
	assertNetworks(t, records, []string{"2001:db8::/32", "2001:db8:1::/48"})
}

// TestBaseStoreLookupCIDRCorruptIndex 验证 cidr 索引存在但 primary 缺失 → ErrCorruptIndex。
// 构造方式：构建正常 v2 库后，用读写句柄删掉一条 primary key（保留其 cidr key），
// 再 ReadOnly 打开查询。
func TestBaseStoreLookupCIDRCorruptIndex(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "corrupt-build", csv)

	// 用读写句柄删除 primary key（保留 cidr key），制造索引不一致。
	dbDir := filepath.Join(rootDir, versionsDirName, "corrupt-build", dbDirName)
	rwDB, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("读写打开库失败: %v", err)
	}
	primaryKey, err := encodePrimaryKeyV2(mustParsePrefix(t, "10.0.0.0/24"))
	if err != nil {
		t.Fatalf("encode primary key 失败: %v", err)
	}
	if err := rwDB.Delete(primaryKey, nil); err != nil {
		t.Fatalf("删除 primary key 失败: %v", err)
	}
	if err := rwDB.Close(); err != nil {
		t.Fatalf("关闭读写库失败: %v", err)
	}

	store := openV2Fixture(t, rootDir, "corrupt-build")
	defer store.Close()

	_, err = store.LookupCIDR(mustParsePrefix(t, "10.0.0.0/24"))
	if err == nil {
		t.Fatal("LookupCIDR 期望 ErrCorruptIndex，实际 nil")
	}
	if !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("LookupCIDR error = %v, want ErrCorruptIndex", err)
	}
}

// writeStoreMetadata 用读写句柄直接覆盖某 buildID 库的 metadata JSON。
// 用于构造 v1 格式 / 缺 capability 的测试库（绕开 BuildFromCSV，避免收口连带失效）。
func writeStoreMetadata(t *testing.T, rootDir, buildID string, meta Metadata) {
	t.Helper()
	dbDir := filepath.Join(rootDir, versionsDirName, buildID, dbDirName)
	db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("读写打开库失败: %v", err)
	}
	metaValue, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata 失败: %v", err)
	}
	if err := db.Set(metadataKey, metaValue, nil); err != nil {
		t.Fatalf("写入 metadata 失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭库失败: %v", err)
	}
}

// TestOpenCurrentV2Success 验证 OpenCurrent 打开完整 v2 库成功，查询正确。
func TestOpenCurrentV2Success(t *testing.T) {
	csv := strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,Root,R,R,R,AS1,Root AS,root.example`,
		`10.1.0.0/16,Mid,M,M,M,AS2,Mid AS,mid.example`,
		`10.1.2.0/24,Leaf,L,L,L,AS3,Leaf AS,leaf.example`,
	}, "\n")
	rootDir := buildV2FixtureCSV(t, "oc-success", csv)

	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent() error = %v", err)
	}
	defer store.Close()

	// 真 LPM 查询验证 delegation 生效。
	match, err := store.LookupIP("10.1.2.3")
	if err != nil {
		t.Fatalf("LookupIP error = %v", err)
	}
	if !match.Matched || match.Record.Network != "10.1.2.0/24" {
		t.Fatalf("LookupIP(10.1.2.3) = (matched=%v, %q), want (true, 10.1.2.0/24)", match.Matched, match.Record.Network)
	}

	// CIDR delegation 验证。
	records, err := store.LookupCIDR("10.1.2.0/24")
	if err != nil {
		t.Fatalf("LookupCIDR error = %v", err)
	}
	assertNetworks(t, records, []string{"10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24"})

	// Metadata delegation 验证。
	if got := store.Metadata().FormatVersion; got != int(formatVersionV2) {
		t.Fatalf("Metadata().FormatVersion = %d, want %d", got, formatVersionV2)
	}
}

// TestOpenCurrentRejectsLegacyFormat 验证 v1 库 → ErrLegacyFormat（finding 1 边界）。
// 用手工写 FormatVersion=1 的 metadata 构造 v1 库，避免依赖公开 BuildFromCSV（收口后会产 v2）。
func TestOpenCurrentRejectsLegacyFormat(t *testing.T) {
	rootDir := buildV2FixtureCSV(t, "oc-legacy", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
	}, "\n"))

	// 覆盖 metadata 为 v1 格式（FormatVersion=1，无 SchemaFeatures）。
	writeStoreMetadata(t, rootDir, "oc-legacy", Metadata{
		FormatVersion:  1,
		RowCount:       1,
		SchemaFeatures: 0,
	})

	_, err := OpenCurrent(rootDir)
	if err == nil {
		t.Fatal("OpenCurrent() 期望 ErrLegacyFormat，实际 nil")
	}
	if !errors.Is(err, ErrLegacyFormat) {
		t.Fatalf("OpenCurrent() error = %v, want ErrLegacyFormat", err)
	}
}

// TestOpenCurrentRejectsIncompleteSchema 验证缺 capability → ErrIncompleteSchema。
func TestOpenCurrentRejectsIncompleteSchema(t *testing.T) {
	rootDir := buildV2FixtureCSV(t, "oc-incomplete", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
	}, "\n"))

	// 覆盖 metadata：FormatVersion=2 但 SchemaFeatures 只有 PrimaryLPM（缺 CIDRStartIdx）。
	writeStoreMetadata(t, rootDir, "oc-incomplete", Metadata{
		FormatVersion:  int(formatVersionV2),
		RowCount:       1,
		SchemaFeatures: SchemaFeaturePrimaryLPM,
	})

	_, err := OpenCurrent(rootDir)
	if err == nil {
		t.Fatal("OpenCurrent() 期望 ErrIncompleteSchema，实际 nil")
	}
	if !errors.Is(err, ErrIncompleteSchema) {
		t.Fatalf("OpenCurrent() error = %v, want ErrIncompleteSchema", err)
	}
}

// TestOpenCurrentNonLegacyErrors 验证 finding 1 边界：
// metadata 读不到/损坏 → 普通错误，非 ErrLegacyFormat。
func TestOpenCurrentNonLegacyErrors(t *testing.T) {
	t.Run("空目录无 metadata", func(t *testing.T) {
		rootDir := t.TempDir()
		// 构造 CURRENT 指向一个不存在的 buildID 目录会触发 ErrNoCurrentDatabase，
		// 这里构造一个存在但无 metadata 的空 Pebble 目录。
		dbDir := filepath.Join(rootDir, versionsDirName, "empty-build", dbDirName)
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			t.Fatalf("MkdirAll error = %v", err)
		}
		db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
		if err != nil {
			t.Fatalf("pebble.Open error = %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(rootDir, currentFileName), []byte("empty-build"), 0644); err != nil {
			t.Fatalf("write CURRENT error = %v", err)
		}

		_, err = OpenCurrent(rootDir)
		if err == nil {
			t.Fatal("OpenCurrent() 期望普通错误，实际 nil")
		}
		if errors.Is(err, ErrLegacyFormat) {
			t.Fatalf("OpenCurrent() 无 metadata 库不应判为 ErrLegacyFormat，got %v", err)
		}
	})
}

// TestOpenCurrentProbeDBReleased 验证 finding 2：错误路径返回后 probe DB 已关闭，
// 后续读写打开同目录不报 lock 冲突。
func TestOpenCurrentProbeDBReleased(t *testing.T) {
	rootDir := buildV2FixtureCSV(t, "oc-probe", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
	}, "\n"))

	// 错误路径（ErrLegacyFormat）：probe DB 必须在返回前关闭。
	writeStoreMetadata(t, rootDir, "oc-probe", Metadata{FormatVersion: 1, SchemaFeatures: 0})
	_, err := OpenCurrent(rootDir)
	if !errors.Is(err, ErrLegacyFormat) {
		t.Fatalf("OpenCurrent（v1）error = %v, want ErrLegacyFormat", err)
	}
	// ErrLegacyFormat 返回后，probe DB 应已释放——用读写句柄打开同目录验证无 lock 冲突。
	rwDB, err := pebble.Open(filepath.Join(rootDir, versionsDirName, "oc-probe", dbDirName),
		&pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("ErrLegacyFormat 后读写打开应成功（probe DB 已释放），error = %v", err)
	}
	rwDB.Close()

	// 恢复 v2 metadata，验证成功路径 probe DB 也释放（openBaseV2 二次打开无 lock）。
	writeStoreMetadata(t, rootDir, "oc-probe", Metadata{
		FormatVersion:  int(formatVersionV2),
		RowCount:       1,
		SchemaFeatures: SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx,
	})
	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("恢复 v2 后 OpenCurrent 应成功（前序 probe DB 均已释放），error = %v", err)
	}
	// store 持有 openBaseV2 打开的 base db，显式 Close 释放，再验证读写打开无 lock。
	store.Close()
	rwDB2, err := pebble.Open(filepath.Join(rootDir, versionsDirName, "oc-probe", dbDirName),
		&pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("store.Close 后读写打开应成功，error = %v", err)
	}
	rwDB2.Close()
}

// TestStoreWriteRecordFails 验证 finding 3：WriteRecord 显式返回失败，不依赖 s.db。
func TestStoreWriteRecordFails(t *testing.T) {
	rootDir := buildV2FixtureCSV(t, "oc-write", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
	}, "\n"))

	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent error = %v", err)
	}
	defer store.Close()

	err = store.WriteRecord(Record{Network: "10.0.0.0/32"})
	if err == nil {
		t.Fatal("WriteRecord() 期望失败 error，实际 nil")
	}
	// SDD §3 场景 20：文案应含"只读"（显式拒绝，非隐式 Pebble ReadOnly）。
	if !strings.Contains(err.Error(), "只读") {
		t.Fatalf("WriteRecord() error = %v, want 含 '只读'", err)
	}
}

// TestStoreLookupInvalidInput SDD §3 场景 19：非法输入返回明确 error。
func TestStoreLookupInvalidInput(t *testing.T) {
	rootDir := buildV2FixtureCSV(t, "invalid-build", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/24,P,P,P,P,AS1,AS,as.example`,
	}, "\n"))
	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent error = %v", err)
	}
	defer store.Close()

	if _, err := store.LookupIP("not-an-ip"); err == nil {
		t.Fatal("LookupIP('not-an-ip') 期望 error，实际 nil")
	}
	if _, err := store.LookupCIDR("not-a-cidr"); err == nil {
		t.Fatal("LookupCIDR('not-a-cidr') 期望 error，实际 nil")
	}
}

// TestStoreLookupIPDefaultRoute SDD §3 场景 11：/0 兜底，任意 IP 命中。
func TestStoreLookupIPDefaultRoute(t *testing.T) {
	rootDir := buildV2FixtureCSV(t, "defaultroute-build", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`0.0.0.0/0,Default,D,D,D,AS0,Default AS,default.example`,
	}, "\n"))
	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent error = %v", err)
	}
	defer store.Close()

	// 任意 IP 都应被 /0 兜底命中（ladder L=0）。
	match, err := store.LookupIP("203.0.113.42")
	if err != nil {
		t.Fatalf("LookupIP error = %v", err)
	}
	if !match.Matched || match.Record.Network != "0.0.0.0/0" {
		t.Fatalf("LookupIP(203.0.113.42) = (matched=%v, %q), want (true, 0.0.0.0/0)", match.Matched, match.Record.Network)
	}
}

// TestStoreLookupSameStartDifferentPrefixLen SDD §3 场景 13：
// 同起始地址、不同 prefixLen 合法共存，LookupIP 命中更长前缀，LookupCIDR 返回两条。
func TestStoreLookupSameStartDifferentPrefixLen(t *testing.T) {
	rootDir := buildV2FixtureCSV(t, "samestart-build", strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,Root,R,R,R,AS1,Root AS,root.example`,
		`10.0.0.0/16,Sub,S,S,S,AS2,Sub AS,sub.example`,
	}, "\n"))
	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent error = %v", err)
	}
	defer store.Close()

	// 10.0.0.5 同时被 /8 和 /16 覆盖，应命中最长前缀 /16。
	match, err := store.LookupIP("10.0.0.5")
	if err != nil {
		t.Fatalf("LookupIP error = %v", err)
	}
	if !match.Matched || match.Record.Network != "10.0.0.0/16" {
		t.Fatalf("LookupIP(10.0.0.5) = (matched=%v, %q), want (true, 10.0.0.0/16)", match.Matched, match.Record.Network)
	}

	// 查 10.0.0.0/24：ancestors 命中 /8 + /16（都覆盖 10.0.0.0），返回 2 条。
	records, err := store.LookupCIDR("10.0.0.0/24")
	if err != nil {
		t.Fatalf("LookupCIDR error = %v", err)
	}
	assertNetworks(t, records, []string{"10.0.0.0/8", "10.0.0.0/16"})
}
