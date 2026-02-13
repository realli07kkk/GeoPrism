package resolver

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

// ProviderInfo Provider 信息
type ProviderInfo struct {
	ID         string
	Endpoint   string
	ServerName string
	Port       int
	Protocol   string // doh, dot, dns
	Name       string
}

// RecordType DNS 记录类型
type RecordType string

const (
	RecordTypeA     RecordType = "A"
	RecordTypeAAAA  RecordType = "AAAA"
	RecordTypeCNAME RecordType = "CNAME"
	RecordTypeTXT   RecordType = "TXT"
	RecordTypeNS    RecordType = "NS"
	RecordTypeMX    RecordType = "MX"
	RecordTypeSOA   RecordType = "SOA"
)

// DNSQuery DNS 查询请求
type DNSQuery struct {
	Domain     string
	RecordType RecordType
	ProviderID string
	Timeout    int // 超时时间，单位毫秒
}

// DNSAnswer DNS 查询响应
type DNSAnswer struct {
	ProviderID string      `json:"provider_id"`
	RCode      int         `json:"rcode"` // 0 表示成功
	RCodeName  string      `json:"rcode_name"`
	Answers    []DNSRecord `json:"answers"`
	TTL        uint32      `json:"ttl"`
	RTTMs      int64       `json:"rtt_ms"` // 往返时间毫秒
	Error      string      `json:"error,omitempty"`
	Success    bool        `json:"success"`
}

// DNSRecord 单条 DNS 记录
type DNSRecord struct {
	Type string `json:"type"`
	Name string `json:"name"`
	TTL  uint32 `json:"ttl"`
	Data string `json:"data"`
}

// QueryResult 查询结果
type QueryResult struct {
	Domain     string      `json:"domain"`
	RecordType RecordType  `json:"record_type"`
	Answers    []DNSAnswer `json:"answers"`
	TotalTime  int64       `json:"total_time_ms"` // 总查询时间毫秒
}

// Resolver DNS 解析器
type Resolver struct {
	httpClient *http.Client
}

// NewResolver 创建新的解析器
func NewResolver() *Resolver {
	return &Resolver{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				IdleConnTimeout:     30 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second,
			},
		},
	}
}

// getRecordTypeCode 将记录类型字符串转换为 DNS 类型码
func getRecordTypeCode(rt RecordType) uint16 {
	switch rt {
	case RecordTypeA:
		return dns.TypeA
	case RecordTypeAAAA:
		return dns.TypeAAAA
	case RecordTypeCNAME:
		return dns.TypeCNAME
	case RecordTypeTXT:
		return dns.TypeTXT
	case RecordTypeNS:
		return dns.TypeNS
	case RecordTypeMX:
		return dns.TypeMX
	case RecordTypeSOA:
		return dns.TypeSOA
	default:
		return dns.TypeA
	}
}

// getRCodeName 获取 RCode 名称
func getRCodeName(rcode int) string {
	switch rcode {
	case dns.RcodeSuccess:
		return "NOERROR"
	case dns.RcodeFormatError:
		return "FORMERR"
	case dns.RcodeServerFailure:
		return "SERVFAIL"
	case dns.RcodeNameError:
		return "NXDOMAIN"
	case dns.RcodeNotImplemented:
		return "NOTIMP"
	case dns.RcodeRefused:
		return "REFUSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", rcode)
	}
}

// parseAnswers 解析 DNS 答案
func parseAnswers(msg *dns.Msg) []DNSRecord {
	records := make([]DNSRecord, 0)
	for _, rr := range msg.Answer {
		records = append(records, DNSRecord{
			Type: dns.TypeToString[rr.Header().Rrtype],
			Name: rr.Header().Name,
			TTL:  rr.Header().Ttl,
			Data: stripTrailingDot(rr.String()),
		})
	}
	return records
}

// stripTrailingDot 去除末尾的点
func stripTrailingDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

