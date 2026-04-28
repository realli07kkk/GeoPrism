---
doc_type: requirement
slug: ptr-reverse-lookup
pitch: 敲一个 IP 就能反查它对应的域名，支持 IPv4 和 IPv6
status: current
last_reviewed: 2026-04-24
implemented_by:
  - arch-resolver
tags: [ptr, reverse-dns, ip]
---

# IP 反向解析

## 用户故事

- 作为一个在日志里看到可疑 IP 的运维，我想反查这个 IP 对应什么域名，而不是手动拼 PTR 记录再去 dig。
- 作为一个想把反向查询集成到脚本里的开发，我希望一个命令就输出结构化结果，而不是从 dig 输出里 grep 再 cut。

## 为什么需要

正向查（域名→IP）有无数工具，反向查（IP→域名）虽然 DNS 协议原生支持 PTR 记录，但命令行操作很别扭——记 in-addr.arpa 格式、拼反向 IP、读 dig 输出。这个能力把反向解析降到和正向查询一样简单：敲 IP 加个 `-x` 就行。

## 怎么解决

用户对某个 IP 加 `-x` 参数，工具自动构造 PTR 查询、向已配置的 Provider 发起请求，把返回的域名列出来。支持 IPv4 和 IPv6 两种地址族。

## 边界

- 只有 DNS 服务器配置了 PTR 记录才能查到，工具不保证一定返回结果。
- 不递归反向解析——查到域名后不会自动再正向查域名对应的 IP 做交叉验证。
- 需要已配置 Provider，不内置反向 DNS 服务器。
