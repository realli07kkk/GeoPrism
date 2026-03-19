package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"geoprism/render"
)

// 测试二进制文件路径
var testBinary string

// TestMain 编译测试二进制文件
func TestMain(m *testing.M) {
	// 编译测试二进制文件
	tmpDir, err := os.MkdirTemp("", "geoprism-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmpDir)

	testBinary = filepath.Join(tmpDir, "geoprism")
	cmd := exec.Command("go", "build", "-o", testBinary, ".")
	if err := cmd.Run(); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

// runCLI 执行测试二进制文件并返回 stdout, stderr, exitCode
func runCLI(args ...string) (stdout, stderr string, exitCode int) {
	home, err := os.MkdirTemp("", "geoprism-cli-home-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)

	return runCLIWithHome(home, args...)
}

// runCLIWithHome 在指定 HOME 下执行测试二进制文件。
func runCLIWithHome(home string, args ...string) (stdout, stderr string, exitCode int) {
	cmd := exec.Command(testBinary, args...)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.Env = append(os.Environ(), "HOME="+home)

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	return stdoutBuf.String(), stderrBuf.String(), exitCode
}

func buildCLIIPDB(t *testing.T, home string) {
	t.Helper()

	rootDir := filepath.Join(home, ".geoprism", "ipdb")
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	buildTestIPDB(t, rootDir)
}

// TestCLIHelp 测试帮助输出
func TestCLIHelp(t *testing.T) {
	stdout, _, exitCode := runCLI("help")
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(stdout, "GeoPrism") {
		t.Errorf("help output missing 'GeoPrism'")
	}
	if !strings.Contains(stdout, "-j, --json") {
		t.Errorf("help output missing '-j, --json'")
	}
}

// TestCLIProvidersJSON 测试 providers -j 输出 JSON
func TestCLIProvidersJSON(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"前导 -j", []string{"-j", "providers"}},
		{"后置 -j", []string{"providers", "-j"}},
		{"前导 --json", []string{"--json", "providers"}},
		{"后置 --json", []string{"providers", "--json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, exitCode := runCLI(tt.args...)
			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			// 验证是有效的 JSON 数组
			var providers []map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &providers); err != nil {
				t.Fatalf("output should be valid JSON array: %v, got: %s", err, stdout)
			}
			if len(providers) == 0 {
				t.Error("providers should not be empty")
			}
		})
	}
}

// TestCLIQueryJSON 测试 query -j 输出 JSON
func TestCLIQueryJSON(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"前导 -j", []string{"-j", "query", "example.com"}},
		{"后置 -j", []string{"query", "example.com", "-j"}},
		{"快捷查询 前导 -j", []string{"-j", "example.com"}},
		{"快捷查询 后置 -j", []string{"example.com", "-j"}},
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
			if result["domain"] != "example.com" {
				t.Errorf("domain = %v, want 'example.com'", result["domain"])
			}
		})
	}
}

func TestCLIIPLookupWithoutIPDB(t *testing.T) {
	_, stderr, exitCode := runCLI("1.1.1.1")
	if exitCode == 0 {
		t.Fatal("should exit with non-zero code when IP DB is missing")
	}
	if !strings.Contains(stderr, missingIPDBErrorMessage) {
		t.Fatalf("stderr should contain missing DB message, got: %s", stderr)
	}
}

func TestCLIIPLookupWithoutIPDBJSON(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"前导 -j", []string{"-j", "1.1.1.1"}},
		{"后置 -j", []string{"1.1.1.1", "-j"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, exitCode := runCLI(tt.args...)
			if exitCode == 0 {
				t.Fatal("should exit with non-zero code when IP DB is missing")
			}

			var result map[string]string
			stderr = strings.TrimSpace(stderr)
			if err := json.Unmarshal([]byte(stderr), &result); err != nil {
				t.Fatalf("stderr should be valid JSON: %v, got: %s", err, stderr)
			}
			if result["error"] != missingIPDBErrorMessage {
				t.Fatalf("error = %q, want %q", result["error"], missingIPDBErrorMessage)
			}
		})
	}
}

