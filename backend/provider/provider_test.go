package provider

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewProviderStoreWritesDefaultTOML(t *testing.T) {
	dir := t.TempDir()

	store, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	for _, id := range []string{"cloudflare", "google", "quad9", "alidns"} {
		if _, ok := store.Get(id); !ok {
			t.Fatalf("default provider %q not found", id)
		}
	}

	configPath := filepath.Join(dir, providersConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[[providers]]") {
		t.Fatalf("default TOML should contain providers array")
	}
	if !strings.Contains(content, `id = "cloudflare"`) {
		t.Fatalf("default TOML should contain cloudflare provider")
	}
}

func TestNewProviderStoreLoadsValidTOML(t *testing.T) {
	dir := t.TempDir()
	writeProvidersFile(t, dir, `
[[providers]]
id = "alpha"
name = "Alpha"
protocol = "doh"
endpoint = "https://dns.alpha.example/dns-query"
server_name = "dns.alpha.example"
port = 443
timeout_ms = 2500
enabled = true
tags = ["team-a"]

[[providers]]
id = "beta"
name = "Beta"
protocol = "dns"
endpoint = ""
server_name = "1.1.1.1"
port = 53
timeout_ms = 3000
enabled = false
tags = []
`)

	store, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	if got := len(store.List()); got != 2 {
		t.Fatalf("len(List()) = %d, want 2", got)
	}
	if got := len(store.GetEnabled()); got != 1 {
		t.Fatalf("len(GetEnabled()) = %d, want 1", got)
	}

	alpha, ok := store.Get("alpha")
	if !ok {
		t.Fatalf("Get(alpha) should exist")
	}
	if alpha.Timeout != 2500 {
		t.Fatalf("alpha.Timeout = %d, want 2500", alpha.Timeout)
	}
	if alpha.Protocol != ProtocolDoH {
		t.Fatalf("alpha.Protocol = %q, want %q", alpha.Protocol, ProtocolDoH)
	}
}

