package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"geoprism/backend/ipdb"
)

const ipdbUsage = `GeoPrism IP 库工具

用法:
  geoprism ipdb build --csv /absolute/path/ipinfo_lite.csv
`

// runIPDB 执行离线 IP 库相关命令。
func (a *App) runIPDB(args []string) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, ipdbUsage)
		os.Exit(1)
	}

	switch args[0] {
	case "build":
		a.runIPDBBuild(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "错误: 未知 ipdb 子命令: %s\n", args[0])
		fmt.Fprint(os.Stderr, ipdbUsage)
		os.Exit(1)
	}
}

// runIPDBBuild 构建离线 IP 库。
func (a *App) runIPDBBuild(args []string) {
	fs := flag.NewFlagSet("ipdb build", flag.ExitOnError)
	csvPath := fs.String("csv", "", "离线 IP CSV 文件绝对路径")
	fs.Parse(args)

	if *csvPath == "" {
		fmt.Fprintln(os.Stderr, "错误: 请通过 --csv 指定 CSV 文件路径")
		os.Exit(1)
	}
	if !filepath.IsAbs(*csvPath) {
		fmt.Fprintf(os.Stderr, "错误: --csv 必须是绝对路径: %s\n", *csvPath)
		os.Exit(1)
	}

	ipdbDir, err := getIPDBDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "构建离线 IP 库失败: %v\n", err)
		os.Exit(1)
	}

	meta, err := ipdb.BuildFromCSV(ipdbDir, ipdb.BuildOptions{
		CSVPath: *csvPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "构建离线 IP 库失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("离线 IP 库构建完成\n")
	fmt.Printf("数据目录: %s\n", ipdbDir)
	fmt.Printf("源文件: %s\n", meta.SourceCSV)
	fmt.Printf("构建时间: %s\n", meta.BuiltAt.Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("总记录数: %d\n", meta.RowCount)
	fmt.Printf("IPv4 记录数: %d\n", meta.IPv4Count)
	fmt.Printf("IPv6 记录数: %d\n", meta.IPv6Count)
}
