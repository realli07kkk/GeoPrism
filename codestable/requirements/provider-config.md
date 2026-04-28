---
doc_type: requirement
slug: provider-config
pitch: 用 TOML 文件管理 DNS 服务器列表，一行命令就能测试哪些可用
status: current
last_reviewed: 2026-04-24
implemented_by:
  - arch-provider
tags: [provider, config, dns]
---

# Provider 配置管理

## 用户故事

- 作为一个想用自己的 DNS 服务器的用户，我希望改一个配置文件就能切换 Provider，而不是重新编译或者记命令行参数。
- 作为一个配了好几个 Provider 不确定哪些还能用的人，我希望一行命令就能跑连通性测试，而不是自己挨个 dig 试。
- 作为一个想把配置分享给团队的人，我希望配置文件格式是人可读的文本，可以直接进版本控制。

## 为什么需要

DNS 查询工具的价值依赖上游 Provider 的可用性。Provider 地址会变、会失效、会新增。把 Provider 管理硬编码在命令行参数里会让每次调用都带上大量重复信息，而且没法复用。这个能力把 Provider 的增删改查和连通性验证独立出来，让查询命令保持简洁。

## 怎么解决

用户在 TOML 配置文件里声明 Provider（地址、协议类型），工具启动时加载。`providers` 子命令列出当前所有 Provider；`test` 子命令按名称或全量测试连通性。首次启动时如果配置文件不存在，工具自动生成一份带默认 Provider 的模板。

## 边界

- 不提供 Provider 的导入/导出功能（当前版本）。
- 不检测 Provider 返回数据的正确性——连通性测试只验证网络可达，不验证 DNS 应答内容。
- TOML 格式是唯一支持的配置格式，旧的 JSON 配置不再读取也不自动迁移。