func TestProviderStoreUpsertAndDeletePersistTOML(t *testing.T) {
	dir := t.TempDir()
	writeProvidersFile(t, dir, `
[[providers]]
id = "alpha"
name = "Alpha"
protocol = "doh"
endpoint = "https://dns.alpha.example/dns-query"
server_name = "dns.alpha.example"
port = 443
timeout_ms = 2500
enabled = true
tags = ["team-a"]
`)

	store, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	if err := store.Upsert(Provider{
		ID:         "custom",
		Name:       "Custom",
		Protocol:   ProtocolDoT,
		Endpoint:   "",
		ServerName: "dot.custom.example",
		Port:       853,
		Timeout:    4000,
		Enabled:    true,
		Tags:       []string{"lab"},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := store.Delete("alpha"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	reloaded, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("reload NewProviderStore() error = %v", err)
	}

	if _, ok := reloaded.Get("alpha"); ok {
		t.Fatalf("alpha should be deleted after reload")
	}
	custom, ok := reloaded.Get("custom")
	if !ok {
		t.Fatalf("custom should exist after reload")
	}
	if custom.Timeout != 4000 {
		t.Fatalf("custom.Timeout = %d, want 4000", custom.Timeout)
	}

	data, err := os.ReadFile(filepath.Join(dir, providersConfigFileName))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `id = "custom"`) {
		t.Fatalf("saved TOML should contain custom provider")
	}
}

func TestNewProviderStoreFailsOnInvalidConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "syntax error",
			content: `
[[providers]
id = "broken"
`,
			wantErr: "解析 Provider TOML 失败",
		},
		{
			name: "duplicate id",
			content: `
[[providers]]
id = "dup"
name = "Alpha"
protocol = "doh"
endpoint = "https://dns.alpha.example/dns-query"
server_name = "dns.alpha.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "dup"
name = "Beta"
protocol = "dns"
endpoint = ""
server_name = "8.8.8.8"
port = 53
timeout_ms = 1000
enabled = true
tags = []
`,
			wantErr: `id "dup" 重复`,
		},
		{
			name: "invalid protocol",
			content: `
[[providers]]
id = "bad"
name = "Bad"
protocol = "https"
endpoint = "https://dns.bad.example/dns-query"
server_name = "dns.bad.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []
`,
			wantErr: `protocol "https" 非法`,
		},
		{
			name: "empty name",
			content: `
[[providers]]
id = "bad"
name = ""
protocol = "dns"
endpoint = ""
server_name = "8.8.8.8"
port = 53
timeout_ms = 1000
enabled = true
tags = []
`,
			wantErr: "name 不能为空",
		},
		{
			name: "invalid port",
			content: `
[[providers]]
id = "bad"
name = "Bad"
protocol = "dns"
endpoint = ""
server_name = "8.8.8.8"
port = 0
timeout_ms = 1000
enabled = true
tags = []
`,
			wantErr: "port 必须大于 0",
		},
		{
			name: "invalid timeout",
			content: `
[[providers]]
id = "bad"
name = "Bad"
protocol = "dns"
endpoint = ""
server_name = "8.8.8.8"
port = 53
timeout_ms = 0
enabled = true
tags = []
`,
			wantErr: "timeout_ms 必须大于 0",
		},
		{
			name: "unknown field",
			content: `
[[providers]]
id = "bad"
name = "Bad"
protocol = "dns"
endpoint = ""
server_name = "8.8.8.8"
port = 53
timeout_ms = 1000
enabled = true
timeout = 1
tags = []
`,
			wantErr: "Provider 配置包含未知字段",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeProvidersFile(t, dir, tc.content)

			_, err := NewProviderStore(dir)
			if err == nil {
				t.Fatalf("NewProviderStore() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestNewProviderStoreIgnoresLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "providers.json")
	jsonContent := `{"legacy":{"name":"Legacy"}}`
	if err := os.WriteFile(jsonPath, []byte(jsonContent), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var store *ProviderStore
	stderr := captureStderr(t, func() {
		var err error
		store, err = NewProviderStore(dir)
		if err != nil {
			t.Fatalf("NewProviderStore() error = %v", err)
		}
	})

	if _, ok := store.Get("cloudflare"); !ok {
		t.Fatalf("default provider should be loaded from TOML")
	}
	gotJSON, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(gotJSON) != jsonContent {
		t.Fatalf("providers.json content changed, got %q", string(gotJSON))
	}
	if _, err := os.Stat(filepath.Join(dir, providersConfigFileName)); err != nil {
		t.Fatalf("providers.toml should exist, err = %v", err)
	}
	if !strings.Contains(stderr, "发现旧版 providers.json") {
		t.Fatalf("stderr should contain legacy json warning, got %q", stderr)
	}
}

func writeProvidersFile(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, providersConfigFileName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("reader.Close() error = %v", err)
	}
	return string(data)
}

// --- 顺序确定性测试（issue 2026-06-20-nondeterministic-result-order 测试矩阵 4-7 条）---

// providerIDs 提取 Provider 列表的 ID 序列，便于断言顺序而非整对象。
func providerIDs(ps []Provider) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}

// equalStringSlice 比较两个字符串切片是否逐元素相等。
func equalStringSlice(a, b []string) bool {
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

// 矩阵 4：load 后 List 顺序 == TOML 声明顺序（非 ID 字典序、非 map 随机序）。
func TestProviderStoreListPreservesTOMLDeclarationOrder(t *testing.T) {
	dir := t.TempDir()
	// 故意用 ID 字典序与声明顺序相反的配置，确保不是巧合。
	writeProvidersFile(t, dir, `
[[providers]]
id = "zeta"
name = "Zeta"
protocol = "doh"
endpoint = "https://dns.zeta.example/dns-query"
server_name = "dns.zeta.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "alpha"
name = "Alpha"
protocol = "doh"
endpoint = "https://dns.alpha.example/dns-query"
server_name = "dns.alpha.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "mid"
name = "Mid"
protocol = "doh"
endpoint = "https://dns.mid.example/dns-query"
server_name = "dns.mid.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []
`)

	store, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	got := providerIDs(store.List())
	want := []string{"zeta", "alpha", "mid"}
	if !equalStringSlice(got, want) {
		t.Fatalf("List() order = %v, want %v (TOML declaration order, not ID sorted)", got, want)
	}
}

// 矩阵 5：GetEnabled 过滤后保持剩余 Provider 的相对声明顺序。
func TestProviderStoreGetEnabledPreservesRelativeOrder(t *testing.T) {
	dir := t.TempDir()
	writeProvidersFile(t, dir, `
[[providers]]
id = "zeta"
name = "Zeta"
protocol = "doh"
endpoint = "https://dns.zeta.example/dns-query"
server_name = "dns.zeta.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "alpha"
name = "Alpha"
protocol = "doh"
endpoint = "https://dns.alpha.example/dns-query"
server_name = "dns.alpha.example"
port = 443
timeout_ms = 1000
enabled = false
tags = []

[[providers]]
id = "mid"
name = "Mid"
protocol = "doh"
endpoint = "https://dns.mid.example/dns-query"
server_name = "dns.mid.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []
`)

	store, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	got := providerIDs(store.GetEnabled())
	want := []string{"zeta", "mid"}
	if !equalStringSlice(got, want) {
		t.Fatalf("GetEnabled() order = %v, want %v (relative declaration order of enabled subset)", got, want)
	}
}

// 矩阵 6：Upsert 更新已有 Provider 不改变其位置。
func TestProviderStoreUpsertUpdateKeepsPosition(t *testing.T) {
	dir := t.TempDir()
	writeProvidersFile(t, dir, `
[[providers]]
id = "zeta"
name = "Zeta"
protocol = "doh"
endpoint = "https://dns.zeta.example/dns-query"
server_name = "dns.zeta.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "alpha"
name = "Alpha"
protocol = "doh"
endpoint = "https://dns.alpha.example/dns-query"
server_name = "dns.alpha.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "mid"
name = "Mid"
protocol = "doh"
endpoint = "https://dns.mid.example/dns-query"
server_name = "dns.mid.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []
`)

	store, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	// 更新中间的 alpha，改其 endpoint。
	if err := store.Upsert(Provider{
		ID:         "alpha",
		Name:       "Alpha-Updated",
		Protocol:   ProtocolDoH,
		Endpoint:   "https://dns.alpha.example/v2",
		ServerName: "dns.alpha.example",
		Port:       443,
		Timeout:    2000,
		Enabled:    true,
		Tags:       []string{"v2"},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got := providerIDs(store.List())
	want := []string{"zeta", "alpha", "mid"}
	if !equalStringSlice(got, want) {
		t.Fatalf("after Upsert(update), List() order = %v, want %v (alpha keeps original position)", got, want)
	}
}

// 矩阵 7：Upsert 新增 Provider 出现在末尾；Delete 后 save→reload 剩余顺序不变。
func TestProviderStoreUpsertNewAppendsAndDeletePreservesOrder(t *testing.T) {
	dir := t.TempDir()
	writeProvidersFile(t, dir, `
[[providers]]
id = "zeta"
name = "Zeta"
protocol = "doh"
endpoint = "https://dns.zeta.example/dns-query"
server_name = "dns.zeta.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []

[[providers]]
id = "alpha"
name = "Alpha"
protocol = "doh"
endpoint = "https://dns.alpha.example/dns-query"
server_name = "dns.alpha.example"
port = 443
timeout_ms = 1000
enabled = true
tags = []
`)

	store, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("NewProviderStore() error = %v", err)
	}

	// 新增 newone，应追加到末尾。
	if err := store.Upsert(Provider{
		ID:         "newone",
		Name:       "New One",
		Protocol:   ProtocolDoH,
		Endpoint:   "https://dns.newone.example/dns-query",
		ServerName: "dns.newone.example",
		Port:       443,
		Timeout:    1000,
		Enabled:    true,
		Tags:       []string{},
	}); err != nil {
		t.Fatalf("Upsert(new) error = %v", err)
	}
	got := providerIDs(store.List())
	want := []string{"zeta", "alpha", "newone"}
	if !equalStringSlice(got, want) {
		t.Fatalf("after Upsert(new), List() order = %v, want %v (new appended at end)", got, want)
	}

	// 删除中间的 alpha，剩余顺序应保持 zeta → newone。
	if err := store.Delete("alpha"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// reload 验证持久化顺序，确认 save 按 order 写回而非按 ID 排序。
	reloaded, err := NewProviderStore(dir)
	if err != nil {
		t.Fatalf("reload NewProviderStore() error = %v", err)
	}
	gotAfterReload := providerIDs(reloaded.List())
	wantAfterReload := []string{"zeta", "newone"}
	if !equalStringSlice(gotAfterReload, wantAfterReload) {
		t.Fatalf("after Delete + reload, List() order = %v, want %v (order preserved, not ID-sorted)", gotAfterReload, wantAfterReload)
	}
}

// 顺手发现不在本 issue 范围的覆盖（save 写回内容）已由现有 TestProviderStoreUpsertAndDeletePersistTOML 保证。