func TestCLIIPLookupJSON(t *testing.T) {
	home := t.TempDir()
	buildCLIIPDB(t, home)

	tests := []struct {
		name   string
		args   []string
		wantIP string
	}{
		{"前导 -j", []string{"-j", "1.0.0.1"}, "1.0.0.1"},
		{"后置 -j", []string{"1.0.0.1", "-j"}, "1.0.0.1"},
		{"IPv6", []string{"2001:db8::1", "--json"}, "2001:db8::1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, exitCode := runCLIWithHome(home, tt.args...)
			if exitCode != 0 {
				t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr)
			}

			var result map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("output should be valid JSON: %v, got: %s", err, stdout)
			}
			if result["ip"] != tt.wantIP {
				t.Fatalf("ip = %v, want %q", result["ip"], tt.wantIP)
			}
			if _, ok := result["matched"]; !ok {
				t.Fatal("JSON should contain matched field")
			}
			if _, ok := result["network"]; !ok {
				t.Fatal("JSON should contain network field")
			}
			if _, ok := result["record"]; ok {
				t.Fatal("JSON should not contain nested record field")
			}
		})
	}
}

func TestCLIIPLookupText(t *testing.T) {
	home := t.TempDir()
	buildCLIIPDB(t, home)

	stdout, stderr, exitCode := runCLIWithHome(home, "1.0.0.1")
	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", exitCode, stderr)
	}

	for _, token := range []string{"IP 查询结果", "1.0.0.1", "HIT", "1.0.0.0/24"} {
		if !strings.Contains(stdout, token) {
			t.Fatalf("stdout missing token %q:\n%s", token, stdout)
		}
	}
}

func TestCLIIPLookupFlagErrorJSON(t *testing.T) {
	_, stderr, exitCode := runCLI("1.0.0.1", "-j", "-bogus")
	if exitCode == 0 {
		t.Fatal("should exit with non-zero code for unknown flag")
	}

	var result map[string]string
	stderr = strings.TrimSpace(stderr)
	if err := json.Unmarshal([]byte(stderr), &result); err != nil {
		t.Fatalf("stderr should be valid JSON: %v, got: %s", err, stderr)
	}
	if _, ok := result["error"]; !ok {
		t.Fatal("JSON should contain error field")
	}
}

// TestCLITestJSON 测试 test --all -j 输出 JSON
func TestCLITestJSON(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"前导 -j", []string{"-j", "test", "--all"}},
		{"后置 -j", []string{"test", "--all", "-j"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, exitCode := runCLI(tt.args...)
			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			// 验证是有效的 JSON 数组
			var results []map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &results); err != nil {
				t.Fatalf("output should be valid JSON array: %v, got: %s", err, stdout)
			}
			if len(results) == 0 {
				t.Error("test results should not be empty")
			}
		})
	}
}

// TestCLIIPDBJSONWarning 测试 ipdb 对 -j 的警告
func TestCLIIPDBJSONWarning(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantWarning string
	}{
		{"前导 -j", []string{"-j", "ipdb", "build"}, "警告: ipdb 命令不支持 JSON 输出"},
		{"后置 -j", []string{"ipdb", "build", "-j"}, "警告: ipdb 命令不支持 JSON 输出"},
		{"后置 --json", []string{"ipdb", "build", "--json"}, "警告: ipdb 命令不支持 JSON 输出"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, _ := runCLI(tt.args...)
			if !strings.Contains(stderr, tt.wantWarning) {
				t.Errorf("stderr should contain %q, got: %s", tt.wantWarning, stderr)
			}
		})
	}
}

// TestCLIQueryErrorJSON 测试 query 错误时 JSON 输出
func TestCLIQueryErrorJSON(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"前导 -j 缺域名", []string{"-j", "query"}},
		{"后置 -j 缺域名", []string{"query", "-j"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, exitCode := runCLI(tt.args...)
			if exitCode == 0 {
				t.Error("should exit with non-zero code")
			}

			// 验证 stderr 是有效的 JSON
			var result map[string]string
			stderr = strings.TrimSpace(stderr)
			if err := json.Unmarshal([]byte(stderr), &result); err != nil {
				t.Fatalf("stderr should be valid JSON: %v, got: %s", err, stderr)
			}
			if _, ok := result["error"]; !ok {
				t.Error("JSON should contain 'error' field")
			}
		})
	}
}

