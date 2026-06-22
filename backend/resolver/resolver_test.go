package resolver

import (
	"errors"
	"testing"
	"time"
)

// --- 顺序确定性测试（issue 2026-06-20-nondeterministic-result-order 测试矩阵 1-3 条）---
//
// 通过注入可控的 queryFn 让 Provider 逆序 / 乱序完成，验证 Answers 仍按输入顺序归位，
// 而非"多跑几次看着没乱"。断言只比较 provider_id 序列，不比较 RTT / total_time（会变）。

func answerIDs(got []DNSAnswer) []string {
	ids := make([]string, len(got))
	for i, a := range got {
		ids[i] = a.ProviderID
	}
	return ids
}

func equalStr(a, b []string) bool {
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

// 矩阵 1：输入 A、B、C，完成顺序 C、B、A（逆序）→ 输出仍为 A、B、C。
func TestQueryMultiPreservesInputOrderUnderReverseCompletion(t *testing.T) {
	r := NewResolver()

	providers := []ProviderInfo{
		{ID: "A", Protocol: "doh", Endpoint: "a", Name: "PA"},
		{ID: "B", Protocol: "doh", Endpoint: "b", Name: "PB"},
		{ID: "C", Protocol: "doh", Endpoint: "c", Name: "PC"},
	}

	// queryFn 按 endpoint 决定延迟：A 最慢、C 最快，模拟逆序完成。
	r.queryFn = func(endpoint, serverName string, port int, protocol string, query DNSQuery) (*DNSAnswer, error) {
		var delay time.Duration
		switch endpoint {
		case "a":
			delay = 30 * time.Millisecond
		case "b":
			delay = 15 * time.Millisecond
		case "c":
			delay = 1 * time.Millisecond
		}
		time.Sleep(delay)
		return &DNSAnswer{
			ProviderID: query.ProviderID,
			Success:    true,
			RCodeName:  "NOERROR",
		}, nil
	}

	got := r.QueryMulti(providers, DNSQuery{Domain: "example.com", RecordType: RecordTypeA}).Answers
	want := []string{"A", "B", "C"}
	if ids := answerIDs(got); !equalStr(ids, want) {
		t.Fatalf("completion order C,B,A → Answers order = %v, want %v (input order)", ids, want)
	}
}

// 矩阵 2：中间 Provider 失败 → 错误结果仍落在原 index。
func TestQueryMultiErrorFallsAtOriginalIndex(t *testing.T) {
	r := NewResolver()

	providers := []ProviderInfo{
		{ID: "A", Protocol: "doh", Endpoint: "a", Name: "PA"},
		{ID: "B", Protocol: "doh", Endpoint: "b", Name: "PB"},
		{ID: "C", Protocol: "doh", Endpoint: "c", Name: "PC"},
	}

	// B 失败且最快（验证错误结果不被挪到末尾，也不因先完成而错位）。
	r.queryFn = func(endpoint, serverName string, port int, protocol string, query DNSQuery) (*DNSAnswer, error) {
		if endpoint == "b" {
			time.Sleep(1 * time.Millisecond)
			return nil, errors.New("network timeout")
		}
		time.Sleep(20 * time.Millisecond)
		return &DNSAnswer{ProviderID: query.ProviderID, Success: true, RCodeName: "NOERROR"}, nil
	}

	got := r.QueryMulti(providers, DNSQuery{Domain: "example.com", RecordType: RecordTypeA}).Answers
	if ids := answerIDs(got); !equalStr(ids, []string{"A", "B", "C"}) {
		t.Fatalf("error Answers order = %v, want [A B C]", ids)
	}
	if got[1].Error == "" || got[1].Success {
		t.Fatalf("Answers[1] should be error placeholder, got Success=%v Error=%q", got[1].Success, got[1].Error)
	}
	if got[1].ProviderID != "B" {
		t.Fatalf("Answers[1].ProviderID = %q, want B (original index preserved)", got[1].ProviderID)
	}
}

// 矩阵 3：空响应（answer == nil 且 err == nil）→ 仍落在原 index（按实现约束补占位错误结果）。
func TestQueryMultiNilAnswerFallsAtOriginalIndex(t *testing.T) {
	r := NewResolver()

	providers := []ProviderInfo{
		{ID: "A", Protocol: "doh", Endpoint: "a", Name: "PA"},
		{ID: "B", Protocol: "doh", Endpoint: "b", Name: "PB"},
	}

	// A 返回空响应（nil, nil）且最快。
	r.queryFn = func(endpoint, serverName string, port int, protocol string, query DNSQuery) (*DNSAnswer, error) {
		if endpoint == "a" {
			time.Sleep(1 * time.Millisecond)
			return nil, nil
		}
		time.Sleep(20 * time.Millisecond)
		return &DNSAnswer{ProviderID: query.ProviderID, Success: true, RCodeName: "NOERROR"}, nil
	}

	got := r.QueryMulti(providers, DNSQuery{Domain: "example.com", RecordType: RecordTypeA}).Answers
	if ids := answerIDs(got); !equalStr(ids, []string{"A", "B"}) {
		t.Fatalf("nil-answer Answers order = %v, want [A B]", ids)
	}
	if got[0].Success {
		t.Fatalf("Answers[0] should be error placeholder for nil response, got Success=true")
	}
	if got[0].ProviderID != "A" {
		t.Fatalf("Answers[0].ProviderID = %q, want A (original index preserved)", got[0].ProviderID)
	}
}

// 额外：并发压力下顺序仍稳定（防止 race detector 下出问题，且确认无 goroutine 写冲突）。
func TestQueryMultiOrderStableUnderConcurrency(t *testing.T) {
	r := NewResolver()

	const n = 8
	providers := make([]ProviderInfo, n)
	for i := 0; i < n; i++ {
		id := string(rune('A' + i))
		providers[i] = ProviderInfo{ID: id, Protocol: "doh", Endpoint: id, Name: "P" + id}
	}

	r.queryFn = func(endpoint, serverName string, port int, protocol string, query DNSQuery) (*DNSAnswer, error) {
		// 随机抖动完成顺序。
		time.Sleep(time.Duration((int(query.ProviderID[0]) % 5)) * time.Millisecond)
		return &DNSAnswer{ProviderID: query.ProviderID, Success: true, RCodeName: "NOERROR"}, nil
	}

	got := r.QueryMulti(providers, DNSQuery{Domain: "example.com", RecordType: RecordTypeA}).Answers
	want := make([]string, n)
	for i := 0; i < n; i++ {
		want[i] = string(rune('A' + i))
	}
	if ids := answerIDs(got); !equalStr(ids, want) {
		t.Fatalf("concurrent Answers order = %v, want %v", ids, want)
	}
}

// normalizeAnswer helper 单测：成功 / 失败 / 空响应三分支独立覆盖（无需驱动网络）。
func TestNormalizeAnswer(t *testing.T) {
	// 成功：返回原 answer，ProviderID 透传。
	a := &DNSAnswer{ProviderID: "x", Success: true, RCodeName: "NOERROR", RTTMs: 5}
	got := normalizeAnswer("x", a, nil)
	if !got.Success || got.ProviderID != "x" || got.RTTMs != 5 {
		t.Fatalf("success branch: got %+v, want %+v", got, *a)
	}

	// 失败：占位错误结果。
	got = normalizeAnswer("y", nil, errors.New("boom"))
	if got.Success || got.Error != "boom" || got.RCodeName != "ERROR" || got.ProviderID != "y" {
		t.Fatalf("error branch: got %+v", got)
	}

	// 空响应：占位错误结果，原 index 保留。
	got = normalizeAnswer("z", nil, nil)
	if got.Success || got.ProviderID != "z" || got.RCodeName != "ERROR" {
		t.Fatalf("nil-answer branch: got %+v", got)
	}
}
