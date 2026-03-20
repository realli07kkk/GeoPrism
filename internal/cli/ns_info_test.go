package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"geoprism/backend/ipdb"
	"geoprism/backend/resolver"
)

// TestCLIQueryWithNSJSON 测试 query --ns -j 输出 JSON
func TestCLIQueryWithNSJSON(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"前导 -j", []string{"-j", "query", "cloudflare.com", "--ns"}},
		{"后置 -j", []string{"query", "cloudflare.com", "--ns", "-j"}},
		{"快捷查询", []string{"cloudflare.com", "--ns", "-j"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, exitCode := runCLI(tt.args...)
			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			// 验证是有效的 JSON 对象
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("output should be valid JSON: %v, got: %s", err, stdout)
			}

			// 验证 ns_info 字段存在
			nsInfo, ok := result["ns_info"]
			if !ok {
				t.Fatal("result should contain 'ns_info' field")
			}

			// 验证 ns_info 结构
			nsInfoMap, ok := nsInfo.(map[string]interface{})
			if !ok {
				t.Fatalf("ns_info should be an object, got: %T", nsInfo)
			}

			// 验证必需字段
			if _, ok := nsInfoMap["available"]; !ok {
				t.Error("ns_info should contain 'available' field")
			}
			if _, ok := nsInfoMap["query_time_ms"]; !ok {
				t.Error("ns_info should contain 'query_time_ms' field")
			}

			// 验证 available=true 时有 servers
			if available, _ := nsInfoMap["available"].(bool); available {
				servers, ok := nsInfoMap["servers"].([]interface{})
				if !ok {
					t.Fatal("ns_info.servers should be an array when available=true")
				}
				if len(servers) == 0 {
					t.Error("ns_info.servers should not be empty when available=true")
				}

				// 验证每个 server 的结构
				for i, s := range servers {
					server, ok := s.(map[string]interface{})
					if !ok {
						t.Fatalf("server[%d] should be an object", i)
					}
					if _, ok := server["name"]; !ok {
						t.Errorf("server[%d] should contain 'name' field", i)
					}
				}
			}
		})
	}
}

// TestCLIQueryWithNSText 测试 query --ns 文本输出
func TestCLIQueryWithNSText(t *testing.T) {
	stdout, _, exitCode := runCLI("query", "cloudflare.com", "--ns")
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	// 验证输出包含 NS 服务器信息标题
	if !strings.Contains(stdout, "NS 服务器信息") {
		t.Error("output should contain 'NS 服务器信息'")
	}

	// 验证输出包含 NS 查询耗时
	if !strings.Contains(stdout, "NS 查询耗时") {
		t.Error("output should contain 'NS 查询耗时'")
	}
}

// TestCLIQueryWithNSNXDOMAIN 测试 --ns 对 NXDOMAIN 的确定性错误聚合
func TestCLIQueryWithNSNXDOMAIN(t *testing.T) {
	// 使用一个不存在的域名
	stdout, _, _ := runCLI("-j", "query", "nonexistent-test-domain-xyz123.invalid", "--ns")

	// 可能返回非零退出码，也可能返回零（取决于 provider 行为）
	// 重要的是验证错误信息的确定性

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		// 如果不是 JSON，检查 stderr
		t.Fatalf("output should be valid JSON: %v", err)
	}

	nsInfo, ok := result["ns_info"].(map[string]interface{})
	if !ok {
		t.Fatal("result should contain 'ns_info' field")
	}

	// 验证 available=false
	if available, _ := nsInfo["available"].(bool); available {
		t.Error("ns_info.available should be false for NXDOMAIN")
	}

	// 验证有错误信息
	if errMsg, _ := nsInfo["error"].(string); errMsg == "" {
		t.Error("ns_info should contain 'error' field when available=false")
	}
}

// TestCLIQueryWithNSDeterministicOrder 测试 --ns 输出顺序稳定性
func TestCLIQueryWithNSDeterministicOrder(t *testing.T) {
	// 运行两次，验证 NS 服务器顺序稳定
	runAndCheckOrder := func() []string {
		stdout, _, exitCode := runCLI("-j", "query", "cloudflare.com", "--ns")
		if exitCode != 0 {
			t.Fatalf("exit code = %d", exitCode)
		}

		var result map[string]interface{}
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}

		nsInfo, _ := result["ns_info"].(map[string]interface{})
		servers, _ := nsInfo["servers"].([]interface{})

		var names []string
		for _, s := range servers {
			server, _ := s.(map[string]interface{})
			if name, ok := server["name"].(string); ok {
				names = append(names, name)
			}
		}
		return names
	}

	order1 := runAndCheckOrder()
	order2 := runAndCheckOrder()

	// 验证两次运行的 NS 服务器顺序相同
	if len(order1) != len(order2) {
		t.Fatalf("NS count differs: %d vs %d", len(order1), len(order2))
	}

	for i := range order1 {
		if order1[i] != order2[i] {
			t.Errorf("NS order differs at index %d: %q vs %q", i, order1[i], order2[i])
		}
	}

	// 验证 NS 名称是按字母顺序排列的
	for i := 1; i < len(order1); i++ {
		if order1[i] < order1[i-1] {
			t.Errorf("NS names not sorted: %q should come before %q", order1[i], order1[i-1])
		}
	}
}

