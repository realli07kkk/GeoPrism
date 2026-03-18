package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"geoprism/backend/ipdb"
	"geoprism/backend/provider"
	"geoprism/backend/resolver"
)

// App 结构体
type App struct {
	providerStore *provider.ProviderStore
	resolver      *resolver.Resolver
	ipdbStore     *ipdb.Store
	ipdbRootDir   string
	ipdbWarning   string

	ipdbWarningShown bool
	ipdbInitialized  bool
}

// NewApp 创建并初始化 App
func NewApp() (*App, error) {
	configDir, err := ensureConfigDirReady()
	if err != nil {
		return nil, err
	}

	store, err := provider.NewProviderStore(configDir)
	if err != nil {
		return nil, fmt.Errorf("初始化 Provider 存储失败: %w", err)
	}

	ipdbRootDir, err := getIPDBDir()
	if err != nil {
		return nil, err
	}

	app := &App{
		providerStore: store,
		resolver:      resolver.NewResolver(),
		ipdbRootDir:   ipdbRootDir,
	}

	return app, nil
}

// Close 关闭应用持有的资源。
func (a *App) Close() error {
	if a == nil || a.ipdbStore == nil {
		return nil
	}
	return a.ipdbStore.Close()
}

// ensureIPDBStore 按需初始化离线 IP 库。
func (a *App) ensureIPDBStore() *ipdb.Store {
	if a == nil {
		return nil
	}
	if a.ipdbInitialized {
		return a.ipdbStore
	}

	a.ipdbInitialized = true

	ipdbStore, err := ipdb.OpenCurrent(a.ipdbRootDir)
	switch {
	case err == nil:
		a.ipdbStore = ipdbStore
	case errors.Is(err, ipdb.ErrNoCurrentDatabase):
		a.setIPDBWarning("未找到可用的离线 IP 库，跳过 IP 匹配")
	default:
		a.setIPDBWarning(fmt.Sprintf("加载离线 IP 库失败，跳过 IP 匹配: %v", err))
	}

	return a.ipdbStore
}

// setIPDBWarning 设置离线 IP 库警告信息。
func (a *App) setIPDBWarning(message string) {
	if a == nil || message == "" {
		return
	}
	if a.ipdbWarning != "" {
		return
	}
	a.ipdbWarning = message
}

// ==================== CLI 子命令 ====================

// runQuery 执行域名查询
func (a *App) runQuery(args []string) {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	recordType := fs.String("t", "A", "记录类型 (A/AAAA/CNAME/TXT/NS/MX/SOA)")
	fs.StringVar(recordType, "type", "A", "记录类型 (A/AAAA/CNAME/TXT/NS/MX/SOA)")
	providerFlag := fs.String("p", "", "Provider 名称，逗号分隔")
	fs.StringVar(providerFlag, "provider", "", "Provider 名称，逗号分隔")
	timeout := fs.Int("timeout", 5000, "超时毫秒")
	fs.Parse(reorderArgs(args))

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "错误: 请指定要查询的域名")
		fmt.Fprintln(os.Stderr, "用法: geoprism query <domain> [-t TYPE] [-p PROVIDER]")
		os.Exit(1)
	}

	domain := fs.Arg(0)

	// 按名称匹配 Provider
	var providerIDs []string
	if *providerFlag != "" {
		names := strings.Split(*providerFlag, ",")
		providerIDs = a.matchProvidersByName(names)
		if len(providerIDs) == 0 {
			fmt.Fprintf(os.Stderr, "错误: 未找到匹配的 Provider: %s\n", *providerFlag)
			os.Exit(1)
		}
	}

	result, err := a.QueryDomain(QueryRequest{
		Domain:      domain,
		RecordType:  strings.ToUpper(*recordType),
		ProviderIDs: providerIDs,
		Timeout:     *timeout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "查询失败: %v\n", err)
		os.Exit(1)
	}
	a.printIPDBWarning()

	// 输出结果
	fmt.Printf("\n查询: %s (%s)\n\n", result.Domain, result.RecordType)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)
	fmt.Fprintln(w, "Provider\t状态\t延迟\t记录\tTTL")
	fmt.Fprintln(w, "--------\t------\t----\t----------------\t---")

	for _, ans := range result.Answers {
		if !ans.Success {
			errMsg := ans.Error
			if errMsg == "" {
				errMsg = ans.RCodeName
			}
			fmt.Fprintf(w, "%s\tERROR\t-\t%s\t-\n", ans.Provider, errMsg)
			continue
		}

		if len(ans.Answers) == 0 {
			fmt.Fprintf(w, "%s\t%s\t%dms\t-\t-\n",
				ans.Provider, ans.RCodeName, ans.RTTMs)
			continue
		}

		// 第一条记录显示 Provider 名称
		fmt.Fprintf(w, "%s\t%s\t%dms\t%s\t%d\n",
			ans.Provider, ans.RCodeName, ans.RTTMs,
			extractRecordData(ans.Answers[0].Data), ans.Answers[0].TTL)

		// 后续记录 Provider 列留空
		for _, r := range ans.Answers[1:] {
			fmt.Fprintf(w, "\t\t\t%s\t%d\n",
				extractRecordData(r.Data), r.TTL)
		}
	}
	w.Flush()

	fmt.Printf("\n总耗时: %dms\n", result.TotalTime)
	writeIPMatches(os.Stdout, result.IPMatches)
}

