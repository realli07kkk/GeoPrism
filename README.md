# GeoPrism

GeoPrism 是一个 macOS 本地运行的 DNS 查询工具，基于 Wails v2（Go + React + TypeScript）开发。

## 当前实现状态

- 已完成（M1）：域名查询（A/AAAA/CNAME/TXT/NS/MX）
- 已完成（M1）：多 Provider 并行查询
- 已完成（M1）：DoH / DoT / DNS(UDP) 支持
- 已完成（M1）：Provider 管理（CRUD + 默认模板）
- 待实现：Provider 导入/导出
- 待实现：IP 库解析与下载更新（M2/M3）
- 待实现：历史与导出、日志诊断（M4）

## 技术栈

- Go 1.23
- Wails v2.11.0
- React 18 + TypeScript + Vite

## 开发命令

```bash
# 启动（Wails 开发模式）
export PATH=$PATH:$(go env GOPATH)/bin
wails dev

# 构建（Wails 打包）
export PATH=$PATH:$(go env GOPATH)/bin
wails build
```

```bash
# 前端单独构建
cd frontend
npm install
npm run build
```

## 目录说明

```text
backend/
  provider/   # Provider 配置管理
  resolver/   # DoH/DoT/DNS 查询
  ipdb/       # 预留（M2）
  updater/    # 预留（M3）
  storage/    # 预留（M4）
frontend/src/
  pages/ components/ hooks/ styles/
```
