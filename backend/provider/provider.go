package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Protocol DNS 协议类型
type Protocol string

const (
	ProtocolDoH Protocol = "doh"
	ProtocolDoT Protocol = "dot"
	ProtocolDNS Protocol = "dns" // 原生 DNS (UDP)
)

// Provider DNS Provider 配置
type Provider struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Protocol   Protocol  `json:"protocol"`
	Endpoint   string    `json:"endpoint"`    // DoH URL 或 DoT 地址
	ServerName string    `json:"server_name"` // DoT SNI
	Port       int       `json:"port"`        // DoT 端口，默认 853
	Timeout    int       `json:"timeout"`     // 超时毫秒
	Enabled    bool      `json:"enabled"`
	Tags       []string  `json:"tags"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ProviderStore Provider 存储管理
type ProviderStore struct {
	configPath string
	mu         sync.RWMutex
	providers  map[string]Provider
}

// NewProviderStore 创建 Provider 存储
func NewProviderStore(configDir string) (*ProviderStore, error) {
	configPath := filepath.Join(configDir, "providers.json")
	store := &ProviderStore{
		configPath: configPath,
		providers:  make(map[string]Provider),
	}

	// 确保目录存在
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}

	// 加载现有配置
	if err := store.load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// 首次运行，加载默认 Provider
		store.loadDefaults()
	}

	return store, nil
}

// loadDefaults 加载默认 Provider
func (s *ProviderStore) loadDefaults() {
	defaults := []Provider{
		{
			ID:         uuid.New().String(),
			Name:       "Cloudflare",
			Protocol:   ProtocolDoH,
			Endpoint:   "https://cloudflare-dns.com/dns-query",
			ServerName: "cloudflare-dns.com",
			Port:       443,
			Timeout:    5000,
			Enabled:    true,
			Tags:       []string{"default", "fast"},
		},
		{
			ID:         uuid.New().String(),
			Name:       "Google",
			Protocol:   ProtocolDoH,
			Endpoint:   "https://dns.google/dns-query",
			ServerName: "dns.google",
			Port:       443,
			Timeout:    5000,
			Enabled:    true,
			Tags:       []string{"default"},
		},
		{
			ID:         uuid.New().String(),
			Name:       "Quad9",
			Protocol:   ProtocolDoH,
			Endpoint:   "https://dns.quad9.net/dns-query",
			ServerName: "dns.quad9.net",
			Port:       443,
			Timeout:    5000,
			Enabled:    true,
			Tags:       []string{"default", "security"},
		},
		{
			ID:         uuid.New().String(),
			Name:       "AliDNS",
			Protocol:   ProtocolDNS,
			Endpoint:   "",
			ServerName: "dns.aliyun.com",
			Port:       53,
			Timeout:    5000,
			Enabled:    true,
			Tags:       []string{"default", "china"},
		},
	}

	for _, p := range defaults {
		s.providers[p.ID] = p
	}
}

// load 从文件加载配置
func (s *ProviderStore) load() error {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.providers)
}

// save 保存配置到文件
func (s *ProviderStore) save() error {
	data, err := json.MarshalIndent(s.providers, "", "  ")
	if err != nil {
		return err
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
		p.CreatedAt = time.Now()
	}
	p.UpdatedAt = time.Now()
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
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Protocol   Protocol  `json:"protocol"`
	Endpoint   string    `json:"endpoint"`
	ServerName string    `json:"server_name"`
	Port       int       `json:"port"`
	Timeout    int       `json:"timeout"`
	Enabled    bool      `json:"enabled"`
	Tags       []string  `json:"tags"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
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
		CreatedAt:  p.CreatedAt,
		UpdatedAt:  p.UpdatedAt,
	}
}