// runProviders 列出所有 Provider
func (a *App) runProviders() {
	providers := a.ListProviders()

	if len(providers) == 0 {
		fmt.Println("暂无 Provider")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)
	fmt.Fprintln(w, "名称\t协议\t端点\t启用")
	fmt.Fprintln(w, "--------\t------\t----\t----")

	for _, p := range providers {
		enabled := "是"
		if !p.Enabled {
			enabled = "否"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Name, p.Protocol, p.Endpoint, enabled)
	}
	w.Flush()
}

// runTest 测试 Provider 连通性
func (a *App) runTest(args []string) {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	all := fs.Bool("all", false, "测试所有 Provider")
	fs.Parse(args)

	var testIDs []string

	if *all {
		for _, p := range a.providerStore.List() {
			testIDs = append(testIDs, p.ID)
		}
	} else if fs.NArg() > 0 {
		// 按名称匹配
		names := []string{fs.Arg(0)}
		testIDs = a.matchProvidersByName(names)
	}

	if len(testIDs) == 0 {
		fmt.Fprintln(os.Stderr, "错误: 请指定 Provider 名称或使用 --all")
		fmt.Fprintln(os.Stderr, "用法: geoprism test <name> 或 geoprism test --all")
		os.Exit(1)
	}

	// 构建 ID 到名称的映射
	nameMap := make(map[string]string)
	for _, p := range a.providerStore.List() {
		nameMap[p.ID] = p.Name
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)
	fmt.Fprintln(w, "Provider\t状态\t延迟\t信息")
	fmt.Fprintln(w, "--------\t------\t----\t----")

	for _, id := range testIDs {
		health, err := a.TestProvider(id)
		name := nameMap[id]
		if err != nil {
			fmt.Fprintf(w, "%s\tERROR\t-\t%v\n", name, err)
			continue
		}

		status := "OK"
		if !health.Success {
			status = "FAIL"
		}
		fmt.Fprintf(w, "%s\t%s\t%dms\t%s\n", name, status, health.LatencyMs, health.Message)
	}
	w.Flush()
}

// reorderArgs 将 flag 参数移到位置参数前面
// 使得 "example.com -t AAAA" 和 "-t AAAA example.com" 等价
func reorderArgs(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			// 如果下一个参数是 flag 的值（不以 - 开头），也移过去
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	return append(flags, positional...)
}

// extractRecordData 从完整 DNS 记录字符串中提取纯数据部分
// 例如 "example.com 300 IN A 104.18.26.120" → "104.18.26.120"
func extractRecordData(data string) string {
	// 格式: name TTL class type value...
	// 按空白分割，取第 4 个字段之后的内容
	fields := strings.Fields(data)
	if len(fields) >= 5 {
		return strings.Join(fields[4:], " ")
	}
	return data
}

// matchProvidersByName 按名称匹配 Provider，返回 ID 列表
func (a *App) matchProvidersByName(names []string) []string {
	var ids []string
	allProviders := a.providerStore.List()

	for _, name := range names {
		name = strings.TrimSpace(name)
		for _, p := range allProviders {
			if strings.EqualFold(p.Name, name) {
				ids = append(ids, p.ID)
				break
			}
		}
	}
	return ids
}

// ==================== Provider 管理 ====================

// ListProviders 获取所有 Provider
func (a *App) ListProviders() []provider.ProviderView {
	providers := a.providerStore.List()
	result := make([]provider.ProviderView, len(providers))
	for i, p := range providers {
		result[i] = p.ToView()
	}
	return result
}

