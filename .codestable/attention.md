# Attention

本文件是 CodeStable 技能启动必读的项目注意事项入口。所有 CodeStable 子技能开始工作前必须读取它。

## 项目碎片知识

<!-- cs-note managed: 用 cs-note 维护，新条目按下面分节追加 -->

### 编译与构建

### 运行与本地起服务

### 测试

- 改 Go 代码后必须跑全 `go vet ./...` + `gofmt -l` + `git diff --check` 三件套；任一不过不准合并（issue 2026-06-20-nondeterministic-result-order 的 CR-001 就是只跑了 `go test` 漏掉 `go vet` 导致 copylocks 进主干）

### 命令与脚本陷阱

### 路径与目录约定

### 环境变量与凭证

### 其他

- ipdb 的 base keyspace 不允许运行期写入；任何在线回写必须走独立 overlay（A′ 落地前已全部禁用，见 issue 2026-06-20-ipdb-writeback-breaks-lpm）
- ipdb v2 base 构建对重复 prefix 直接失败（ErrDuplicatePrefix）：允许不同 prefix 重叠、只拒绝同 family 内 Masked 后完全相同的 prefix，不再沿用 v1“任何区间重叠都拒绝”（详见 compound/2026-06-22-decision-ipdb-base-reject-duplicate-prefix.md）
- ipdb v2 CIDR 二级索引用零长度 value：canonical Record 只存 primary、LookupCIDR 回查 primary 取值，primary 与 cidr 必须同一 batch 原子写（详见 compound/2026-06-22-decision-ipdb-cidr-index-empty-value.md）
