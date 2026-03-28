package settings

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DataSourcePriority 数据源优先级。
type DataSourcePriority string

const (
	PriorityIPDBFirst   DataSourcePriority = "ipdb-first"
	PriorityIPInfoFirst DataSourcePriority = "ipinfo-first"
)

// Settings 应用级配置。
type Settings struct {
	IPInfoToken        string             `toml:"ipinfo_token"`
	DataSourcePriority DataSourcePriority `toml:"data_source_priority"`
}

// SettingsStore 管理应用配置的加载和读取。
type SettingsStore struct {
	settings Settings
}

//go:embed defaults/settings.toml
var defaultSettingsTOML []byte

// NewSettingsStore 从 configDir 加载 settings.toml，不存在则写入默认模板。
func NewSettingsStore(configDir string) (*SettingsStore, error) {
	configPath := filepath.Join(configDir, "settings.toml")

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if writeErr := os.WriteFile(configPath, defaultSettingsTOML, 0644); writeErr != nil {
			return nil, fmt.Errorf("写入默认配置失败: %w", writeErr)
		}
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var wrapper struct {
		Settings Settings `toml:"settings"`
	}
	meta, err := toml.Decode(string(data), &wrapper)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if len(meta.Undecoded()) > 0 {
		return nil, fmt.Errorf("配置包含未知字段: %v", meta.Undecoded())
	}

	s := wrapper.Settings
	if s.DataSourcePriority == "" {
		s.DataSourcePriority = PriorityIPDBFirst
	}

	return &SettingsStore{settings: s}, nil
}

// IPInfoToken 返回 ipinfo API token。
func (s *SettingsStore) IPInfoToken() string {
	if s == nil {
		return ""
	}
	return s.settings.IPInfoToken
}

// DataSourcePriority 返回数据源优先级。
func (s *SettingsStore) DataSourcePriority() DataSourcePriority {
	if s == nil {
		return PriorityIPDBFirst
	}
	return s.settings.DataSourcePriority
}

// IsIPInfoEnabled 返回是否启用了 ipinfo 集成（token 非空）。
func (s *SettingsStore) IsIPInfoEnabled() bool {
	return s != nil && s.settings.IPInfoToken != ""
}

// IsIPInfoFirst 返回是否以 ipinfo 为优先数据源。
func (s *SettingsStore) IsIPInfoFirst() bool {
	return s != nil && s.settings.DataSourcePriority == PriorityIPInfoFirst
}
