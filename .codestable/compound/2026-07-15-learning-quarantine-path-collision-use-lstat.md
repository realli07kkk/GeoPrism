---
doc_type: learning
track: pitfall
date: 2026-07-15
slug: quarantine-path-collision-use-lstat
component: backend/ipdb
severity: medium
tags: [go, filesystem, lstat, symlink, quarantine, rename, cross-platform]
---

# Quarantine 目标名探测必须用 Lstat 识别 dangling symlink

## 问题

`ipdb-overlay-store` 在确认 overlay 数据库损坏后，会把 `overlay/db`
重命名到带时间戳的 quarantine sibling，再创建 fresh DB。

选择 quarantine 目标名时，需要判断的是“这个目录项是否已经存在”，而不是
“沿着这个路径最终能否访问到对象”。如果使用 `os.Stat`，它会跟随 symlink；
当候选名是 dangling symlink 时，`Stat` 返回 `os.IsNotExist`，从而把一个已经
占用的目录项误判为空闲。

## 症状

候选路径已存在，但它是指向不存在目标的 symlink：

1. `os.Stat(candidate)` 返回 not exist。
2. 分配逻辑错误地选择该名称。
3. 后续把目录 rename 到该名称时，行为变得依赖操作系统和源/目标类型：
   可能因目标目录项已存在或类型不兼容而失败，也可能产生非预期覆盖。
4. quarantine 是损坏恢复路径，因此最终表现为本可自动恢复的 overlay 无法重建，
   或已有占位证据被破坏。

该问题不会直接损坏 active DB，失败时原始证据仍保留，因此严重度为 medium。

## 没用的做法

- 使用 `os.Stat` 配合 `os.IsNotExist` 判断候选名是否空闲。`Stat` 回答的是
  symlink 目标是否存在，不是候选目录项是否存在。
- 只依赖纳秒时间戳保证名称唯一。时钟可被固定，连续恢复、测试注入或恢复历史目录
  都可能产生相同名称。
- 有 suffix 循环但仍使用 `Stat`。如果首个碰撞项是 dangling symlink，循环根本
  不会进入下一个 suffix。

## 解法

用 `os.Lstat` 检查每个候选路径：

- `os.IsNotExist(err)`：目录项确实不存在，可以使用。
- `err == nil`：目录项已占用，无论它是目录、文件还是 dangling symlink，都继续
  尝试 `-1`、`-2` 等 suffix。
- 其它错误：带上下文返回，不能把权限或 I/O 错误当成路径空闲。

quarantine 名称同时使用 Windows-safe 的 UTC 时间格式，不包含冒号。

对应测试应创建占用基础候选名的 dangling symlink，触发损坏恢复，并验证：

- 实际 quarantine 使用 `-1` suffix。
- 原 dangling symlink 未被覆盖。
- fresh overlay 可以正常打开。

## 为什么有效

`Lstat` 检查 symlink 目录项自身，不跟随其目标，因此能准确回答 quarantine
分配逻辑真正关心的问题：“目标名称是否已被任何目录项占用”。

这样可以让名称选择在 Unix 和 Windows 上保持一致，并避免恢复路径依赖
`Rename` 对 symlink 和目录类型碰撞的具体平台语义。

需要注意：`Lstat` 解决的是错误的 symlink 存在性判断，不消除检查与 rename
之间的 TOCTOU。当前 overlay 生命周期锁能够串行化所有合作进程，适合本项目的
本地应用数据目录；若目录允许不受信任的并发写入，则需要额外采用原子占位或
no-replace rename 机制。

## 预防

1. 为 quarantine、backup、rotation 或临时文件选择唯一名称时，先明确判断的是
   “目录项存在”还是“目标对象可访问”；前者默认使用 `Lstat`。
2. 只有明确的 `IsNotExist` 才表示名称空闲，其它 filesystem error 必须返回。
3. 唯一名测试除普通文件和目录碰撞外，还要覆盖 dangling symlink。
4. 不把高精度时间戳当成唯一性保证，始终提供确定性的 suffix fallback。
5. 跨平台恢复逻辑至少做 Windows 交叉编译，并在 Windows CI 中运行真实 rename
   与 symlink 场景。

## 相关文档

- feature：`../features/2026-07-15-ipdb-overlay-store/`
- 实现：`../../backend/ipdb/overlay.go`
- 回归测试：`../../backend/ipdb/overlay_test.go`
