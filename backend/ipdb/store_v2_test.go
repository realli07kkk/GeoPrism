package ipdb

import (
	"os"
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
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
	}, "\n"))
	if _, err := BuildFromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "v1-build"}); err != nil {
		t.Fatalf("BuildFromCSV() error = %v", err)
	}

	_, err := openBaseV2(rootDir, "v1-build")
	if err == nil {
		t.Fatal("openBaseV2() 打开 v1 格式库应失败")
	}
	if !strings.Contains(err.Error(), "格式版本") {
		t.Fatalf("error = %v, want 格式版本不符", err)
	}
}
