# Attention

本文件是 CodeStable 技能启动必读的项目注意事项入口。所有 CodeStable 子技能开始工作前必须读取它。

## 项目碎片知识

<!-- cs-note managed: 用 cs-note 维护，新条目按下面分节追加 -->

### 编译与构建

### 运行与本地起服务

### 测试

- 改 Go 代码后必须跑全 `go vet ./...` + `gofmt -l` + `git diff --check` 三件套；任一不过不准合并（issue 2026-06-20-nondeterministic-result-order 的 CR-001 就是只跑了 `go test` 漏掉 `go vet` 导致 copylocks 进主干）

### 命令与脚本陷阱

- `netip.PrefixFrom(addr, N)` 对越界 N（IPv4 >32 / IPv6 >128）不 panic，返回 `Bits()==-1` 的 invalid Prefix；写 codec/builder/store 必须主动判 `IsValid()` 和 `p.Addr()==p.Masked().Addr()`（来源：ipdb-v2-schema CR-001）
- `netip.Addr.Is6()` 包含 IPv4-mapped IPv6（`::ffff:x.x.x.x`，`Is4In6()==true`）；做 family 分发时若需排除此类地址，必须显式判 `Is4In6()`（来源：ipdb-v2-schema CR-004，详见 decision ipdb-codec-reject-ipv4-mapped-ipv6）
- ipdb v2 builder（`buildV2FromCSV`）要求 CSV 按 family 内起始地址非递减排序，乱序直接 reject；造 v2 fixture 时随机生成的 prefix 必须先按 (family, addrBytes) 排序再写 CSV（来源：ipdb-v2-query property test 踩坑）

### 路径与目录约定

### 环境变量与凭证

### 其他

- ipdb 的 base keyspace 不允许运行期写入；任何在线回写必须走独立 overlay（A′ 落地前已全部禁用，见 issue 2026-06-20-ipdb-writeback-breaks-lpm）
- ipdb v2 base 构建对重复 prefix 直接失败（ErrDuplicatePrefix）：允许不同 prefix 重叠、只拒绝同 family 内 Masked 后完全相同的 prefix，不再沿用 v1“任何区间重叠都拒绝”（详见 compound/2026-06-22-decision-ipdb-base-reject-duplicate-prefix.md）
- ipdb v2 CIDR 二级索引用零长度 value：canonical Record 只存 primary、LookupCIDR 回查 primary 取值，primary 与 cidr 必须同一 batch 原子写（详见 compound/2026-06-22-decision-ipdb-cidr-index-empty-value.md）
