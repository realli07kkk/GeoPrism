package ipdb

import "time"

const (
	keyFamilyMeta byte = 0x00
	keyFamilyIPv4 byte = 0x04
	keyFamilyIPv6 byte = 0x06

	// currentFormatVersion 是当前激活库的 FormatVersion。ipdb-v2-query 收口后升到 2，
	// 公开 BuildFromCSV/OpenCurrent 全切 v2。该常量也被 v1 value codec 用作 value version
	// 字节（v1 codec 现为死代码，归 ipdb-lookup-integration 清理）。
	currentFormatVersion byte = 2
	// formatVersionV2 是 v2 base 库的库级 FormatVersion。与 currentFormatVersion 收口后同值，
	// 保留独立命名以区分"库级版本"与"value 协议版本"语义。
	formatVersionV2         byte = 2
	defaultPebbleModulePath      = "github.com/cockroachdb/pebble/v2"
	defaultPebbleVersion         = "unknown"
	metadataKeyName              = "meta"
	currentFileName              = "CURRENT"
	versionsDirName              = "versions"
	dbDirName                    = "db"
	// stagingDirPrefix 是 v2 构建中间目录前缀：versions/.staging-{buildID}；
	// 关库后 rename 为正式目录 versions/{buildID} 再切 CURRENT。
	stagingDirPrefix = ".staging-"
)

// v2 key 首字节（kind），区分索引类型；与 v1 的 keyFamily* 前缀语义区分。
// 数值与 v1 的 0x04/0x06 不重叠，便于版本/用途识别。
const (
	keyKindPrimaryV4 byte = 0x14 // primary LPM 主索引（IPv4）
	keyKindPrimaryV6 byte = 0x16 // primary LPM 主索引（IPv6）
	keyKindCIDRV4    byte = 0x24 // cidr 二级索引（IPv4，value 零长度）
	keyKindCIDRV6    byte = 0x26 // cidr 二级索引（IPv6，value 零长度）
	keyKindOverlayV4 byte = 0x34 // overlay 单 IP 缓存（IPv4，仅 /32）
	keyKindOverlayV6 byte = 0x36 // overlay 单 IP 缓存（IPv6，仅 /128）
)

// SchemaFeatures 标识 base 离线库具备哪些索引能力（capability 位）。
// v1 旧库 JSON 无 schema_features 字段，反序列化为零值 0 = 无 v2 capability。
type SchemaFeatures uint32

const (
	// SchemaFeaturePrimaryLPM：含 primary LPM 主索引。
	SchemaFeaturePrimaryLPM SchemaFeatures = 1 << 0
	// SchemaFeatureCIDRStartIdx：含 cidr 起始地址二级索引（value 零长度）。
	SchemaFeatureCIDRStartIdx SchemaFeatures = 1 << 1
	// SchemaFeatureCIDRInlineValue：预留位——未来若 cidr 查询成瓶颈，
	// 以此 capability 引入"cidr 索引内联整份 Record"。本 feature 不使用。
	SchemaFeatureCIDRInlineValue SchemaFeatures = 1 << 2
)

// Record 表示单条离线 IP 信息。
type Record struct {
	Network       string `json:"network"`
	Country       string `json:"country"`
	CountryCode   string `json:"country_code"`
	Continent     string `json:"continent"`
	ContinentCode string `json:"continent_code"`
	ASN           string `json:"asn"`
	ASName        string `json:"as_name"`
	ASDomain      string `json:"as_domain"`
}

// Match 表示单个 IP 的匹配结果。
type Match struct {
	IP      string `json:"ip"`
	Matched bool   `json:"matched"`
	Record  Record `json:"record"`
}

// Metadata 表示离线库元信息。
type Metadata struct {
	FormatVersion int       `json:"format_version"`
	SourceCSV     string    `json:"source_csv"`
	BuiltAt       time.Time `json:"built_at"`
	RowCount      int64     `json:"row_count"`
	IPv4Count     int64     `json:"ipv4_count"`
	IPv6Count     int64     `json:"ipv6_count"`
	Builder       string    `json:"builder"`
	PebbleModule  string    `json:"pebble_module"`
	PebbleVersion string    `json:"pebble_version"`
	// SchemaFeatures 标识 base 库的索引能力（v2 新增）。
	// omitempty：v1 库 metadata 保持不含该字段，行为不变；
	// 旧 JSON 反序列化得零值 0 = 无 v2 capability。
	SchemaFeatures SchemaFeatures `json:"schema_features,omitempty"`
}

// BuildOptions 表示构建参数。
type BuildOptions struct {
	CSVPath string
	BuildID string
	BuiltAt time.Time
}

// OverlayMeta 表示单条 overlay 缓存记录的元信息（进 overlay value）。
// 与 OverlayMetadata（库级 JSON 元信息）不同，后者属 ipdb-overlay-store。
type OverlayMeta struct {
	Source    string    // 来源标识，如 "ipinfo"
	FetchedAt time.Time // 抓取时间
	// ExpiresAt 过期时间；零值表示永不过期（磁盘编码为 0）。
	ExpiresAt time.Time
}
