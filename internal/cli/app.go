package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"geoprism/backend/ipdb"
	"geoprism/backend/ipinfo"
	"geoprism/backend/provider"
	"geoprism/backend/resolver"
	"geoprism/backend/settings"
	"geoprism/render"
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
	ipdbInitErr      error
	outputMode       render.OutputMode

	settings     *settings.SettingsStore
	ipinfoClient *ipinfo.Client // token 为空时为 nil
	ipinfoLookup func(string) *ipinfo.Response
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

	// 加载 settings.toml（可选，失败不退出）
	settingsStore, settingsErr := settings.NewSettingsStore(configDir)
	if settingsErr != nil {
		fmt.Fprintf(os.Stderr, "警告: 加载应用配置失败: %v\n", settingsErr)
	}

	// 有 ipinfo token 时创建客户端
	var ipinfoClient *ipinfo.Client
	if settingsStore != nil && settingsStore.IsIPInfoEnabled() {
		ipinfoClient = ipinfo.NewClient(settingsStore.IPInfoToken())
	}

	ipdbRootDir, err := getIPDBDir()
	if err != nil {
		return nil, err
	}

	app := &App{
		providerStore: store,
		resolver:      resolver.NewResolver(),
		ipdbRootDir:   ipdbRootDir,
		settings:      settingsStore,
		ipinfoClient:  ipinfoClient,
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

// SetOutputMode 设置输出模式。
func (a *App) SetOutputMode(mode render.OutputMode) {
	if a != nil {
		a.outputMode = mode
	}
}

// writeError 统一输出错误信息，JSON 模式下输出 JSON，文本模式下输出纯文本。
func (a *App) writeError(message string) {
	if a != nil && a.outputMode == render.OutputJSON {
		render.WriteJSON(os.Stderr, map[string]string{"error": message})
	} else {
		fmt.Fprintf(os.Stderr, "错误: %s\n", message)
	}
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
	if err == nil {
		a.ipdbStore = ipdbStore
		a.recordIPDBInitError(nil)
		// v1 格式库历史上允许运行期把 ipinfo /32、/128 回写进 base keyspace，
		// 破坏 base "网段不重叠" 不变量（issue 2026-06-20-ipdb-writeback-breaks-lpm）。
		// 在线回写现已停用；已存在的 v1 库无法判断是否被污染，统一提示用户重建。
		if ipdbStore.Metadata().FormatVersion < 2 {
			a.setIPDBWarning("检测到旧版离线库格式，在线回写已停用；如曾启用 ipinfo 回写，请重新执行 geoprism ipdb build --csv <绝对路径> 重建离线库")
		}
		return a.ipdbStore
	}
	a.recordIPDBInitError(err)

	return a.ipdbStore
}

// hasIPInfoLookup 返回是否可执行 ipinfo 查询。
func (a *App) hasIPInfoLookup() bool {
	return a != nil && (a.ipinfoClient != nil || a.ipinfoLookup != nil)
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

// recordIPDBInitError 统一记录离线库初始化结果，避免错误状态和告警文案分裂。
func (a *App) recordIPDBInitError(err error) {
	if a == nil {
		return
	}

	a.ipdbInitErr = err
	if err == nil {
		return
	}

	if errors.Is(err, ipdb.ErrNoCurrentDatabase) {
		a.setIPDBWarning("未找到可用的离线 IP 库，跳过 IP 匹配")
		return
	}

	a.setIPDBWarning(fmt.Sprintf("加载离线 IP 库失败，跳过 IP 匹配: %v", err))
}

// ==================== CLI 子命令 ====================

// runQuery 执行域名查询
func (a *App) runQuery(args []string) {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	recordType := fs.String("t", "A", "记录类型 (A/AAAA/CNAME/TXT/NS/MX/SOA/PTR)")
	fs.StringVar(recordType, "type", "A", "记录类型 (A/AAAA/CNAME/TXT/NS/MX/SOA/PTR)")
	providerFlag := fs.String("p", "", "Provider 名称，逗号分隔")
	fs.StringVar(providerFlag, "provider", "", "Provider 名称，逗号分隔")
	timeout := fs.Int("timeout", 5000, "超时毫秒")
	jsonFlag := fs.Bool("j", false, "JSON 格式输出")
	fs.BoolVar(jsonFlag, "json", false, "JSON 格式输出")
	ptrFlag := fs.Bool("x", false, "反向 PTR 查询（IP → 域名）")
	fs.BoolVar(ptrFlag, "ptr", false, "反向 PTR 查询（IP → 域名）")
	fs.Parse(reorderArgs(args))

	if *jsonFlag {
		a.outputMode = render.OutputJSON
	}

	if fs.NArg() == 0 {
		a.writeError("请指定要查询的域名")
		os.Exit(1)
	}

	domain := fs.Arg(0)

	// 处理反向查询
	if *ptrFlag {
		ip := parseIPAndValidate(domain)
		if ip == nil {
			a.writeError("-x 参数需要有效的 IP 地址")
			os.Exit(1)
		}
		domain = ipToReverseName(ip)
		*recordType = "PTR"
	}

	// 按名称匹配 Provider
	var providerIDs []string
	if *providerFlag != "" {
		names := strings.Split(*providerFlag, ",")
		providerIDs = a.matchProvidersByName(names)
		if len(providerIDs) == 0 {
			a.writeError("未找到匹配的 Provider: " + *providerFlag)
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
		a.writeError(err.Error())
		os.Exit(1)
	}

	a.printIPDBWarning()

	if a.outputMode == render.OutputJSON {
		render.WriteJSON(os.Stdout, result)
		return
	}

	render.WriteQueryResult(os.Stdout, result)
	render.WriteIPMatches(os.Stdout, result)
	render.WriteNSInfo(os.Stdout, result.NSInfo)
}

// runProviders 列出所有 Provider
func (a *App) runProviders(args []string) {
	fs := flag.NewFlagSet("providers", flag.ExitOnError)
	jsonFlag := fs.Bool("j", false, "JSON 格式输出")
	fs.BoolVar(jsonFlag, "json", false, "JSON 格式输出")
	fs.Parse(args)

	if *jsonFlag {
		a.outputMode = render.OutputJSON
	}

	providers := a.ListProviders()

	if a.outputMode == render.OutputJSON {
		render.WriteJSON(os.Stdout, providers)
		return
	}

	if len(providers) == 0 {
		fmt.Println("暂无 Provider")
		return
	}

	render.WriteProviders(os.Stdout, providerRenderList(providers))
}

// runTest 测试 Provider 连通性
func (a *App) runTest(args []string) {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	all := fs.Bool("all", false, "测试所有 Provider")
	jsonFlag := fs.Bool("j", false, "JSON 格式输出")
	fs.BoolVar(jsonFlag, "json", false, "JSON 格式输出")
	fs.Parse(args)

	if *jsonFlag {
		a.outputMode = render.OutputJSON
	}

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
		a.writeError("请指定 Provider 名称或使用 --all")
		os.Exit(1)
	}

	// 构建 ID 到名称的映射
	nameMap := make(map[string]string)
	for _, p := range a.providerStore.List() {
		nameMap[p.ID] = p.Name
	}

	results := make(providerTestResultList, 0, len(testIDs))
	for _, id := range testIDs {
		health, err := a.TestProvider(id)
		name := nameMap[id]
		results = append(results, newProviderTestResult(name, health, err))
	}

	if a.outputMode == render.OutputJSON {
		render.WriteJSON(os.Stdout, results)
		return
	}

	render.WriteTestResults(os.Stdout, results)
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

type providerRenderItem provider.ProviderView

// NameText 返回 Provider 名称。
func (p providerRenderItem) NameText() string {
	return p.Name
}

// ProtocolText 返回协议名。
func (p providerRenderItem) ProtocolText() string {
	return string(p.Protocol)
}

// EndpointText 返回端点。
func (p providerRenderItem) EndpointText() string {
	return p.Endpoint
}

// EnabledState 返回启用状态。
func (p providerRenderItem) EnabledState() bool {
	return p.Enabled
}

type providerRenderList []provider.ProviderView

// ProviderCount 返回 Provider 数量。
func (p providerRenderList) ProviderCount() int {
	return len(p)
}

// ProviderAt 返回指定 Provider。
func (p providerRenderList) ProviderAt(i int) render.ProviderSource {
	return providerRenderItem(p[i])
}

type providerTestResult struct {
	Name      string `json:"provider_name"`
	Success   bool   `json:"success"`
	LatencyMs int64  `json:"latency_ms"`
	Message   string `json:"message"`

	// 内部字段，用于区分 ERROR（执行错误）和 FAIL（探测失败）
	hasExecError bool
}

// newProviderTestResult 构造测试结果。
func newProviderTestResult(name string, health ProviderHealth, err error) providerTestResult {
	r := providerTestResult{Name: name}
	if err != nil {
		r.hasExecError = true
		r.Message = err.Error()
	} else {
		r.Success = health.Success
		r.LatencyMs = health.LatencyMs
		r.Message = health.Message
	}
	return r
}

// NameText 返回 Provider 名称。
func (r providerTestResult) NameText() string {
	return r.Name
}

// StatusText 返回测试状态。
// ERROR: 执行出错（如 Provider 不存在）
// FAIL: 探测失败（如 DNS 返回错误响应）
// OK: 成功
func (r providerTestResult) StatusText() string {
	if r.hasExecError {
		return "ERROR"
	}
	if !r.Success {
		return "FAIL"
	}
	return "OK"
}

// LatencyMsValue 返回延迟。
func (r providerTestResult) LatencyMsValue() int64 {
	return r.LatencyMs
}

// MessageText 返回结果说明。
func (r providerTestResult) MessageText() string {
	return r.Message
}

type providerTestResultList []providerTestResult

// ResultCount 返回结果数量。
func (r providerTestResultList) ResultCount() int {
	return len(r)
}

// ResultAt 返回指定结果。
func (r providerTestResultList) ResultAt(i int) render.TestResultSource {
	return r[i]
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

// ProviderName 返回 Provider 名称。
func (a QueryAnswer) ProviderName() string {
	return a.Provider
}

// SuccessState 返回查询是否成功。
func (a QueryAnswer) SuccessState() bool {
	return a.Success
}

// RCodeNameText 返回 RCode 名称。
func (a QueryAnswer) RCodeNameText() string {
	return a.RCodeName
}

// RTTMsValue 返回耗时。
func (a QueryAnswer) RTTMsValue() int64 {
	return a.RTTMs
}

// ErrorText 返回错误信息。
func (a QueryAnswer) ErrorText() string {
	return a.Error
}

// RecordCount 返回记录数量。
func (a QueryAnswer) RecordCount() int {
	return len(a.Answers)
}

// RecordDataAt 返回指定记录的展示值。
func (a QueryAnswer) RecordDataAt(i int) string {
	return extractRecordData(a.Answers[i].Data)
}

// RecordTTLAt 返回指定记录的 TTL。
func (a QueryAnswer) RecordTTLAt(i int) uint32 {
	return a.Answers[i].TTL
}

// QueryResultView 查询结果视图
type QueryResultView struct {
	Domain     string        `json:"domain"`
	RecordType string        `json:"record_type"`
	Answers    []QueryAnswer `json:"answers"`
	IPMatches  []IPMatchView `json:"ip_matches"`
	TotalTime  int64         `json:"total_time_ms"`
	NSInfo     *NSInfoView   `json:"ns_info,omitempty"` // NS 服务器信息
}

// DomainText 返回查询域名。
func (v QueryResultView) DomainText() string {
	return v.Domain
}

// RecordTypeText 返回记录类型。
func (v QueryResultView) RecordTypeText() string {
	return v.RecordType
}

// TotalTimeMs 返回总耗时。
func (v QueryResultView) TotalTimeMs() int64 {
	return v.TotalTime
}

// AnswerCount 返回 Provider 响应数量。
func (v QueryResultView) AnswerCount() int {
	return len(v.Answers)
}

// AnswerAt 返回指定 Provider 响应。
func (v QueryResultView) AnswerAt(i int) render.QueryAnswerSource {
	return v.Answers[i]
}

// MatchCount 返回 IP 匹配数量。
func (v QueryResultView) MatchCount() int {
	return len(v.IPMatches)
}

// MatchAt 返回指定 IP 匹配结果。
func (v QueryResultView) MatchAt(i int) render.IPMatchSource {
	return v.IPMatches[i]
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

	// 始终查询 NS 信息
	nsInfo := a.queryNSInfo(req.Domain, providers, timeout)
	view.NSInfo = &nsInfo

	return view, nil
}
