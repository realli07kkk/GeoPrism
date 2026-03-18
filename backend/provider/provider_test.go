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