// QueryDoH 使用 DoH 查询
func (r *Resolver) QueryDoH(endpoint, domain string, rt RecordType, timeout time.Duration) (*DNSAnswer, error) {
	// 构建 DNS 查询消息
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), getRecordTypeCode(rt))

	// 序列化 DNS 消息
	msgBytes, err := m.Pack()
	if err != nil {
		return nil, fmt.Errorf("failed to pack DNS message: %w", err)
	}

	// 创建 HTTP 请求
	base64URL := base64.RawURLEncoding.EncodeToString(msgBytes)
	url := fmt.Sprintf("%s?dns=%s", endpoint, base64URL)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-message")

	start := time.Now()
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	rttMs := time.Since(start).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return &DNSAnswer{
			RCode:     resp.StatusCode,
			RCodeName: fmt.Sprintf("HTTP %d", resp.StatusCode),
			Error:     string(body),
			Success:   false,
			RTTMs:     rttMs,
		}, nil
	}

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 解析 DNS 响应
	msg := new(dns.Msg)
	if err := msg.Unpack(body); err != nil {
		return nil, fmt.Errorf("failed to unpack DNS response: %w", err)
	}

	// 提取 TTL
	var ttl uint32
	if len(msg.Answer) > 0 {
		ttl = msg.Answer[0].Header().Ttl
	}

	return &DNSAnswer{
		RCode:     msg.Rcode,
		RCodeName: getRCodeName(msg.Rcode),
		Answers:   parseAnswers(msg),
		TTL:       ttl,
		RTTMs:     rttMs,
		Success:   msg.Rcode == dns.RcodeSuccess,
	}, nil
}

// QueryDoT 使用 DoT 查询
func (r *Resolver) QueryDoT(serverName, domain string, port int, rt RecordType, timeout time.Duration) (*DNSAnswer, error) {
	// 构建 DNS 查询消息
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), getRecordTypeCode(rt))

	// 序列化 DNS 消息
	msgBytes, err := m.Pack()
	if err != nil {
		return nil, fmt.Errorf("failed to pack DNS message: %w", err)
	}

	// 设置 TLS 连接
	address := fmt.Sprintf("%s:%d", serverName, port)
	conn, err := tls.Dial("tcp", address, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to DoT server: %w", err)
	}
	defer conn.Close()

	// 设置超时 deadline
	conn.SetDeadline(time.Now().Add(timeout))
	conn.SetReadDeadline(time.Now().Add(timeout))
	conn.SetWriteDeadline(time.Now().Add(timeout))

	// 发送 DNS 消息（使用 DNS over TLS 格式）
	start := time.Now()

	// DNS 消息长度前缀
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, uint16(len(msgBytes)))
	_, err = conn.Write(append(buf, msgBytes...))
	if err != nil {
		return nil, err
	}

	// 读取响应长度（使用 io.ReadFull 避免半包问题）
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		return nil, err
	}
	respLen := binary.BigEndian.Uint16(buf)

	// 读取响应内容
	respBytes := make([]byte, respLen)
	_, err = io.ReadFull(conn, respBytes)
	if err != nil {
		return nil, err
	}

	rttMs := time.Since(start).Milliseconds()

	// 解析 DNS 响应
	msg := new(dns.Msg)
	if err := msg.Unpack(respBytes); err != nil {
		return nil, fmt.Errorf("failed to unpack DNS response: %w", err)
	}

	// 提取 TTL
	var ttl uint32
	if len(msg.Answer) > 0 {
		ttl = msg.Answer[0].Header().Ttl
	}

	return &DNSAnswer{
		RCode:     msg.Rcode,
		RCodeName: getRCodeName(msg.Rcode),
		Answers:   parseAnswers(msg),
		TTL:       ttl,
		RTTMs:     rttMs,
		Success:   msg.Rcode == dns.RcodeSuccess,
	}, nil
}