// TestSelectNSQueryError 测试错误选择优先级
func TestSelectNSQueryError(t *testing.T) {
	tests := []struct {
		name    string
		answers []resolver.DNSAnswer
		wantErr string
	}{
		{
			name: "NXDOMAIN 优先级最高",
			answers: []resolver.DNSAnswer{
				{Success: false, RCodeName: "SERVFAIL"},
				{Success: false, RCodeName: "NXDOMAIN"},
				{Success: false, RCodeName: "REFUSED"},
			},
			wantErr: "域名不存在 (NXDOMAIN)",
		},
		{
			name: "SERVFAIL 次之",
			answers: []resolver.DNSAnswer{
				{Success: false, RCodeName: "REFUSED"},
				{Success: false, RCodeName: "SERVFAIL"},
				{Success: false, Error: "timeout"},
			},
			wantErr: "服务器错误 (SERVFAIL)",
		},
		{
			name: "REFUSED 再次",
			answers: []resolver.DNSAnswer{
				{Success: false, Error: "timeout"},
				{Success: false, RCodeName: "REFUSED"},
			},
			wantErr: "查询被拒绝 (REFUSED)",
		},
		{
			name: "其他错误按字母排序取第一个",
			answers: []resolver.DNSAnswer{
				{Success: false, Error: "timeout"},
				{Success: false, Error: "connection refused"},
			},
			wantErr: "connection refused",
		},
		{
			name: "无错误时返回默认消息",
			answers: []resolver.DNSAnswer{
				{Success: true, Answers: nil},
			},
			wantErr: "未找到 NS 记录",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectNSQueryError(tt.answers)
			if got != tt.wantErr {
				t.Errorf("selectNSQueryError() = %q, want %q", got, tt.wantErr)
			}
		})
	}
}

// TestSelectNSError 测试 NS IP 查询错误选择优先级
func TestSelectNSError(t *testing.T) {
	tests := []struct {
		name    string
		errors  []string
		wantErr string
	}{
		{
			name:    "空错误列表返回默认消息",
			errors:  []string{},
			wantErr: "未找到 IP 地址",
		},
		{
			name:    "NXDOMAIN 优先",
			errors:  []string{"SERVFAIL", "NXDOMAIN", "timeout"},
			wantErr: "域名不存在 (NXDOMAIN)",
		},
		{
			name:    "SERVFAIL 次之",
			errors:  []string{"timeout", "SERVFAIL"},
			wantErr: "服务器错误 (SERVFAIL)",
		},
		{
			name:    "REFUSED 再次",
			errors:  []string{"timeout", "REFUSED"},
			wantErr: "查询被拒绝 (REFUSED)",
		},
		{
			name:    "其他错误排序后取第一个",
			errors:  []string{"timeout", "connection reset"},
			wantErr: "connection reset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectNSError(tt.errors)
			if got != tt.wantErr {
				t.Errorf("selectNSError() = %q, want %q", got, tt.wantErr)
			}
		})
	}
}

// TestNSInfoViewInterface 测试 NSInfoView 接口实现
func TestNSInfoViewInterface(t *testing.T) {
	view := NSInfoView{
		Servers: []NSRecordView{
			{Name: "ns1.example.com", IPs: []NSIPInfo{{IP: "1.1.1.1"}}},
			{Name: "ns2.example.com", IPs: []NSIPInfo{{IP: "2.2.2.2"}}},
		},
		QueryTime: 100,
		Available: true,
	}

	if view.ServerCount() != 2 {
		t.Errorf("ServerCount() = %d, want 2", view.ServerCount())
	}

	if view.QueryTimeMs() != 100 {
		t.Errorf("QueryTimeMs() = %d, want 100", view.QueryTimeMs())
	}

	if !view.IsAvailable() {
		t.Error("IsAvailable() = false, want true")
	}

	if view.ServerAt(0).(NSRecordView).Name != "ns1.example.com" {
		t.Errorf("ServerAt(0).Name = %q", view.ServerAt(0).(NSRecordView).Name)
	}
}

// TestNSRecordViewInterface 测试 NSRecordView 接口实现
func TestNSRecordViewInterface(t *testing.T) {
	view := NSRecordView{
		Name: "ns1.example.com",
		IPs: []NSIPInfo{
			{IP: "1.1.1.1", RecordType: "A"},
			{IP: "2001:db8::1", RecordType: "AAAA"},
		},
	}

	if view.NameText() != "ns1.example.com" {
		t.Errorf("NameText() = %q", view.NameText())
	}

	if view.IPCount() != 2 {
		t.Errorf("IPCount() = %d, want 2", view.IPCount())
	}

	if view.HasError() {
		t.Error("HasError() = true, want false")
	}

	view.Error = "test error"
	if !view.HasError() {
		t.Error("HasError() = false, want true")
	}
	if view.ErrorText() != "test error" {
		t.Errorf("ErrorText() = %q", view.ErrorText())
	}
}

// TestNSIPInfoInterface 测试 NSIPInfo 接口实现
func TestNSIPInfoInterface(t *testing.T) {
	info := NSIPInfo{
		IP:         "1.1.1.1",
		RecordType: "A",
		Matched:    true,
		Record: ipdb.Record{
			Country: "United States",
			ASN:     "AS13335",
			ASName:  "Cloudflare, Inc.",
		},
	}

	if info.IPText() != "1.1.1.1" {
		t.Errorf("IPText() = %q", info.IPText())
	}

	if info.RecordTypeText() != "A" {
		t.Errorf("RecordTypeText() = %q", info.RecordTypeText())
	}

	if !info.MatchedState() {
		t.Error("MatchedState() = false, want true")
	}

	if info.CountryText() != "United States" {
		t.Errorf("CountryText() = %q", info.CountryText())
	}

	if info.ASNText() != "AS13335" {
		t.Errorf("ASNText() = %q", info.ASNText())
	}

	if info.ASNameText() != "Cloudflare, Inc." {
		t.Errorf("ASNameText() = %q", info.ASNameText())
	}
}
