package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"geoprism/backend/provider"
	"geoprism/backend/resolver"
)

// 验证 issue 2026-06-20-nondeterministic-result-order 测试矩阵第 8 条：
// 显式 -p c,a 顺序 → QueryDomain 输出的 Answers 顺序为 c,a，不被配置声明顺序覆盖。
// 通过 NewResolverWithQueryFunc 注入 mock resolver，避免真实网络。
func TestQueryDomainExplicitProviderOrderPreserved(t *testing.T) {
	// 用 ID 字典序与声明顺序相反的配置，确保显式 -p 顺序不是巧合。
	// 声明顺序：zeta, alpha, mid；ID 字典序：alpha, mid, zeta。
	configDir := t.TempDir()
	writeProviderConfigForOrderTest(t, configDir, `
[[providers]]
id = "zeta"
name = "Zeta"
protocol = "doh"
endpoint = "https://z.example/dns-query"
server_name = "z.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "alpha"
name = "Alpha"
protocol = "doh"
endpoint = "https://a.example/dns-query"
server_name = "a.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "mid"
name = "Mid"
protocol = "doh"
endpoint = "https://m.example/dns-query"
server_name = "m.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []
`)
	store, err := provider.NewProviderStore(configDir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	// 注入 mock resolver：返回成功响应即可。
	// 顺序保持由 QueryMulti 的 index 归位保证（矩阵第 1 条已验证逆序完成场景），
	// 此用例聚焦 CLI 层是否把 req.ProviderIDs 顺序忠实透传给 QueryMulti。
	mockResolver := resolver.NewResolverWithQueryFunc(func(endpoint, serverName string, port int, protocol string, query resolver.DNSQuery) (*resolver.DNSAnswer, error) {
		return &resolver.DNSAnswer{
			ProviderID: query.ProviderID,
			Success:    true,
			RCodeName:  "NOERROR",
		}, nil
	})

	app := &App{
		providerStore: store,
		resolver:      mockResolver,
	}
	defer app.Close()

	// 显式 -p mid,alpha,zeta（与配置声明顺序逆序）→ Answers 必须为 mid,alpha,zeta。
	view, err := app.QueryDomain(QueryRequest{
		Domain:      "example.com",
		RecordType:  "A",
		ProviderIDs: []string{"mid", "alpha", "zeta"},
	})
	if err != nil {
		t.Fatalf("QueryDomain() error = %v", err)
	}
	got := make([]string, len(view.Answers))
	for i, a := range view.Answers {
		got[i] = a.ProviderID
	}
	if want := []string{"mid", "alpha", "zeta"}; !equalStringSliceCLITest(got, want) {
		t.Fatalf("explicit ProviderIDs order not preserved: got %v, want %v", got, want)
	}

	// 再验证子集逆序：alpha,zeta（配置里 zeta 在 alpha 前）→ 应得到 alpha,zeta。
	view2, err := app.QueryDomain(QueryRequest{
		Domain:      "example.com",
		RecordType:  "A",
		ProviderIDs: []string{"alpha", "zeta"},
	})
	if err != nil {
		t.Fatalf("QueryDomain() second call error = %v", err)
	}
	got2 := make([]string, len(view2.Answers))
	for i, a := range view2.Answers {
		got2[i] = a.ProviderID
	}
	if want := []string{"alpha", "zeta"}; !equalStringSliceCLITest(got2, want) {
		t.Fatalf("explicit ProviderIDs order (subset) not preserved: got %v, want %v", got2, want)
	}
}

// equalStringSliceCLITest 比较两个字符串切片是否逐元素相等。
func equalStringSliceCLITest(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeProviderConfigForOrderTest 写入 providers.toml 内容到指定 configDir。
func writeProviderConfigForOrderTest(t *testing.T, configDir, content string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(configDir, "providers.toml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
