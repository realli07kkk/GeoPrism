package provider

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
)

// Protocol DNS 协议类型
type Protocol string

const (
	ProtocolDoH Protocol = "doh"
	ProtocolDoT Protocol = "dot"
	ProtocolDNS Protocol = "dns" // 原生 DNS (UDP)
)

const providersConfigFileName = "providers.toml"

//go:embed defaults/providers.toml
var defaultProvidersTOML []byte

// Provider DNS Provider 配置
type Provider struct {
	ID         string   `json:"id" toml:"id"`
	Name       string   `json:"name" toml:"name"`
	Protocol   Protocol `json:"protocol" toml:"protocol"`
	Endpoint   string   `json:"endpoint" toml:"endpoint"`       // DoH URL
	ServerName string   `json:"server_name" toml:"server_name"` // DoT / DNS 地址或 DoT SNI
	Port       int      `json:"port" toml:"port"`               // 端口
	Timeout    int      `json:"timeout" toml:"timeout_ms"`      // 超时毫秒
	Enabled    bool     `json:"enabled" toml:"enabled"`
	Tags       []string `json:"tags" toml:"tags"`
}

// ProviderStore Provider 存储管理
type ProviderStore struct {
	configPath string
	mu         sync.RWMutex
	providers  map[string]Provider
}

type providersTOML struct {
	Providers []Provider `toml:"providers"`
}

// NewProviderStore 创建 Provider 存储
func NewProviderStore(configDir string) (*ProviderStore, error) {
	configPath := filepath.Join(configDir, providersConfigFileName)
	store := &ProviderStore{
		configPath: configPath,
		providers:  make(map[string]Provider),
	}

	// 确保目录存在
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}

	if err := store.ensureConfigFile(); err != nil {
		return nil, err
	}

	if err := store.load(); err != nil {
		return nil, err
	}

	return store, nil
}

// ensureConfigFile 确保默认配置文件存在。
func (s *ProviderStore) ensureConfigFile() error {
	if _, err := os.Stat(s.configPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	legacyJSONPath := filepath.Join(filepath.Dir(s.configPath), "providers.json")
	if _, err := os.Stat(legacyJSONPath); err == nil {
		fmt.Fprintf(os.Stderr, "警告: 发现旧版 providers.json，请手动迁移至 providers.toml；当前将写入默认配置\n")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	return os.WriteFile(s.configPath, defaultProvidersTOML, 0644)
}

// load 从文件加载配置
func (s *ProviderStore) load() error {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return err
	}

	var cfg providersTOML
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return fmt.Errorf("解析 Provider TOML 失败: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return fmt.Errorf("Provider 配置包含未知字段: %s", joinUndecodedKeys(undecoded))
	}

	providers := make(map[string]Provider, len(cfg.Providers))
	for idx, provider := range cfg.Providers {
		p := normalizeProvider(provider)
		if err := validateProvider(p); err != nil {
			return fmt.Errorf("Provider[%d] 校验失败: %w", idx, err)
		}
		if _, exists := providers[p.ID]; exists {
			return fmt.Errorf("Provider[%d] 校验失败: id %q 重复", idx, p.ID)
		}
		providers[p.ID] = p
	}

	s.providers = providers
	return nil
}

// save 保存配置到文件
func (s *ProviderStore) save() error {
	ids := make([]string, 0, len(s.providers))
	for id := range s.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	cfg := providersTOML{
		Providers: make([]Provider, 0, len(ids)),
	}
	for _, id := range ids {
		cfg.Providers = append(cfg.Providers, s.providers[id])
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}

	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return os.WriteFile(s.configPath, data, 0644)
}

// List 获取所有 Provider
func (s *ProviderStore) List() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Provider, 0, len(s.providers))
	for _, p := range s.providers {
		result = append(result, p)
	}
	return result
}

// Get 获取单个 Provider
func (s *ProviderStore) Get(id string) (Provider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.providers[id]
	return p, ok
}

// Upsert 创建或更新 Provider
func (s *ProviderStore) Upsert(p Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	p = normalizeProvider(p)
	if err := validateProvider(p); err != nil {
		return err
	}
	s.providers[p.ID] = p
	return s.save()
}

// Delete 删除 Provider
func (s *ProviderStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.providers[id]; !ok {
		return nil
	}
	delete(s.providers, id)
	return s.save()
}

// GetEnabled 获取所有启用的 Provider
func (s *ProviderStore) GetEnabled() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Provider, 0)
	for _, p := range s.providers {
		if p.Enabled {
			result = append(result, p)
		}
	}
	return result
}

// ProviderView 前端展示用的 Provider 结构
type ProviderView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Protocol   Protocol `json:"protocol"`
	Endpoint   string   `json:"endpoint"`
	ServerName string   `json:"server_name"`
	Port       int      `json:"port"`
	Timeout    int      `json:"timeout"`
	Enabled    bool     `json:"enabled"`
	Tags       []string `json:"tags"`
}

// ToView 转换为前端视图
func (p *Provider) ToView() ProviderView {
	return ProviderView{
		ID:         p.ID,
		Name:       p.Name,
		Protocol:   p.Protocol,
		Endpoint:   p.Endpoint,
		ServerName: p.ServerName,
		Port:       p.Port,
		Timeout:    p.Timeout,
		Enabled:    p.Enabled,
		Tags:       p.Tags,
	}
}

// normalizeProvider 规范化 Provider 文本字段。
func normalizeProvider(p Provider) Provider {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Endpoint = strings.TrimSpace(p.Endpoint)
	p.ServerName = strings.TrimSpace(p.ServerName)
	return p
}

// validateProvider 校验 Provider 配置。
func validateProvider(p Provider) error {
	if p.ID == "" {
		return fmt.Errorf("id 不能为空")
	}
	if p.Name == "" {
		return fmt.Errorf("name 不能为空")
	}
	switch p.Protocol {
	case ProtocolDoH, ProtocolDoT, ProtocolDNS:
	default:
		return fmt.Errorf("protocol %q 非法", p.Protocol)
	}
	if p.Port <= 0 {
		return fmt.Errorf("port 必须大于 0")
	}
	if p.Timeout <= 0 {
		return fmt.Errorf("timeout_ms 必须大于 0")
	}
	if p.Protocol == ProtocolDoH && p.Endpoint == "" {
		return fmt.Errorf("protocol 为 doh 时 endpoint 不能为空")
	}
	if (p.Protocol == ProtocolDoT || p.Protocol == ProtocolDNS) && p.ServerName == "" {
		return fmt.Errorf("protocol 为 %s 时 server_name 不能为空", p.Protocol)
	}
	return nil
}

// joinUndecodedKeys 拼接未识别的 TOML 字段。
func joinUndecodedKeys(keys []toml.Key) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