// QueryDNS 使用原生 DNS (UDP) 查询
func (r *Resolver) QueryDNS(serverName string, port int, domain string, rt RecordType, timeout time.Duration) (*DNSAnswer, error) {
	// 构建 DNS 查询消息
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), getRecordTypeCode(rt))

	// 设置 UDP 客户端
	c := &dns.Client{
		Net:          "udp",
		Timeout:      timeout,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	}

	// 执行查询
	address := fmt.Sprintf("%s:%d", serverName, port)
	resp, rtt, err := c.Exchange(m, address)
	rttMs := rtt.Milliseconds()

	if err != nil {
		return &DNSAnswer{
			Success:   false,
			Error:     err.Error(),
			RCodeName: "ERROR",
			RTTMs:     rttMs,
		}, nil
	}

	// 提取 TTL
	var ttl uint32
	if len(resp.Answer) > 0 {
		ttl = resp.Answer[0].Header().Ttl
	}

	return &DNSAnswer{
		RCode:     resp.Rcode,
		RCodeName: getRCodeName(resp.Rcode),
		Answers:   parseAnswers(resp),
		TTL:       ttl,
		RTTMs:     rttMs,
		Success:   resp.Rcode == dns.RcodeSuccess,
	}, nil
}

// Query 执行 DNS 查询
func (r *Resolver) Query(endpoint, serverName string, port int, protocol string, query DNSQuery) (*DNSAnswer, error) {
	timeout := time.Duration(query.Timeout) * time.Millisecond
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	switch protocol {
	case "doh":
		return r.QueryDoH(endpoint, query.Domain, query.RecordType, timeout)
	case "dot":
		return r.QueryDoT(serverName, query.Domain, port, query.RecordType, timeout)
	case "dns":
		// DNS 使用 serverName 作为地址，port 作为端口
		return r.QueryDNS(serverName, port, query.Domain, query.RecordType, timeout)
	default:
		return r.QueryDoH(endpoint, query.Domain, query.RecordType, timeout)
	}
}

// QueryMulti 并行查询多个 Provider
func (r *Resolver) QueryMulti(providers []ProviderInfo, query DNSQuery) QueryResult {
	start := time.Now()
	result := QueryResult{
		Domain:     query.Domain,
		RecordType: query.RecordType,
		Answers:    make([]DNSAnswer, 0, len(providers)),
	}

	// 使用 goroutine 并行查询
	type resultChan struct {
		providerID string
		answer     *DNSAnswer
		err        error
	}

	ch := make(chan resultChan, len(providers))

	for _, p := range providers {
		go func(p ProviderInfo) {
			answer, err := r.Query(p.Endpoint, p.ServerName, p.Port, p.Protocol, DNSQuery{
				Domain:     query.Domain,
				RecordType: query.RecordType,
				ProviderID: p.ID,
				Timeout:    query.Timeout,
			})
			if err != nil {
				ch <- resultChan{providerID: p.ID, err: err}
			} else {
				answer.ProviderID = p.ID
				ch <- resultChan{providerID: p.ID, answer: answer}
			}
		}(p)
	}

	// 收集结果
	for i := 0; i < len(providers); i++ {
		res := <-ch
		if res.err != nil {
			result.Answers = append(result.Answers, DNSAnswer{
				ProviderID: res.providerID,
				Success:    false,
				Error:      res.err.Error(),
				RCodeName:  "ERROR",
			})
		} else if res.answer != nil {
			result.Answers = append(result.Answers, *res.answer)
		}
	}

	result.TotalTime = time.Since(start).Milliseconds()
	return result
}

// TestConnection 测试 Provider 连接
func (r *Resolver) TestConnection(endpoint, serverName string, port int, protocol string, timeout int) (bool, string, int64) {
	duration := time.Duration(timeout) * time.Millisecond
	if duration == 0 {
		duration = 5 * time.Second
	}

	start := time.Now()

	var err error
	var answer *DNSAnswer

	switch protocol {
	case "doh":
		answer, err = r.QueryDoH(endpoint, "google.com", RecordTypeA, duration)
	case "dot":
		answer, err = r.QueryDoT(serverName, "google.com", port, RecordTypeA, duration)
	case "dns":
		answer, err = r.QueryDNS(serverName, port, "google.com", RecordTypeA, duration)
	default:
		answer, err = r.QueryDoH(endpoint, "google.com", RecordTypeA, duration)
	}

	rttMs := time.Since(start).Milliseconds()

	if err != nil {
		return false, err.Error(), rttMs
	}

	if answer.Success {
		return true, "OK", rttMs
	}
	return false, answer.RCodeName, rttMs
}
