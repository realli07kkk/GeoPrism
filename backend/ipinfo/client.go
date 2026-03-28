package ipinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"geoprism/backend/ipdb"
)

// Response 对应 ipinfo Lite API 的 JSON 响应。
type Response struct {
	IP            string `json:"ip"`
	Country       string `json:"country"`
	CountryCode   string `json:"country_code"`
	Continent     string `json:"continent"`
	ContinentCode string `json:"continent_code"`
	ASN           string `json:"asn"`
	ASName        string `json:"as_name"`
	ASDomain      string `json:"as_domain"`
}

// ToRecord 将 ipinfo 响应转换为 ipdb Record。
// Network 字段根据 IP 类型设置为 /32 或 /128。
func (r *Response) ToRecord() ipdb.Record {
	prefixLen := 32
	if isIPv6(r.IP) {
		prefixLen = 128
	}
	return ipdb.Record{
		Network:       fmt.Sprintf("%s/%d", r.IP, prefixLen),
		Country:       r.Country,
		CountryCode:   r.CountryCode,
		Continent:     r.Continent,
		ContinentCode: r.ContinentCode,
		ASN:           r.ASN,
		ASName:        r.ASName,
		ASDomain:      r.ASDomain,
	}
}

func isIPv6(ip string) bool {
	for _, c := range ip {
		if c == ':' {
			return true
		}
	}
	return false
}

// Client 是 ipinfo Lite API 的 HTTP 客户端。
type Client struct {
	token      string
	httpClient *http.Client
}

// NewClient 创建 ipinfo 客户端。
func NewClient(token string) *Client {
	return &Client{
		token: token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// LookupIP 查询指定 IP 的信息。
func (c *Client) LookupIP(ctx context.Context, ip string) (*Response, error) {
	url := fmt.Sprintf("https://api.ipinfo.io/lite/%s?token=%s", ip, c.token)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 ipinfo 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ipinfo 返回状态码 %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析 ipinfo 响应失败: %w", err)
	}

	return &result, nil
}
