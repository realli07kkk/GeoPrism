package cli

import (
	"fmt"
	"net"
	"strings"
)

// ipToReverseName 将 IP 地址转换为反向查询域名
// IPv4: 8.8.8.8 → 8.8.8.8.in-addr.arpa
// IPv6: 2001:db8::1 → 1.0.0.0...8.b.d.0.1.0.0.2.ip6.arpa
func ipToReverseName(ip net.IP) string {
	// 统一处理为 4 字节表示（IPv4）
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", ip4[3], ip4[2], ip4[1], ip4[0])
	}

	// IPv6: 每个十六进制数字反转，用点分隔
	// 展开为完整 32 个十六进制数字
	hex := fmt.Sprintf("%032x", ip)
	var parts []string
	for i := len(hex) - 1; i >= 0; i-- {
		parts = append(parts, string(hex[i]))
	}
	return strings.Join(parts, ".") + ".ip6.arpa"
}

// parseIPAndValidate 解析并验证 IP 地址
// 返回 nil 表示不是有效的 IP
func parseIPAndValidate(s string) net.IP {
	return net.ParseIP(s)
}
