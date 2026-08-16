# ⚡ LAN Drop

**局域网极简跨设备传输站** —— 一个单文件、零依赖的跨平台工具，让手机、平板与电脑在同一 Wi-Fi 下互传文件、同步剪贴板文本，无需数据线、无需登录任何账号、不经任何第三方云端。

[![Release](https://img.shields.io/github/v/release/GodBook/lan-drop?color=blue&label=Release)](https://github.com/GodBook/lan-drop/releases)
[![Go Version](https://img.shields.io/badge/Go-1.21%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey)](https://github.com/GodBook/lan-drop/releases)
[![Dependencies](https://img.shields.io/badge/Dependencies-0-success)](go.mod)

---

## ✨ 核心特性

| 特性 | 说明 |
| :--- | :--- |
| 📦 **单文件零依赖** | 所有 Web 资源（HTML/CSS/JS）在编译期通过 `//go:embed` 内嵌进二进制，目标机器无需安装任何运行时，下载即用 |
| 📱 **扫码直连** | 启动后在终端渲染二维码，手机系统相机 / 微信扫码即达，无需手动输入 IP |
| 🧠 **智能网卡探测** | 自动遍历网络接口，过滤 Docker / VMware / VPN 虚拟网卡，优先返回真实物理局域网 IP |
| 🔒 **PIN 码鉴权** | 默认生成 4 位动态 PIN 并拼入二维码参数，扫码免密进入；手动输入 IP 的访客需通过验证，防止公共 Wi-Fi 下的未授权操作 |
| 🚀 **大文件分片上传** | 前端按 4MB 分块并发流式上传，服务端临时分块落盘 + 原子合并，任意大小文件常驻内存仅 15~30MB |
| 📶 **断点续传下载** | 下载端完整支持 HTTP `Range` 请求，支持多线程加速与意外中断恢复 |
| 💬 **实时文本广播** | 基于 RFC-6455 自研 WebSocket 实现，任何设备发送文本即刻全网毫秒级推送，新设备接入自动同步最近 50 条历史 |
| 🧩 **纯标准库** | 后端 100% Go 标准库（零第三方包），供应链安全，`go build` 一把梭 |

## 🚀 快速开始

### 方式一：直接下载（推荐普通用户）

前往 [**Releases**](https://github.com/GodBook/lan-drop/releases) 页面，下载对应平台的可执行文件：

| 平台 | 文件 |
| :--- | :--- |
| Windows x64 | `landrop-windows-amd64.exe` |
| macOS Apple Silicon (M1/M2/M3/M4) | `landrop-darwin-arm64` |
| macOS Intel | `landrop-darwin-amd64` |
| Linux x64 | `landrop-linux-amd64` |

Windows 用户双击 `landrop-windows-amd64.exe`（或仓库自带的 `start.bat`）；macOS / Linux 用户在终端运行：

```bash
chmod +x landrop-darwin-arm64   # 仅首次需要
./landrop-darwin-arm64
```

> macOS 首次运行若提示"无法验证开发者"，请执行 `xattr -d com.apple.quarantine landrop-darwin-arm64` 后重试。

### 方式二：从源码编译

```bash
git clone https://github.com/GodBook/lan-drop.git
cd lan-drop
go build -ldflags="-s -w" -o landrop .
./landrop
```

要求 Go 1.21 及以上，无任何第三方依赖需要拉取。

## 📖 使用说明

### 启动

```bash
# 1. 默认启动（端口 8087，文件存至 ~/Downloads/LAN_Drop，随机 PIN）
./landrop

# 2. 自定义端口与存储目录
./landrop -p 9090 -d /data/shared_files

# 3. 固定 PIN 码
./landrop -pin 6688

# 4. 关闭 PIN 验证（仅建议受信任的家庭内网）
./landrop -no-pin
```

启动后终端会输出访问地址、PIN 码与二维码：

```
==================================================================
   ⚡ LAN Drop v1.0.0 - 局域网极简跨设备文件与文本极速快传站
==================================================================
 🌐 局域网访问地址 : http://192.168.1.5:8087/?pin=8579
 🔒 访问 PIN 码    : 8579 (扫码可直接免密进入)
 📂 文件存储目录   : /Users/you/Downloads/LAN_Drop
------------------------------------------------------------------
 📱 手机扫码直达 (用微信/系统相机扫描下方二维码)：
 ██████████████████
 ...
==================================================================
```

### 命令行参数

| 参数 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `-p` | `8087` | 服务端口（被占用时自动递增寻找可用端口） |
| `-d` | `~/Downloads/LAN_Drop` | 接收文件的存储目录 |
| `-pin` | 随机 4 位 | 固定访问 PIN 码 |
| `-no-pin` | 关闭 | 关闭 PIN 身份验证 |

### 传输流程

1. 电脑启动 `landrop`，终端出现二维码；
2. 手机连接**同一 Wi-Fi**，扫码打开网页（扫码自动免 PIN）；
3. 手机选相册文件 / 拖入任意文件 → 自动分片上传至电脑存储目录；
4. 任意设备在文本框输入内容 → 所有在线设备实时同步，一键复制。

## 🔌 API 一览

前端页面之外，服务端同时暴露完整的 REST / WebSocket 接口，可被脚本或第三方客户端调用（除 `/api/info`、`/api/auth` 外均需 PIN，通过 `?pin=` 查询参数、`X-PIN` 请求头或 Cookie 传递）：

| 路径 | 方法 | 说明 |
| :--- | :--- | :--- |
| `/api/info` | GET | 获取主机名、IP、端口与存储目录 |
| `/api/auth` | POST | 提交 `{"pin":"1234"}` 完成鉴权并下发 Cookie |
| `/api/upload/chunk` | POST | 上传单个 4MB 分片（Multipart: `file_id` / `chunk_index` / `total_chunks` / `filename` / `chunk`） |
| `/api/upload/complete` | POST | 触发分片原子合并落盘 |
| `/api/files` | GET | 获取已接收文件列表 |
| `/api/files/delete` | POST | 删除服务端指定文件 |
| `/api/download/{filename}` | GET | 下载文件（支持 `Range` 断点续传） |
| `/api/ws` | GET (WS) | WebSocket 长连接（文本实时广播通道） |
| `/api/text/send` | POST | 发送文本广播（WebSocket 的 HTTP 降级方案） |
| `/api/text/feed` | GET | 获取最近 50 条历史文本 |

## 🏗️ 项目结构

```
lan-drop/
├── main.go                      # 进程入口：CLI 参数、IP/端口初始化、embed 静态资源、优雅停机
├── go.mod                       # 模块定义（零第三方依赖）
├── Makefile                     # 多平台交叉编译一键脚本 (make build-all)
├── start.bat                    # Windows 双击启动脚本
├── run_dev_server.py            # 无 Go 环境时的 Python 零依赖预览服务
├── internal/
│   ├── network/ip.go            # 局域网物理网卡智能过滤、可用端口自动探测
│   ├── qrcode/qrcode.go         # 纯 Go 实现的终端二维码编码与渲染引擎
│   └── server/
│       ├── config.go            # 配置、数据模型、PIN 生成器、文本环形缓冲区
│       ├── handler.go           # HTTP 路由分发、PIN 鉴权中间件
│       ├── upload.go            # 分片接收、临时分块管理、原子合并、Range 下载
│       └── ws.go                # RFC-6455 WebSocket 握手/帧编解码、连接生命周期与组播 Hub
└── web/
    ├── index.html               # 响应式页面（移动端触控 / 桌面全屏拖拽 / PIN 弹窗）
    ├── app.js                   # 分片上传控制器、WebSocket 通信、剪贴板封装
    └── style.css                # 深色主题、进度条与 Toast 组件
```

## 🛠️ 构建与发布

```bash
# 本平台编译
go build -ldflags="-s -w" -o landrop .

# 交叉编译（任一系统上为其他平台打包）
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dist/landrop-windows-amd64.exe .
GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o dist/landrop-darwin-arm64 .
GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o dist/landrop-linux-amd64 .

# Makefile 一键全平台（含 macOS Intel 与 Linux arm64）
make build-all
```

仓库内置 **GitHub Actions** 流水线（`.github/workflows/build.yml`）：

- 推送到 `main` 自动编译全平台二进制并上传 Artifacts；
- 推送 `v*` 标签（如 `v1.0.0`）自动创建 GitHub Release 并附带全部二进制。

## ❓ 常见问题

**手机扫码后页面打不开？**
- 确认手机与电脑连接同一 Wi-Fi；
- 路由器若开启"AP 隔离（Client Isolation）"，局域网设备互相不可达，需在路由器后台关闭；
- 首次运行时 Windows 防火墙弹窗请勾选"允许"，否则会拦截入站连接。

**大文件传输会吃满内存吗？**
不会。上传按 4MB 分片独立落盘，合并使用 `io.Copy` 流式管道，常驻内存约 15~30MB。

**网页端点"复制"没反应？**
部分浏览器（如非 HTTPS 的旧版 Safari）限制 `navigator.clipboard`，前端已实现 `document.execCommand('copy')` 降级兜底。

## 🗺️ Roadmap

- [ ] mDNS / Bonjour 服务发现，支持 `http://landrop.local` 免 IP 访问
- [ ] 一键自签名 TLS，提升开放 Wi-Fi 下的通信私密性
- [ ] 前端拖入整个文件夹，服务端自动归档为 Zip

## 📄 License

本项目基于 [MIT License](LICENSE) 开源。
