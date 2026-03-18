package ipdb

import "time"

const (
	keyFamilyMeta byte = 0x00
	keyFamilyIPv4 byte = 0x04
	keyFamilyIPv6 byte = 0x06

	currentFormatVersion    byte = 1
	defaultPebbleModulePath      = "github.com/cockroachdb/pebble/v2"
	defaultPebbleVersion         = "unknown"
	metadataKeyName              = "meta"
	currentFileName              = "CURRENT"
	versionsDirName              = "versions"
	dbDirName                    = "db"
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
}

// BuildOptions 表示构建参数。
type BuildOptions struct {
	CSVPath string
	BuildID string
	BuiltAt time.Time
}
