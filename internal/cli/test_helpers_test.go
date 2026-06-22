package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"geoprism/backend/ipdb"

	"github.com/cockroachdb/pebble/v2"
)

// metadataKeyBytes 是 ipdb 库的 metadata key 字节（keyFamilyMeta=0x00 + "meta"）。
// 用于 cli 端到端测试手工篡改库 metadata（如造 v1 fixture），避免跨包依赖 ipdb 内部符号。
// 来源：backend/ipdb/codec.go 的 metadataKey = []byte{keyFamilyMeta, 'm','e','t','a'}。
var metadataKeyBytes = []byte{0x00, 'm', 'e', 't', 'a'}

// rewriteIPDBMetadata 把 rootDir 下 buildID 库的 metadata 覆写为 meta。
// 用于 cli 测试构造 v1 / 缺 capability 等非默认库（公开 BuildFromCSV 现产 v2 库）。
func rewriteIPDBMetadata(t *testing.T, rootDir, buildID string, meta ipdb.Metadata) {
	t.Helper()
	dbDir := filepath.Join(rootDir, "versions", buildID, "db")
	db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("读写打开库失败: %v", err)
	}
	metaValue, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata 失败: %v", err)
	}
	if err := db.Set(metadataKeyBytes, metaValue, nil); err != nil {
		t.Fatalf("写入 metadata 失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭库失败: %v", err)
	}
}

// silentLogger 与 backend/ipdb 的 silentLogger 行为一致（抑制 Pebble 日志），cli 测试独立定义避免跨包。
type silentLogger struct{}

func (silentLogger) Infof(string, ...interface{})  {}
func (silentLogger) Errorf(string, ...interface{}) {}
func (silentLogger) Fatalf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func buildTestIPDB(t *testing.T, rootDir string) {
	t.Helper()

	csvPath := writeTestCSVFixture(t, t.TempDir(), strings.Join([]string{
		"network,country,country_code,continent,continent_code,asn,as_name,as_domain",
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
		`2001:db8::/48,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
	}, "\n"))

	if _, err := ipdb.BuildFromCSV(rootDir, ipdb.BuildOptions{
		CSVPath: csvPath,
		BuildID: "test-build",
	}); err != nil {
		t.Fatalf("BuildFromCSV() error = %v", err)
	}
}

func writeTestCSVFixture(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "fixture.csv")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write CSV fixture error = %v", err)
	}
	return path
}
