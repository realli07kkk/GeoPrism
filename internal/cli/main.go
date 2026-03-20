package cli

import (
	"fmt"
	"net"
	"os"

	"geoprism/render"
)

const usage = `GeoPrism - DNS / IP 查询工具

用法:
  geoprism <command> [flags]
  geoprism <domain|ip>           快捷查询域名或直接匹配单个 IP

输出格式:
  -j, --json                     JSON 格式输出（支持 query/providers/test 和快捷 domain/ip 查询，不支持 ipdb）

命令:
  query       查询域名 DNS 记录
  ipdb        构建离线 IP 库
  providers   列出所有 Provider
  test        测试 Provider 连通性
  help        显示帮助信息

query 参数:
  -t, --type <type>              记录类型 (A/AAAA/CNAME/TXT/NS/MX/SOA/PTR)
  -x, --ptr                     反向 PTR 查询（IP → 域名）
  -p, --provider <names>         Provider 名称，逗号分隔
  --timeout <ms>                 超时毫秒（默认 5000）

示例:
  geoprism example.com
  geoprism example.com -j
  geoprism 1.1.1.1
  geoprism 1.1.1.1 -j
  geoprism query example.com -t AAAA
  geoprism query 8.8.8.8 -x      # 反向查询：8.8.8.8 → dns.google
  geoprism -x 8.8.8.8            # 快捷反向查询
  geoprism query example.com -p cloudflare,google
  geoprism ipdb build --csv /absolute/path/ipinfo_lite.csv
  geoprism providers
  geoprism test --all
  geoprism test cloudflare`

// 已知子命令列表
var knownCommands = map[string]bool{
	"ipdb":      true,
	"query":     true,
	"providers": true,
	"test":      true,
	"help":      true,
}

// 支持 JSON 输出的命令
var jsonSupportedCommands = map[string]bool{
	"query":     true,
	"providers": true,
	"test":      true,
}

// isGlobalFlag 判断是否是全局 flag
func isGlobalFlag(arg string) bool {
	return arg == "-j" || arg == "--json"
}

// isReverseFlag 判断是否是反向查询 flag
func isReverseFlag(arg string) bool {
	return arg == "-x" || arg == "--ptr"
}

// isIPLiteral 判断输入是否是合法 IP 字面量。
func isIPLiteral(arg string) bool {
	return net.ParseIP(arg) != nil
}

// Main 运行 CLI 入口。
func Main(args []string) {
	if len(args) == 0 {
		fmt.Println(usage)
		os.Exit(0)
	}

	// 初始化应用
	app, err := NewApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := app.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "关闭资源失败: %v\n", err)
		}
	}()

	// 先剥离前导的全局 flag（只处理开头的连续 flag）
	outputJSON := false
	i := 0
	for i < len(args) && isGlobalFlag(args[i]) {
		outputJSON = true
		i++
	}

	// 检查是否有反向查询 flag（任意位置）
	hasReverseFlag := false
	var cleanArgs []string
	for _, arg := range args[i:] {
		if isReverseFlag(arg) {
			hasReverseFlag = true
		} else {
			cleanArgs = append(cleanArgs, arg)
		}
	}

	// 检查前导 -j 是否被不支持 JSON 的命令使用
	if outputJSON && len(cleanArgs) > 0 {
		cmd := cleanArgs[0]
		if knownCommands[cmd] && !jsonSupportedCommands[cmd] {
			fmt.Fprintf(os.Stderr, "警告: %s 命令不支持 JSON 输出，-j 将被忽略\n", cmd)
			outputJSON = false
		}
	}

	if len(cleanArgs) == 0 {
		// 只有全局 flag，没有命令或域名
		fmt.Println(usage)
		os.Exit(0)
	}

	// 设置全局输出模式
	if outputJSON {
		app.SetOutputMode(render.OutputJSON)
	}

	cmd := cleanArgs[0]

	// 如果不是已知命令，按输入内容决定走域名查询还是 IP 查询
	if !knownCommands[cmd] {
		// 快捷反向查询：geoprism -x 8.8.8.8
		if hasReverseFlag {
			app.runQuery(append([]string{"-x"}, cleanArgs...))
			return
		}
		if isIPLiteral(cmd) {
			app.runIPLookup(cleanArgs)
		} else {
			app.runQuery(cleanArgs)
		}
		return
	}

	// 已知命令：传递剩余参数到子命令
	switch cmd {
	case "query":
		queryArgs := cleanArgs[1:]
		if hasReverseFlag {
			queryArgs = append([]string{"-x"}, queryArgs...)
		}
		app.runQuery(queryArgs)
	case "ipdb":
		app.runIPDB(cleanArgs[1:])
	case "providers":
		app.runProviders(cleanArgs[1:])
	case "test":
		app.runTest(cleanArgs[1:])
	case "help":
		fmt.Println(usage)
	}
}
