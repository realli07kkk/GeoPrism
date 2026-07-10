---
doc_type: issue-fix
issue: 2026-07-10-ipdb-concurrent-build-dangling-current
path: fast-track
fix_date: 2026-07-10
tags: [ipdb, concurrency, lifecycle, locking, staging, current, gc]
---

# IPDB 并发构建可能产生悬空 CURRENT 修复记录

## 1. 问题描述

多个进程并发执行 `BuildFromCSV` 时，共享的固定 staging、固定 `CURRENT.tmp`，以及发布后无协调的 `cleanupOldVersions` 会互相覆盖或删除目录。特定交错下，一个 builder 可删除另一个 builder 刚发布且已被 `CURRENT` 指向的正式版本，最终留下悬空 `CURRENT`，离线库无法打开。

## 2. 根因

- staging 路径仅由 `buildID` 决定，并在构建前被无条件删除，缺少构建所有权。
- 所有 builder 共用 `CURRENT.tmp`，临时文件写入和 rename 会互相干扰。
- `cleanupOldVersions` 只保留调用者自己的 `buildID`，没有跨进程发布协调，也不了解正在读取旧版本的 reader。
- 默认 `BuildID` 曾在锁外由 wall clock 生成；即使后续 builder 串行，同一 tick、时钟回拨或已有同名目录仍可能导致正式目录冲突。

## 3. 修复方案

采用两层跨进程文件锁，并保留 staging/CURRENT 的独立临时路径：

1. `BUILD.lock` 使用独占锁，覆盖 crash staging 回收、完整构建、发布和 GC，串行化同一 `rootDir` 下的 builder。
2. `VERSIONS.lock` 表达版本生命周期：
   - `OpenCurrent` 在读取 `CURRENT` 前获取共享锁；
   - 锁随真正持有 Pebble 句柄的 `BaseStore` 存活；
   - `BaseStore.Close` 先关闭 Pebble，再释放共享锁；
   - builder 在 rename、切换 `CURRENT` 和旧版本 GC 期间获取独占锁。
3. 锁序固定为 `BUILD(EX) → VERSIONS(EX)`；reader 只获取 `VERSIONS(SH)`，不存在反向取锁。
4. staging 使用 `MkdirTemp` 独占目录；`CURRENT` 使用 `CreateTemp`、`Sync`、rename，不再共享临时路径。
5. 发布成功后恢复有界生命周期：重读并校验磁盘 `CURRENT` 与当前数据库，随后只删除非当前正式版本和遗留 staging。校验失败时停止删除并输出告警，不回滚已成功发布的当前版本。
6. crash staging 回收会跳过实际 `CURRENT`，避免合法的 `.staging-*` BuildID 被误判。
7. 空 `BuildID` 在取得 `BUILD.lock` 后分配；默认时间戳与已有目录冲突时追加数字后缀。显式 `BuildID` 语义不变。
8. Unix 使用 `flock`（含 `EINTR` 重试）；Windows 使用 `LockFileEx`；`golang.org/x/sys v0.30.0` 从间接依赖转为直接依赖，版本与 checksum 不变。

## 4. 改动文件清单

| 文件 | 改动 |
|---|---|
| `backend/ipdb/file_lock_unix.go` | Unix 共享/独占文件锁与 `EINTR` 重试 |
| `backend/ipdb/file_lock_windows.go` | Windows `LockFileEx` / `UnlockFileEx` 实现 |
| `backend/ipdb/file_lock_unsupported.go` | 未支持平台返回明确错误，保持交叉编译边界完整 |
| `backend/ipdb/builder.go` | 独立 `CURRENT` 临时文件；恢复且加固 staging/旧版本回收；GC 前校验磁盘 CURRENT |
| `backend/ipdb/builder_v2.go` | 两锁编排、独立 staging、0755 权限、锁内默认 BuildID 唯一分配、GC 告警 |
| `backend/ipdb/store.go` | `OpenCurrent` 在读取 CURRENT 前获取共享生命周期锁 |
| `backend/ipdb/store_v2.go` | `BaseStore` 持有 reader lease；Close 先关 Pebble 再解锁并合并错误 |
| `backend/ipdb/types.go` | 锁文件名与独立 staging 语义 |
| `backend/ipdb/builder_v2_test.go` | goroutine/subprocess 并发、retention、reader handoff、kill 解锁、crash staging、保留前缀、默认 ID 测试 |
| `backend/ipdb/store_v2_test.go` | `OpenCurrent` 错误路径释放共享锁测试 |
| `go.mod` | `golang.org/x/sys v0.30.0` 标记为直接依赖 |
| `render/style.go` | 经用户明确授权，执行纯 `gofmt`，清除项目全局格式门的既有输出 |

## 5. 验证结果

### 复现与期望行为

- 6 路 goroutine 并发调用公开 `BuildFromCSV`，使用固定相同时钟和空 `BuildID`：全部成功，最终只保留 `CURRENT` 指向的一个正式版本，且可查询。
- 4 个真实 subprocess 并发调用公开空 `BuildID` 路径：全部成功，`CURRENT` 不悬空，无 staging 残留。
- 旧 reader 持有共享锁时，第二次真实构建完成 staging 后停在发布阶段；旧 reader 仍可查询。`Close` 后新版本发布完成并安全回收旧版本。
- 持有 `BUILD.lock` 的子进程被 kill 后，下一次构建成功取得锁并回收其 crash staging。
- 两次顺序构建后只保留第二个版本；人工遗留 staging 会在下一次构建回收。
- 合法正式版本名以 `.staging-` 开头时，后续失败构建不会删除当前库。
- `OpenCurrent` 的旧格式错误路径不会泄漏 `VERSIONS.lock`。
- 默认时间戳与已有正式目录同名时自动分配 `-1` 后缀，不覆盖旧目录、不因 rename 冲突失败。

### 质量门

以下命令全部通过：

```text
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
git diff --check
go mod verify
```

交叉编译通过：

```text
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go test -c ./backend/ipdb
CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go test -c ./backend/ipdb
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go test -c ./backend/ipdb
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c ./backend/ipdb
```

所有交叉编译产物均写入 `/tmp` 并已清理，仓库内没有构建产物。

## 6. 遗留事项

- 新旧二进制混跑时，旧版本不会参与 advisory lock 协议；项目当前没有 rolling mixed-version 安全合同，本次不扩展该能力。
- Windows 锁实现已通过交叉编译和官方 API 语义核对，但当前 macOS 环境无法执行 Windows runtime 测试。
- `rootDir` 需要可写以创建持久锁文件，符合当前 `~/.geoprism/ipdb` 同用户本地数据目录模型；未承诺 split-permission 部署。
- 调用方若持有未关闭的 `Store`，必须先 `Close` 再同步调用 `BuildFromCSV`；否则 builder 会等待由调用方自己持有的共享生命周期锁。公开函数注释已明确该约束。
- 文件锁面向本地文件系统，不声明 NFS/SMB 上的锁一致性保证。
