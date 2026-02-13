package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"geoprism/backend/provider"
	"geoprism/backend/resolver"
)

// App 结构体
type App struct {
	ctx           context.Context
	providerStore *provider.ProviderStore
	resolver      *resolver.Resolver
}

// NewApp 创建新的 App
func NewApp() *App {
	return &App{
		resolver: resolver.NewResolver(),
	}
}

// getConfigDir 获取配置目录
func getConfigDir() string {
	// macOS: ~/Library/Application Support/GeoPrism
	home, _ := os.UserHomeDir()
	appSupport := filepath.Join(home, "Library", "Application Support", "GeoPrism")
	return filepath.Join(appSupport, "config")
}

// startup 应用启动时调用
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// 获取应用配置目录
	configDir := getConfigDir()

	// 初始化 Provider 存储
	store, err := provider.NewProviderStore(configDir)
	if err != nil {
		fmt.Printf("Failed to init provider store: %v\n", err)
		return
	}
	a.providerStore = store

	fmt.Printf("GeoPrism started, config dir: %s\n", configDir)
}

// ==================== Provider 管理接口 ====================

// ListProviders 获取所有 Provider
func (a *App) ListProviders() []provider.ProviderView {
	providers := a.providerStore.List()
	result := make([]provider.ProviderView, len(providers))
	for i, p := range providers {
		result[i] = p.ToView()
	}
	return result
}

// UpsertProvider 创建或更新 Provider
func (a *App) UpsertProvider(p provider.ProviderView) error {
	prov := provider.Provider{
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
	return a.providerStore.Upsert(prov)
}

// DeleteProvider 删除 Provider
func (a *App) DeleteProvider(id string) error {
	return a.providerStore.Delete(id)
}

// ProviderHealth Provider 健康状态
type ProviderHealth struct {
	ProviderID string `json:"provider_id"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	LatencyMs  int64  `json:"latency_ms"`
}

// TestProvider 测试 Provider 连通性
func (a *App) TestProvider(id string) (ProviderHealth, error) {
	p, ok := a.providerStore.Get(id)
	if !ok {
		return ProviderHealth{ProviderID: id}, fmt.Errorf("provider not found")
	}

	success, msg, latency := a.resolver.TestConnection(
		p.Endpoint,
		p.ServerName,
		p.Port,
		string(p.Protocol),
		p.Timeout,
	)

	return ProviderHealth{
		ProviderID: id,
		Success:    success,
		Message:    msg,
		LatencyMs:  latency,
	}, nil
}

// ==================== DNS 查询接口 ====================

// QueryRequest 查询请求
type QueryRequest struct {
	Domain      string   `json:"domain"`
	RecordType  string   `json:"record_type"`
	ProviderIDs []string `json:"provider_ids"`
	Timeout     int      `json:"timeout"`
	Retry       int      `json:"retry"`
}

// QueryAnswer 查询响应
type QueryAnswer struct {
	ProviderID string               `json:"provider_id"`
	Provider   string               `json:"provider_name"`
	RCode      int                  `json:"rcode"`
	RCodeName  string               `json:"rcode_name"`
	Answers    []resolver.DNSRecord `json:"answers"`
	TTL        uint32               `json:"ttl"`
	RTTMs      int64                `json:"rtt_ms"`
	Error      string               `json:"error,omitempty"`
	Success    bool                 `json:"success"`
}

// QueryResultView 查询结果视图
type QueryResultView struct {
	Domain     string        `json:"domain"`
	RecordType string        `json:"record_type"`
	Answers    []QueryAnswer `json:"answers"`
	TotalTime  int64         `json:"total_time_ms"`
}

// QueryDomain 执行域名查询
func (a *App) QueryDomain(req QueryRequest) (QueryResultView, error) {
	var providers []resolver.ProviderInfo

	if len(req.ProviderIDs) == 0 {
		// 查询所有启用的 Provider
		for _, p := range a.providerStore.GetEnabled() {
			providers = append(providers, resolver.ProviderInfo{
				ID:         p.ID,
				Endpoint:   p.Endpoint,
				ServerName: p.ServerName,
				Port:       p.Port,
				Protocol:   string(p.Protocol),
				Name:       p.Name,
			})
		}
	} else {
		for _, id := range req.ProviderIDs {
			if p, ok := a.providerStore.Get(id); ok {
				providers = append(providers, resolver.ProviderInfo{
					ID:         p.ID,
					Endpoint:   p.Endpoint,
					ServerName: p.ServerName,
					Port:       p.Port,
					Protocol:   string(p.Protocol),
					Name:       p.Name,
				})
			}
		}
	}

	if len(providers) == 0 {
		return QueryResultView{}, fmt.Errorf("no providers available")
	}

	// 执行查询，使用默认超时 5000ms
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 5000
	}

	result := a.resolver.QueryMulti(providers, resolver.DNSQuery{
		Domain:     req.Domain,
		RecordType: resolver.RecordType(req.RecordType),
		Timeout:    timeout,
	})

	// 转换为前端视图
	view := QueryResultView{
		Domain:     result.Domain,
		RecordType: string(result.RecordType),
		Answers:    make([]QueryAnswer, len(result.Answers)),
		TotalTime:  result.TotalTime,
	}

	// 创建 provider ID 到名称的映射
	providerNames := make(map[string]string)
	for _, p := range providers {
		providerNames[p.ID] = p.Name
	}

	for i, ans := range result.Answers {
		view.Answers[i] = QueryAnswer{
			ProviderID: ans.ProviderID,
			Provider:   providerNames[ans.ProviderID],
			RCode:      ans.RCode,
			RCodeName:  ans.RCodeName,
			Answers:    ans.Answers,
			TTL:        ans.TTL,
			RTTMs:      ans.RTTMs,
			Error:      ans.Error,
			Success:    ans.Success,
		}
	}

	return view, nil
}

// Greet 示例方法
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, It's show time!", name)
}