// UpsertProvider 创建或更新 Provider
func (a *App) UpsertProvider(p provider.ProviderView) error {
	prov := provider.Provider{
		ID:         p.ID,
		Name:       p.Name,
		Protocol:   p.Protocol,
		Endpoint:   p.Endpoint,
		ServerName: p.ServerName,
		Port:       p.Port,
		Timeout:    p.Timeout,
		Enabled:    p.Enabled,
		Tags:       p.Tags,
	}
	return a.providerStore.Upsert(prov)
}

// DeleteProvider 删除 Provider
func (a *App) DeleteProvider(id string) error {
	return a.providerStore.Delete(id)
}

// ==================== Provider 健康检查 ====================

// ProviderHealth Provider 健康状态
type ProviderHealth struct {
	ProviderID string `json:"provider_id"`
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	LatencyMs  int64  `json:"latency_ms"`
}

// TestProvider 测试 Provider 连通性
func (a *App) TestProvider(id string) (ProviderHealth, error) {
	p, ok := a.providerStore.Get(id)
	if !ok {
		return ProviderHealth{ProviderID: id}, fmt.Errorf("provider not found")
	}

	success, msg, latency := a.resolver.TestConnection(
		p.Endpoint,
		p.ServerName,
		p.Port,
		string(p.Protocol),
		p.Timeout,
	)

	return ProviderHealth{
		ProviderID: id,
		Success:    success,
		Message:    msg,
		LatencyMs:  latency,
	}, nil
}

// ==================== DNS 查询 ====================

// QueryRequest 查询请求
type QueryRequest struct {
	Domain      string   `json:"domain"`
	RecordType  string   `json:"record_type"`
	ProviderIDs []string `json:"provider_ids"`
	Timeout     int      `json:"timeout"`
}

// QueryAnswer 查询响应
type QueryAnswer struct {
	ProviderID string               `json:"provider_id"`
	Provider   string               `json:"provider_name"`
	RCode      int                  `json:"rcode"`
	RCodeName  string               `json:"rcode_name"`
	Answers    []resolver.DNSRecord `json:"answers"`
	TTL        uint32               `json:"ttl"`
	RTTMs      int64                `json:"rtt_ms"`
	Error      string               `json:"error,omitempty"`
	Success    bool                 `json:"success"`
}

// QueryResultView 查询结果视图
type QueryResultView struct {
	Domain     string        `json:"domain"`
	RecordType string        `json:"record_type"`
	Answers    []QueryAnswer `json:"answers"`
	IPMatches  []IPMatchView `json:"ip_matches"`
	TotalTime  int64         `json:"total_time_ms"`
}

// QueryDomain 执行域名查询
func (a *App) QueryDomain(req QueryRequest) (QueryResultView, error) {
	var providers []resolver.ProviderInfo

	if len(req.ProviderIDs) == 0 {
		// 查询所有启用的 Provider
		for _, p := range a.providerStore.GetEnabled() {
			providers = append(providers, resolver.ProviderInfo{
				ID:         p.ID,
				Endpoint:   p.Endpoint,
				ServerName: p.ServerName,
				Port:       p.Port,
				Protocol:   string(p.Protocol),
				Name:       p.Name,
			})
		}
	} else {
		for _, id := range req.ProviderIDs {
			if p, ok := a.providerStore.Get(id); ok {
				providers = append(providers, resolver.ProviderInfo{
					ID:         p.ID,
					Endpoint:   p.Endpoint,
					ServerName: p.ServerName,
					Port:       p.Port,
					Protocol:   string(p.Protocol),
					Name:       p.Name,
				})
			}
		}
	}

	if len(providers) == 0 {
		return QueryResultView{}, fmt.Errorf("no providers available")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 5000
	}

	result := a.resolver.QueryMulti(providers, resolver.DNSQuery{
		Domain:     req.Domain,
		RecordType: resolver.RecordType(req.RecordType),
		Timeout:    timeout,
	})

	view := QueryResultView{
		Domain:     result.Domain,
		RecordType: string(result.RecordType),
		Answers:    make([]QueryAnswer, len(result.Answers)),
		TotalTime:  result.TotalTime,
	}

	providerNames := make(map[string]string)
	for _, p := range providers {
		providerNames[p.ID] = p.Name
	}

	for i, ans := range result.Answers {
		view.Answers[i] = QueryAnswer{
			ProviderID: ans.ProviderID,
			Provider:   providerNames[ans.ProviderID],
			RCode:      ans.RCode,
			RCodeName:  ans.RCodeName,
			Answers:    ans.Answers,
			TTL:        ans.TTL,
			RTTMs:      ans.RTTMs,
			Error:      ans.Error,
			Success:    ans.Success,
		}
	}
	view.IPMatches = a.collectIPMatches(view.Answers)

	return view, nil
}
