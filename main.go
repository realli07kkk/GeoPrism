package main

import (
	"fmt"
	"os"
)

const usage = `GeoPrism - DNS 查询工具

用法:
  geoprism <command> [flags]
  geoprism <domain>              快捷查询，等价于 geoprism query <domain>

命令:
  query       查询域名 DNS 记录
  providers   列出所有 Provider
  test        测试 Provider 连通性
  help        显示帮助信息

示例:
  geoprism example.com
  geoprism query example.com -t AAAA
  geoprism query example.com -p cloudflare,google
  geoprism providers
  geoprism test --all
  geoprism test cloudflare`

// 已知子命令列表
var knownCommands = map[string]bool{
	"query":     true,
	"providers": true,
	"test":      true,
	"help":      true,
}

func main() {
	args := os.Args[1:]

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

	cmd := args[0]

	// 如果不是已知命令，当作域名进行快捷查询
	if !knownCommands[cmd] {
		app.runQuery(args)
		return
	}

	switch cmd {
	case "query":
		app.runQuery(args[1:])
	case "providers":
		app.runProviders()
	case "test":
		app.runTest(args[1:])
	case "help":
		fmt.Println(usage)
	}
}
