# 开发、测试与质量门

本文是改 GeoPrism 代码前后的操作手册：命令、提交前必须跑的检查、测试矩阵、踩过的坑。架构与行为见 [architecture.md](architecture.md) 及各子系统文档。

<a id="env"></a>
## 1. 环境与命令

- Go 1.26（`go.mod`）；无 CGO 依赖。
- 首次运行会在 `~/.geoprism/config/` 写入默认 `providers.toml` 与 `settings.toml`（见 [architecture.md §3](architecture.md#state)）。
- 离线 IP 库源文件 `ipinfo*.csv` 被 `.gitignore` 忽略；`bin/` 与 `geoprism` 构建产物同样忽略。

```bash
# 编译 / 运行
go build -o geoprism .
go run . example.com
go run . 1.1.1.1                     # 需先构建离线库
go run . ipdb build --csv /absolute/path/ipinfo_lite.csv

# 跨平台编译到 bin/（linux/darwin amd64+arm64，windows amd64）
./build.sh                            # 或 PowerShell: ./build.ps1

# 测试与静态检查
go test ./...
go test -race -count=1 ./backend/ipdb   # ipdb 含跨进程锁测试，较慢
go vet ./...
gofmt -l .                             # 期望无输出
git diff --check                       # 期望无输出
```

<a id="gates"></a>
## 2. 提交前质量门（硬要求）

改任何 Go 代码后，**三件套必须全过，任一不过不准合并**：

```bash
go vet ./... && gofmt -l . && git diff --check
```

来源：issue `2026-06-20-nondeterministic-result-order` 的 CR-001——只跑了 `go test` 漏掉 `go vet`，一个 `sync.Mutex` 死代码触发 copylocks 进了主干，初版验收记录"vet 无告警"是事实错误。

<a id="tests"></a>
## 3. 测试矩阵

测试即契约证据；改对应模块时先跑对应包，再看 [ipdb-v2.md §14](ipdb-v2.md#tests) 的详细清单。

| 包 | 覆盖 | 关键测试 |
|---|---|---|
| `backend/resolver` | `QueryMulti` 顺序契约（逆序完成 / error / nil answer / 并发）、`normalizeAnswer` | `TestQueryMulti*`、`TestNormalizeAnswer` |
| `backend/provider` | 默认模板、TOML 加载、Upsert/Delete 持久化、非法配置、忽略 legacy JSON、`List`/`GetEnabled` 保序、Upsert 保位/追加 | `TestProviderStore*`、`TestNewProviderStore*` |
| `backend/ipdb` | 构建（双索引、重复 prefix、重叠允许、双写不拆、staging、并发、锁释放、崩溃恢复）、codec 正负例、metadata、LPM/CIDR property、`OpenCurrent` 判版、overlay 全量 | 见 [ipdb-v2.md §14](ipdb-v2.md#tests) |
| `internal/cli` | 命令路由、help、providers/query/test JSON、IP/CIDR JSON 与文本、无库软失败、flag 错误走 JSON、`-j` 不支持警告、v1 库降级、回写停用、NS 探测与错误选择、`paths` | `cli_test.go`、`ip_lookup_test.go`、`cidr_lookup_test.go`、`ip_match_test.go`、`issue_writeback_fix_test.go`、`ns_info_test.go`、`provider_order_test.go`、`paths_test.go` |
| `render` | IP / CIDR / NS 的 TTY 与非 TTY 渲染、typed nil 处理 | `TestWriteIPLookup`、`TestWriteCIDRLookup`、`TestWriteNSInfo*` |

跨进程锁测试（`TestConcurrentBuildsAcrossProcessesKeepCurrentTarget`、`TestBuildLockReleasedAfterProcessExit`、`TestOpenOverlayLockAcrossProcessesAndProcessExit`）通过子进程 helper 实现；它们依赖 `flock` / `LockFileEx`，Windows 只做了交叉编译，没有真实运行验证。

<a id="netip"></a>
## 4. `net/netip` 陷阱

1. **`netip.PrefixFrom(addr, N)` 越界不 panic**（IPv4 > 32 / IPv6 > 128）：返回 `Bits()==-1` 的 invalid Prefix。写 codec / builder / store 必须显式判 `IsValid()`，并判 `p.Addr() == p.Masked().Addr()`。（来源：`ipdb-v2-schema` CR-001）
2. **`netip.Addr.Is6()` 包含 IPv4-mapped IPv6**（`::ffff:x.x.x.x`，`Is4In6()==true`）。做 family 分发时若需排除，必须显式判 `Is4In6()`；ipdb codec 一律拒绝（D-10）。
3. **codec 不内部 Mask**：`encodePrimaryKeyV2` / `encodeCIDRKeyV2` 要求入参已 `Masked()`，否则报错。查询侧先 `PrefixFrom(addr, l).Masked()` 再编码。
4. **造 v2 fixture 必须排序**：`buildV2FromCSV` 要求 CSV 在每个 family 内起始地址非递减；随机生成的 prefix 先按 `(family, addrBytes)` 排序再写 CSV，否则会被判乱序。
5. `netip.ParsePrefix("10.1.2.3/24")` 会保留 host bits；CLI 入口 `parseIPInput` / `LookupCIDR` 都会 `Masked()` 规范化，但 CSV 构建对非规范网段**直接拒绝**。

<a id="fs"></a>
## 5. 文件系统与锁陷阱

- **探测"名字是否空闲"要用 `os.Lstat`，不能用 `os.Stat`**：`Stat` 跟随 symlink，dangling symlink 会被误判为空闲，随后 `rename` 失败。规则：`Lstat` 返回 `IsNotExist` → 可用；`err == nil` → 占用，换下一个后缀；其它错误 → 返回。`Lstat` 不消除 TOCTOU，唯一性靠 overlay 生命周期锁串行化。（来源：`ipdb-overlay-store` CR）
- **时间戳进文件名要 Windows-safe**：用 UTC `20060102T150405.000000000Z`，不含冒号；不依赖时间戳唯一，冲突时加 `-N` 后缀。
- **锁的语义**：`flock` 对 `EINTR` 重试；`LockFileEx` 用 `LOCKFILE_FAIL_IMMEDIATELY` 实现非阻塞；非阻塞 `busy=true` 只表示"被其它 owner 占用"，打开锁文件 / 权限错误仍通过 `err` 返回，不要混为一谈。
- **Close 顺序**：先关 Pebble 再释放文件锁，否则 builder 可能在 DB 仍用旧版本文件时删掉它（`BaseStore.Close` / `OverlayStore.Close`）。
- **调用方持有 `Store` 时必须先 `Close` 再 `BuildFromCSV`**：发布阶段会等待所有 reader 释放 `VERSIONS.lock`。

<a id="decode"></a>
## 6. 解码与并发经验

- **decode 必须与 encode 对称校验**：坏 key 被当成功、坏 value panic，根因都是 decode 少了 encode 侧的检查。列一张"encode 时所有协议不变量"清单，逐条在 decode 侧确认。
- **任何 `uint64 → int` 转换先做上界检查**（`addLenOffset`），否则 `int()` 溢出为负让 `offset + n > len` 这类判断失真。
- **损坏数据返回 error 不 panic**；panic 只留给程序性错误。
- **并发收集结果按 index 归位**，不要 `append`（D-06）；`ns_info.go` `queryNSIPs` 同样按 index 写。
- **测试全绿 ≠ 守住边界**：补负例矩阵（截断、多余字节、错版本、错 flags、越界、未知 kind），并用 `go vet` 抓 copylocks。

<a id="platforms"></a>
## 7. 平台支持

| 平台 | 文件锁 | 验证程度 |
|---|---|---|
| darwin（主 target） | `flock` | 本地运行 + 测试 |
| linux / freebsd / netbsd / openbsd / dragonfly | `flock` | 交叉编译；行为同 darwin |
| windows | `LockFileEx` | 仅交叉编译（`build.sh` 产出 `*-windows-amd64.exe`），无运行时测试 |
| solaris | `flock`（在 build tag 内） | **未验证**；Pebble 对 Solaris 的支持需按当前 `pebble/v2` 版本复核 |
| 其它 | 显式报错"当前操作系统不支持 IPDB 生命周期文件锁" | 不可用 ipdb |

建议：若要在 Windows / Solaris 上正式支持，先在 CI 加对应 job 跑 `go test ./backend/ipdb`。
