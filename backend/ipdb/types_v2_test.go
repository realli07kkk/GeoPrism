package ipdb

import (
	"encoding/json"
	"strings"
	"testing"
)

// 验收第 23 条：capability 位组合可正确表示（位运算）。
func TestSchemaFeaturesCapabilityBits(t *testing.T) {
	// 完整 v2 base 库的 capability 组合。
	full := SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx
	if full&SchemaFeaturePrimaryLPM == 0 {
		t.Fatal("PrimaryLPM 位未设置")
	}
	if full&SchemaFeatureCIDRStartIdx == 0 {
		t.Fatal("CIDRStartIdx 位未设置")
	}
	// 预留位默认不参与完整组合。
	if full&SchemaFeatureCIDRInlineValue != 0 {
		t.Fatal("CIDRInlineValue 预留位不应被默认设置")
	}
	// 各 capability 位互不重叠（不同位）。
	if SchemaFeaturePrimaryLPM == SchemaFeatureCIDRStartIdx {
		t.Fatal("两个 capability 不应相等")
	}
	// 零值 = 无 v2 capability。
	var zero SchemaFeatures
	if zero != 0 {
		t.Fatalf("零值应为 0，got %d", zero)
	}
}

// 验收第 24 条：schema_features JSON tag 行为。
// - Metadata{SchemaFeatures:0} marshal 不含 schema_features 字段（omitempty 生效，v1 行为不变）
// - 旧 JSON（无 schema_features）反序列化得零值 0
// - 非零 marshal 正常输出字段
func TestMetadataSchemaFeaturesJSONOmitEmpty(t *testing.T) {
	// 1. 零值 marshal 不含字段（v1 库 metadata 行为不变）。
	zeroMeta := Metadata{FormatVersion: 1, RowCount: 100}
	out, err := json.Marshal(zeroMeta)
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}
	if strings.Contains(string(out), "schema_features") {
		t.Fatalf("SchemaFeatures=0 时 marshal 不应包含 schema_features 字段，got %s", out)
	}

	// 2. 旧 JSON（无 schema_features 字段）反序列化得零值 0。
	oldJSON := `{"format_version":1,"source_csv":"x.csv","built_at":"2026-01-01T00:00:00Z","row_count":100,"ipv4_count":100,"ipv6_count":0,"builder":"geoprism","pebble_module":"m","pebble_version":"v"}`
	var decoded Metadata
	if err := json.Unmarshal([]byte(oldJSON), &decoded); err != nil {
		t.Fatalf("unmarshal old JSON error = %v", err)
	}
	if decoded.SchemaFeatures != 0 {
		t.Fatalf("旧 JSON 反序列化 SchemaFeatures 应为 0，got %d", decoded.SchemaFeatures)
	}

	// 3. 非零 marshal 正常输出字段。
	v2Meta := Metadata{
		FormatVersion:  2,
		SchemaFeatures: SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx,
	}
	out2, err := json.Marshal(v2Meta)
	if err != nil {
		t.Fatalf("marshal v2 error = %v", err)
	}
	if !strings.Contains(string(out2), "schema_features") {
		t.Fatalf("非零 SchemaFeatures marshal 应包含 schema_features 字段，got %s", out2)
	}
	// 反序列化 round-trip 精确。
	var decoded2 Metadata
	if err := json.Unmarshal(out2, &decoded2); err != nil {
		t.Fatalf("unmarshal v2 error = %v", err)
	}
	if decoded2.SchemaFeatures != v2Meta.SchemaFeatures {
		t.Fatalf("非零 round-trip 失败: got %d want %d", decoded2.SchemaFeatures, v2Meta.SchemaFeatures)
	}
}

// v1 Metadata（FormatVersion=1，无 schema_features）marshal/unmarshal 双向稳定，
// 确保 v1 行为不被破坏（回归保护）。
func TestMetadataV1RegressionNoSchemaFeatures(t *testing.T) {
	original := Metadata{
		FormatVersion: 1,
		SourceCSV:     "/data/ipinfo.csv",
		RowCount:      5000,
		IPv4Count:     4500,
		IPv6Count:     500,
		Builder:       "geoprism",
	}
	out, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}
	if strings.Contains(string(out), "schema_features") {
		t.Fatalf("v1 Metadata marshal 不应出现 schema_features：v1 行为被破坏，got %s", out)
	}
	var decoded Metadata
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}
	decoded.SchemaFeatures = 0 // 原值为 0，显式对齐避免零值歧义
	if decoded.FormatVersion != original.FormatVersion ||
		decoded.SourceCSV != original.SourceCSV ||
		decoded.RowCount != original.RowCount {
		t.Fatalf("v1 Metadata round-trip 字段丢失: %#v", decoded)
	}
}