// TestCLIQueryFlagNotSwallowed 测试 -p 参数值不会被 -j 吞掉
func TestCLIQueryFlagNotSwallowed(t *testing.T) {
	// 使用 --json 作为 Provider 名称（不存在的 Provider）
	_, stderr, exitCode := runCLI("query", "example.com", "-p", "--json")

	if exitCode == 0 {
		t.Error("should exit with non-zero code (provider not found)")
	}

	// 应该报错"未找到匹配的 Provider: --json"，而不是 JSON 格式的错误
	if !strings.Contains(stderr, "未找到匹配的 Provider") {
		t.Errorf("stderr should contain provider error, got: %s", stderr)
	}
}

// TestCLICommandRouting 测试命令路由不被前导 -j 破坏
func TestCLICommandRouting(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantJSON   bool
		wantInJSON string
	}{
		{"-j providers", []string{"-j", "providers"}, true, `"name"`}, // providers 列表使用 name 字段
		{"-j query example.com", []string{"-j", "query", "example.com"}, true, `"domain"`},
		{"-j test --all", []string{"-j", "test", "--all"}, true, `"provider_name"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, exitCode := runCLI(tt.args...)
			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			if tt.wantJSON {
				if !json.Valid([]byte(stdout)) {
					t.Errorf("output should be valid JSON, got: %s", stdout)
				}
				if !strings.Contains(stdout, tt.wantInJSON) {
					t.Errorf("JSON should contain %q", tt.wantInJSON)
				}
			}
		})
	}
}

// TestProviderTestResultStatus 测试 FAIL/ERROR/OK 三态语义
func TestProviderTestResultStatus(t *testing.T) {
	tests := []struct {
		name         string
		result       providerTestResult
		wantStatus   string
		wantSuccess  bool
		wantHasError bool
	}{
		{
			name:         "OK - 成功",
			result:       providerTestResult{Name: "Test", Success: true, LatencyMs: 100, Message: "OK", hasExecError: false},
			wantStatus:   "OK",
			wantSuccess:  true,
			wantHasError: false,
		},
		{
			name:         "FAIL - 探测失败",
			result:       providerTestResult{Name: "Test", Success: false, LatencyMs: 100, Message: "SERVFAIL", hasExecError: false},
			wantStatus:   "FAIL",
			wantSuccess:  false,
			wantHasError: false,
		},
		{
			name:         "ERROR - 执行错误",
			result:       providerTestResult{Name: "Test", Success: false, LatencyMs: 0, Message: "provider not found", hasExecError: true},
			wantStatus:   "ERROR",
			wantSuccess:  false,
			wantHasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.StatusText(); got != tt.wantStatus {
				t.Errorf("StatusText() = %q, want %q", got, tt.wantStatus)
			}
			if got := tt.result.Success; got != tt.wantSuccess {
				t.Errorf("Success = %v, want %v", got, tt.wantSuccess)
			}
			if got := tt.result.hasExecError; got != tt.wantHasError {
				t.Errorf("hasExecError = %v, want %v", got, tt.wantHasError)
			}
		})
	}
}

// TestProviderTestResultJSON 测试 JSON 序列化结构
func TestProviderTestResultJSON(t *testing.T) {
	tests := []struct {
		name   string
		result providerTestResult
	}{
		{
			name:   "成功结果",
			result: providerTestResult{Name: "Cloudflare", Success: true, LatencyMs: 100, Message: "OK"},
		},
		{
			name:   "失败结果",
			result: providerTestResult{Name: "Test", Success: false, LatencyMs: 50, Message: "SERVFAIL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := render.WriteJSON(&buf, tt.result); err != nil {
				t.Fatalf("WriteJSON error: %v", err)
			}

			// 验证 JSON 结构
			var parsed map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
				t.Fatalf("JSON parse error: %v", err)
			}

			// 验证必需字段存在
			requiredFields := []string{"provider_name", "success", "latency_ms", "message"}
			for _, field := range requiredFields {
				if _, ok := parsed[field]; !ok {
					t.Errorf("JSON missing required field: %s", field)
				}
			}

			// 验证 hasExecError 不会被序列化
			if _, ok := parsed["hasExecError"]; ok {
				t.Errorf("JSON should not contain hasExecError field")
			}
		})
	}
}

// TestNewProviderTestResult 测试构造函数
func TestNewProviderTestResult(t *testing.T) {
	t.Run("无错误时填充 health", func(t *testing.T) {
		health := ProviderHealth{
			ProviderID: "test",
			Success:    true,
			Message:    "OK",
			LatencyMs:  100,
		}
		result := newProviderTestResult("TestProvider", health, nil)

		if result.Name != "TestProvider" {
			t.Errorf("Name = %q, want %q", result.Name, "TestProvider")
		}
		if !result.Success {
			t.Errorf("Success = %v, want true", result.Success)
		}
		if result.LatencyMs != 100 {
			t.Errorf("LatencyMs = %d, want 100", result.LatencyMs)
		}
		if result.Message != "OK" {
			t.Errorf("Message = %q, want %q", result.Message, "OK")
		}
		if result.hasExecError {
			t.Errorf("hasExecError = %v, want false", result.hasExecError)
		}
	})

	t.Run("有错误时设置 hasExecError", func(t *testing.T) {
		health := ProviderHealth{}
		result := newProviderTestResult("TestProvider", health, &testError{msg: "provider not found"})

		if result.Success {
			t.Errorf("Success = %v, want false", result.Success)
		}
		if !result.hasExecError {
			t.Errorf("hasExecError = %v, want true", result.hasExecError)
		}
		if result.Message != "provider not found" {
			t.Errorf("Message = %q, want %q", result.Message, "provider not found")
		}
		if result.StatusText() != "ERROR" {
			t.Errorf("StatusText() = %q, want %q", result.StatusText(), "ERROR")
		}
	})
}

// testError 是一个简单的 error 实现，用于测试
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

// TestWriteError 测试统一错误输出
func TestWriteError(t *testing.T) {
	t.Run("文本模式 - 输出到 stderr", func(t *testing.T) {
		// 保存原始 stderr
		originalStderr := os.Stderr
		defer func() { os.Stderr = originalStderr }()

		// 创建管道捕获 stderr
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stderr = w

		app := &App{outputMode: render.OutputText}
		app.writeError("测试错误消息")

		w.Close()

		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		if !strings.Contains(output, "错误:") {
			t.Errorf("output should contain '错误:', got: %q", output)
		}
		if !strings.Contains(output, "测试错误消息") {
			t.Errorf("output should contain error message, got: %q", output)
		}
	})

	t.Run("JSON 模式 - 输出 JSON 到 stderr", func(t *testing.T) {
		// 保存原始 stderr
		originalStderr := os.Stderr
		defer func() { os.Stderr = originalStderr }()

		// 创建管道捕获 stderr
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stderr = w

		app := &App{outputMode: render.OutputJSON}
		app.writeError("测试 JSON 错误")

		w.Close()

		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := strings.TrimSpace(buf.String())

		// 验证是有效的 JSON
		var parsed map[string]string
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatalf("output should be valid JSON, got: %q, error: %v", output, err)
		}
		if parsed["error"] != "测试 JSON 错误" {
			t.Errorf("JSON error field = %q, want %q", parsed["error"], "测试 JSON 错误")
		}
	})

	t.Run("nil App 不 panic", func(t *testing.T) {
		var app *App
		// 不应该 panic
		app.writeError("test")
	})
}

// TestWriteJSON 测试 JSON 输出函数
func TestWriteJSON(t *testing.T) {
	t.Run("不转义 HTML", func(t *testing.T) {
		data := map[string]string{"url": "https://example.com?a=1&b=2"}
		var buf bytes.Buffer
		if err := render.WriteJSON(&buf, data); err != nil {
			t.Fatalf("WriteJSON error: %v", err)
		}

		output := buf.String()
		// & 不应该被转义成 \u0026
		if strings.Contains(output, `\u0026`) {
			t.Errorf("HTML characters should not be escaped: %s", output)
		}
		if !strings.Contains(output, "&") {
			t.Errorf("output should contain unescaped &: %s", output)
		}
	})

	t.Run("紧凑输出", func(t *testing.T) {
		data := map[string]string{"a": "1", "b": "2"}
		var buf bytes.Buffer
		if err := render.WriteJSON(&buf, data); err != nil {
			t.Fatalf("WriteJSON error: %v", err)
		}

		output := strings.TrimSpace(buf.String())
		// 紧凑 JSON 不应该有缩进
		if strings.Contains(output, "  ") {
			t.Errorf("JSON should be compact (no indentation): %s", output)
		}
	})
}
