# ⚡ LAN Drop

**局域网极简跨设备传输站** —— 一个单文件、零依赖的跨平台工具，让手机、平板与电脑在同一 Wi-Fi 下互传文件、同步剪贴板文本，无需数据线、无需登录任何账号、不经任何第三方云端。

**English documentation: [README.en.md](README.en.md)**

[![Release](https://img.shields.io/github/v/release/GodBook/lan-drop?color=blue&label=Release)](https://github.com/GodBook/lan-drop/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/GodBook/lan-drop/build.yml?label=CI)](https://github.com/GodBook/lan-drop/actions)
[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey)](https://github.com/GodBook/lan-drop/releases)
[![Dependencies](https://img.shields.io/badge/Dependencies-0-success)](go.mod)

![Desktop UI](docs/screenshot-desktop.png)

---

## 🆕 1.5.0 更新说明

- 安全加固：Android 主代码正式纳入 Git，移除公开签名密钥；扫码 PIN 通过 `303` 清理并改用随机会话 Cookie，统一失败限流和 `no-referrer`。
- 上传保护：单片限制 5 MiB，增加总分片数、总文件大小、磁盘余量、multipart 内存阈值和并发请求闸门，支持暂停、继续、取消与失败分片自动重试。
- 二维码可靠性：明确限制 Medium / Version 1–5，超出容量显式报错，并加入独立扫码器真实解码测试。
- 桌面产品化：单实例、系统托盘、最小化到托盘、打开目录、复制地址、存储目录和多网卡切换；修复启动竞态。
- 文件与连接体验：搜索、类型筛选、分页、批量删除、WebSocket 指数退避，以及 mDNS/Android NSD 自动发现。

发布时请在 GitHub Actions Secrets 配置全新的 Android 签名密钥；旧签名已经公开，旧版用户升级到 1.5.0 前需要卸载旧 APK。

### 1.5.1 补丁

- 修复 Windows 桌面端和命令行 EXE 缺少应用图标的问题。
- 系统托盘、任务栏、窗口和文件管理器现在使用统一的 LAN Drop 专属图标。

## ✨ 核心特性

| 特性 | 说明 |
| :--- | :--- |
| 🖥 **桌面应用** | Windows 端 `LAN-Drop-Desktop.exe`：带 LAN Drop 专属应用图标，单实例运行、原生系统托盘、最小化/关闭到托盘，可打开接收目录、复制地址并动态切换网卡和存储目录 |
| 📱 **安卓应用** | `landrop-android.apk`：通过 NSD 自动发现电脑，也可扫码 / 输 IP 直连；记住常用服务器，支持系统文件选择器与下载管理 |
| 📦 **单文件零依赖** | 服务端核心 100% Go 标准库；Web 资源编译期内嵌，CLI 二进制下载即用 |
| 📱 **扫码直连** | Windows 桌面端弹窗或命令行终端显示二维码，手机系统相机 / 微信 / LAN Drop App 扫码即达 |
| 🧠 **多网卡选择** | 自动过滤 Docker / VMware / WSL / Hyper-V / VPN 等虚拟网卡；桌面端列出全部物理网卡，可随时切换实际可达的连接地址 |
| 📡 **无感发现** | CLI/桌面端通过 mDNS 广播 `_landrop._tcp.local.`，Android 使用 NSD 自动发现；广播不携带 PIN，二维码仍是可靠兜底 |
| 🔒 **会话级安全** | 动态 4 位 PIN + 常数时间比较 + 统一失败锁定（防穷举）；扫码 PIN 换取随机 256 位会话令牌后立即 `303` 清理地址栏，并设置 `Referrer-Policy: no-referrer`；WebSocket 校验 Origin 防 CSWSH |
| 🚀 **受限分片上传** | 前端按 4 MiB 分块、3 路并发；服务端硬限制单片 5 MiB、最多 4096 片、单文件 20 GiB，并检查磁盘余量后原子合并 |
| ⏸ **完整传输控制** | 上传可暂停、继续、取消；失败分片最多自动重试 3 次，重新选择同一文件时会跳过已到货分片，刷新页面也可续传 |
| 📶 **断点续传下载** | 下载端完整支持 HTTP `Range` 请求，支持多线程加速与意外中断恢复 |
| 💬 **实时文本广播** | 基于 RFC-6455 自研 WebSocket（含心跳保活与超大帧防护），任何设备发送文本即刻全网推送，新设备自动同步最近 50 条历史 |
| 💾 **历史落盘** | 文本流持久化到磁盘，重启服务不丢历史 |
| 🖼 **剪贴板图片直传** | PC 端直接 Ctrl+V 粘贴截图，自动作为图片文件传输到手机 |
| 👀 **媒体在线预览** | 图片 / 视频 / 音频点击即可在页面内预览（RFC 5987 编码保证中文名下载不乱码） |
| 🔎 **文件管理** | 搜索、类型筛选、分页、全选本页和批量删除；服务端使用目录索引缓存，目录未变化时避免重复扫描与排序 |
| 🔔 **桌面通知** | 页面在后台时，收到新文件 / 新文本弹出系统通知 |
| 🧹 **自动清理** | 中断上传的临时分块目录按 TTL 自动清扫；合并失败自动回滚不留半截文件 |
| 🧩 **纯标准库** | 后端 100% Go 标准库（零第三方包），供应链安全，`go build` 一把梭 |

## 🚀 快速开始

### 方式一：直接下载（推荐普通用户）

前往 [**Releases**](https://github.com/GodBook/lan-drop/releases) 页面，下载对应平台的可执行文件：

| 平台 | 文件 | 说明 |
| :--- | :--- | :--- |
| Windows 桌面应用 | `LAN-Drop-Desktop-windows-x64.exe` | 原生窗口软件，推荐普通用户 |
| Windows 服务端 | `landrop-windows-amd64.exe` | 命令行版（终端二维码/PIN 显示） |
| Android | `landrop-android.apk` | 安卓客户端，安装后扫码/输 IP 直连 |
| macOS Apple Silicon (M1/M2/M3/M4) | `landrop-darwin-arm64` | 命令行版 |
| macOS Intel | `landrop-darwin-amd64` | 命令行版 |
| Linux x64 / arm64 (树莓派) | `landrop-linux-amd64` / `landrop-linux-arm64` | 命令行版 |

**Windows 用户**：普通用户直接双击 `LAN-Drop-Desktop-windows-x64.exe`——原生窗口打开即是操作界面（需要 Windows 10/11 自带的 WebView2 运行时，系统默认已内置）。程序保持单实例运行；最小化或关闭窗口会进入系统托盘，在托盘菜单中可重新打开窗口、打开接收目录、复制连接地址、切换网卡/存储目录或彻底退出。需要终端模式时使用命令行版 `landrop-windows-amd64.exe`（或 `start.bat`）。

**安卓用户**：下载 `landrop-android.apk` 安装（需在系统里允许"安装未知来源应用"）。打开后会自动列出同一局域网内通过 NSD 发现的电脑，也可扫码或输入 `IP:端口` 连接；最近记录只保存服务器 origin，不保存扫码 PIN。

**macOS / Linux 用户**（命令行版）在终端运行：

```bash
chmod +x landrop-darwin-arm64   # 仅首次需要
./landrop-darwin-arm64
```

> macOS 首次运行若提示"无法验证开发者"，请执行 `xattr -d com.apple.quarantine landrop-darwin-arm64` 后重试。

### 方式二：Docker

```bash
docker run -d --name landrop \
  -p 8087:8087 \
  -v landrop-data:/data \
  ghcr.io/godbook/lan-drop:latest
```

镜像基于 Alpine、以非 root 用户运行，接收文件落在卷 `landrop-data`。也可以用 `docker build -t landrop .` 自行构建。

### 方式三：从源码编译

```bash
git clone https://github.com/GodBook/lan-drop.git
cd lan-drop
go build -ldflags="-s -w" -o landrop .
./landrop
```

要求 Go 1.22 及以上，无任何第三方依赖需要拉取。

## 📖 使用说明

### 启动

```bash
# 1. 默认启动（端口 8087，文件存至 ~/Downloads/LAN_Drop，随机 PIN，自动打开浏览器）
./landrop

# 2. 自定义端口与存储目录
./landrop -p 9090 -d /data/shared_files

# 3. 固定 PIN 码 / 关闭 PIN（仅建议受信任的家庭内网）
./landrop -pin 6688
./landrop -no-pin

# 4. 查看版本
./landrop -v
```

启动后终端会输出访问地址、PIN 码与二维码，PC 浏览器自动打开本机页面：

```
==================================================================
   ⚡ LAN Drop v1.5.1 - 局域网极简跨设备文件与文本极速快传站
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
| `-no-browser` | 关闭 | 启动时不自动打开浏览器 |
| `-v` | — | 打印版本号并退出 |

### 传输流程

1. Windows 桌面端可直接从 Android 自动发现列表连接，也可点击顶部“连接二维码”；命令行版在终端显示二维码；
2. 手机连接**同一 Wi-Fi**，扫码 PIN 只用于一次性换取随机会话 Cookie，随后地址栏会立即跳转到不含 PIN 的干净地址；
3. 手机选相册文件 / 拖入任意文件 / PC 直接 Ctrl+V 粘贴截图 → 分片并发上传，可暂停、继续或取消；
4. 任意设备在文本框输入内容 → 所有在线设备实时同步，一键复制；
5. 页面切到后台时，新文件 / 新文本自动弹系统通知。

![Mobile UI](docs/screenshot-mobile.png)

## 🔌 API 一览

前端页面之外，服务端同时暴露 REST / WebSocket 接口。推荐调用 `/api/auth` 换取 `landrop_session` Cookie；二维码中的根页面 `?pin=` 会在校验后立即通过 `303` 清除。兼容的 `X-PIN` 和查询 PIN 与 `/api/auth` 共用失败计数及锁定，不能绕过防穷举限制。

| 路径 | 方法 | 说明 |
| :--- | :--- | :--- |
| `/api/info` | GET | 获取主机名、IP、端口与存储目录 |
| `/api/qr` | GET | 获取受鉴权保护的连接二维码 SVG；`?format=json` 返回连接地址与 PIN |
| `/api/auth` | POST | 提交 `{"pin":"1234"}` 完成鉴权并下发会话 Cookie（连续失败 5 次锁定 30 秒） |
| `/api/upload/chunk` | POST | 上传单个分片（Multipart: `file_id` / `chunk_index` / `total_chunks` / `filename` / `file_size` / `chunk`；单片最大 5 MiB） |
| `/api/upload/status` | GET | 查询 `file_id` 已到货的分片列表（断点续传核心） |
| `/api/upload/complete` | POST | 传入 `file_id` / `filename` / `total_chunks` / `file_size`，触发可取消的原子合并 |
| `/api/upload/cancel` | POST | 取消 `file_id`，中止排队分片或进行中的合并并清理临时状态 |
| `/api/files` | GET | 文件列表；支持 `q`、`type`、`page`、`page_size` 搜索、筛选与分页 |
| `/api/files/delete` | POST | 删除单个 `filename` 或最多 100 个 `filenames` |
| `/api/download/{filename}` | GET | 下载文件（支持 `Range` 断点续传；`?inline=1` 切换为内联预览） |
| `/api/ws` | GET (WS) | WebSocket 长连接（文本实时广播通道，校验 Origin） |
| `/api/text/send` | POST | 发送文本广播（WebSocket 的 HTTP 降级方案，上限 1MB） |
| `/api/text/feed` | GET | 获取最近 50 条历史文本 |

## 🏗️ 项目结构

```
lan-drop/
├── main.go                      # CLI 服务端入口：多网卡二维码、自动开浏览器、优雅停机、临时目录清扫
├── go.mod                       # 根模块（零第三方依赖）
├── Dockerfile                   # 多阶段容器构建（Alpine + 非 root）
├── Makefile                     # 多平台交叉编译一键脚本 (make build-all)
├── start.bat                    # Windows 双击启动脚本
├── core/                        # 服务端核心库（CLI 与桌面应用共用）
│   ├── console/                 # Windows 终端 ANSI/VT 支持
│   ├── network/ip.go            # 局域网物理网卡智能过滤、可用端口自动探测
│   ├── discovery/               # mDNS/Bonjour 服务公告与离线 goodbye
│   ├── qrcode/qrcode.go         # Medium、Version 1-5 的终端 / SVG 二维码编码与渲染
│   └── server/
│       ├── config.go            # 配置、会话令牌、PIN 常数时间比较、限速、文本环形缓冲与落盘
│       ├── handler.go           # HTTP 路由、鉴权中间件、静态资源 ETag 缓存
│       ├── upload.go            # 分片接收、续传状态、原子合并回滚、Range 下载、TTL 清扫
│       ├── ws.go                # RFC-6455 WebSocket：帧校验、心跳、Origin 检查、优雅停机
│       └── *_test.go            # 单元测试 + httptest 集成测试
├── webui/                       # 嵌入式前端资源单一来源（所有交付形态共享）
│   └── assets/{index.html, app.js, style.css}
├── desktop/                     # Windows 桌面应用（WebView2 + 原生托盘与设置）
│   ├── main.go                  # 单实例服务、窗口生命周期与动态设置
│   └── *_windows.go             # 托盘、网卡选择、设置持久化、单实例互斥体
└── android/                     # 安卓客户端（Gradle 工程，CI 云端打包 APK）
    ├── 签名配置.md              # 新发布密钥与 CI Secrets 配置说明
    └── app/                     # WebView 壳：NSD 发现、扫码/手动连接、最近服务器、文件选择与下载
```

## 🛠️ 构建与发布

```bash
# 本平台编译
go build -ldflags="-s -w -X main.AppVersion=1.5.1" -o landrop .

# Makefile 一键全平台（版本号自动注入）
make build-all
```

仓库内置 **GitHub Actions** 流水线（`.github/workflows/build.yml`）：

1. 每次推送先跑 **gofmt 检查 + go vet + go test -race**；
2. 通过后并行编译：全平台 CLI 二进制、**Windows 桌面应用**、**Android APK**；普通分支生成 debug APK，只有 `v*` 标签使用 Secrets 中的新密钥签名 release APK；
3. 推送 `v*` 标签自动创建 GitHub Release 附带以上全部产物，并构建 **linux/amd64 + linux/arm64** Docker 镜像发布到 GHCR。

### 桌面端单独编译

```bash
cd desktop
go build -ldflags="-s -w -H windowsgui -X main.AppVersion=1.5.1" -o LAN-Drop-Desktop.exe .
```

### 安卓端单独编译

需要 JDK 17 与 Android SDK。无需签名配置可运行 `gradle -p android testDebugUnitTest assembleDebug`；release 构建必须通过 `LANDROP_KEYSTORE_PATH`、`LANDROP_KEYSTORE_PASSWORD`、`LANDROP_KEY_ALIAS`、`LANDROP_KEY_PASSWORD` 提供新密钥。仓库不保存发布密钥，完整步骤见 [android/签名配置.md](android/签名配置.md)。旧密钥已经公开，不能继续用于正式分发；更换签名后，已安装旧签名版本的用户需要卸载重装。

## ❓ 常见问题

**手机扫码后页面打不开？**
- 确认手机与电脑连接同一 Wi-Fi；
- 路由器若开启"AP 隔离（Client Isolation）"，局域网设备互相不可达，需在路由器后台关闭；
- 首次运行时 Windows 防火墙弹窗请勾选"允许"，否则会拦截入站连接；
- 多网卡环境下，桌面端可在连接设置中切换物理网卡；命令行版会为每个候选网卡各输出一个二维码。

**大文件传输会吃满内存吗？**
不会。上传按 4 MiB 分片独立落盘，合并使用 `io.Copy` 流式管道，常驻内存约 15~30MB。

**上传到一半断了要重传吗？**
不用。重新选择同一个文件（文件名、大小、修改时间一致）会自动复用会话，已传分片跳过，只补缺失部分。

**网页端点"复制"没反应？**
部分浏览器（如非 HTTPS 的旧版 Safari）限制 `navigator.clipboard`，前端已实现 `document.execCommand('copy')` 降级兜底。

## 🗺️ Roadmap

- [ ] 一键自签名 TLS，提升开放 Wi-Fi 下的通信私密性
- [ ] 前端拖入整个文件夹，服务端自动归档为 Zip

## 📄 License

本项目基于 [MIT License](LICENSE) 开源。
