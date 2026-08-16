# ⚡ LAN Drop（局域网极简跨设备传输站）完整交接文档

---

## 1\. 项目基本信息

* **项目名称**：LAN Drop (局域网极简跨设备传输站)  
* **定位**：轻量级、无依赖、跨平台的局域网文件与剪贴板双向同步传输工具，旨在解决手机、平板与电脑间不依赖任何第三方社交软件或云端账号的数据互传需求。  
* **交付形态**：独立单文件二进制可执行程序（支持 Windows `.exe`、macOS、Linux），内嵌完整 Web 静态资源。  
* **核心技术栈**：  
  * **后端**：Go 1.22+（纯标准库实现，零第三方包依赖）  
  * **前端**：原生 ES6 \+ CSS3 \+ HTML5（自包含，免 npm 构建流程）  
  * **协议**：HTTP/1.1、RFC-6455 WebSocket、HTTP Range 断点续传

---

## 2\. 交付物与源码清单

| 文件 / 目录路径 | 类型 | 说明 |
| :---- | :---- | :---- |
| `main.go` | 核心源码 | 进程入口、CLI 参数解析、IP/端口初始化、静态资源 `embed` 与优雅停机 |
| `go.mod` | 配置 | Go 模块定义文件 (`landrop`) |
| `Makefile` | 脚本 | 多平台交叉编译脚本（支持一键编译 Windows / macOS / Linux） |
| `start.bat` | 脚本 | Windows 用户双击即用启动脚本（自动探测 exe 或 Python 环境） |
| `run_dev_server.py` | 工具 | Python 零依赖即时预览服务（用于无 Go 环境下的功能测试） |
| `.github/workflows/build.yml` | CI/CD | GitHub Actions 自动化云端多平台编译流水线配置 |
| `internal/network/ip.go` | 模块 | 局域网物理/无线网卡智能过滤算法、可用端口自动探测 |
| `internal/qrcode/qrcode.go` | 模块 | 纯 Go 实现的终端 ASCII/Unicode 二维码编码与渲染引擎 |
| `internal/server/config.go` | 模块 | 服务端配置、数据模型、PIN 码生成器与文本流内存环形缓冲区 |
| `internal/server/handler.go` | 模块 | HTTP 路由分发、中间件鉴权、静态资源流映射 |
| `internal/server/upload.go` | 模块 | 4MB 分片接收、临时分块管理、原子流式合并与 Range 下载 |
| `internal/server/ws.go` | 模块 | RFC-6455 WebSocket 握手实现、客户端连接生命周期与组播 Hub |
| `web/index.html` | 前端 | 响应式 Web 页面（支持移动端触控、桌面端全屏拖拽放置与 PIN 弹窗） |
| `web/app.js` | 前端 | 大文件切片上传控制器、WebSocket 文本双向通信与剪贴板 API 封装 |
| `web/style.css` | 前端 | 现代化深色主题、交互悬浮动效、进度条与 Toast 通知组件 |

---

## 3\. 系统架构与核心机制

### 3.1 架构拓扑

               \+-------------------------------------------------------+

               |                  LAN Drop 服务端                      |

               |                                                       |

               |  \[IP/网卡探测\]      \[终端二维码\]      \[安全 PIN 鉴权\] |

               |  \[静态资源 embed\]   \[WebSocket Hub\]   \[分片上传/合并\] |

               \+---------------------------+---------------------------+

                                           | (局域网 HTTP / WebSocket)

                     \+---------------------+---------------------+

                     |                                           |

             \+-------v-------+                           \+-------v-------+

             |   PC 浏览器   |                           |  手机端浏览器 |

             | 全屏拖拽/文本流 |                           | 扫码直连/选相册 |

             \+---------------+                           \+---------------+

### 3.2 关键机制详解

1. **零外部依赖自包含架构**：  
   * 所有前端资源（HTML/CSS/JS/图标）在编译阶段直接通过 `//go:embed web/*` 嵌入至二进制可执行文件中，打包后仅生成单个独立文件，目标机器无需安装任何运行时环境。  
2. **真实局域网 IP 嗅探与终端二维码**：  
   * 自动遍历所有网络接口，过滤 Docker、VMware、VPN 等虚拟网卡，优先获取 `192.168.x.x`、`10.x.x.x` 或 `172.16-31.x.x` 网段的物理 IP；  
   * 通过内置轻量 QR 编码算法，在控制台以 ANSI Unicode 字符块直接输出黑白二维码，手机系统相机或微信扫码直达。  
3. **大文件分片上传与断点续传**：  
   * 前端将任意大小文件通过 `Blob.slice()` 划分为 4MB 的独立分块，携带 `file_id` 与 `chunk_index` 并发流式提交；  
   * 后端将分块写入 `.tmp_<file_id>/` 临时目录，所有分块就绪后触发原子合并并清理暂存区；  
   * 文件下载端支持 HTTP `Range` 头，支持多线程下载加速与意外中断恢复。  
4. **实时文本与剪贴板广播 (WebSocket Hub)**：  
   * 基于 RFC-6455 规范自研底层 WebSocket 握手与数据帧编解码；  
   * 服务端维护全局 Hub 组播队列与容量为 50 条的文本环形缓冲区（Ring Buffer），任何客户端发送文本即刻全网毫秒级推送，新接入设备自动同步最近历史。  
5. **轻量会话安全 (PIN Code)**：  
   * 启动时默认生成 4 位动态 PIN 码并拼装入二维码参数（`?pin=XXXX`），扫码设备免密进入；直接输入 IP 访问的用户需在 Web 端输入 PIN 验证通过后方可建立连接，防止公共 Wi-Fi 环境下的未授权操作。

---

## 4\. API 接口定义规范

| 路径 | 方法 | 说明 | 关键参数 / Payload | 响应示例 |
| :---- | :---- | :---- | :---- | :---- |
| `/api/info` | GET | 获取服务端主机名、IP、端口及存储目录 | 无 | `{"hostname":"MacBook","host_ip":"192.168.1.5","port":8080}` |
| `/api/auth` | POST | 提交 PIN 码完成身份鉴权 | `{"pin":"1234"}` | `{"status":"ok"}` |
| `/api/upload/chunk` | POST | 上传单个文件分片 (Multipart) | `file_id`, `chunk_index`, `total_chunks`, `filename`, `chunk` | `{"status":"ok","chunk_index":0}` |
| `/api/upload/complete` | POST | 触发分片合并与落盘 | `{"file_id":"...","filename":"a.zip","total_chunks":5}` | `{"status":"success","file":{...}}` |
| `/api/files` | GET | 获取当前已接收文件列表 | 无 | `{"status":"ok","files":[{...}]}` |
| `/api/files/delete` | POST | 删除服务端指定文件 | `{"filename":"a.zip"}` | `{"status":"ok"}` |
| `/api/download/{filename}` | GET | 下载指定文件（支持 HTTP Range） | URL 路径参数 | 二进制文件流（带 Content-Disposition 标头） |
| `/api/ws` | GET (WS) | WebSocket 长连接握手端点 | `Sec-WebSocket-Key` | 升级为双向 WebSocket 通信通道 |
| `/api/text/send` | POST | 发送文本广播（HTTP 降级方案） | `{"content":"...","sender":"Device-1"}` | `{"status":"ok","data":{...}}` |
| `/api/text/feed` | GET | 获取历史文本流 | 无 | `{"status":"ok","feed":[{...}]}` |

---

## 5\. 构建、打包与运维指南

### 5.1 本地编译生成二进制

* **Windows 本地编译**：  
    
  cd lan-drop  
    
  go build \-ldflags="-s \-w" \-o landrop.exe .  
    
* **跨平台交叉编译（在任一系统为其他平台打包）**：  
    
  \# 生成 Windows 64位 exe  
    
  GOOS=windows GOARCH=amd64 go build \-ldflags="-s \-w" \-o dist/landrop-windows-amd64.exe .  
    
  \# 生成 macOS Apple Silicon (M1/M2/M3) 原生版本  
    
  GOOS=darwin GOARCH=arm64 go build \-ldflags="-s \-w" \-o dist/landrop-darwin-arm64 .  
    
  \# 生成 Linux x86\_64 版本  
    
  GOOS=linux GOARCH=amd64 go build \-ldflags="-s \-w" \-o dist/landrop-linux-amd64 .  
    
* **使用 Makefile 一键编译全平台**：  
    
  make build-all

### 5.2 命令行启动参数说明

\# 1\. 默认启动（端口8087，存储在 \~/Downloads/LAN\_Drop，随机PIN）

./landrop

\# 2\. 自定义端口与自定义存储目录

./landrop \-p 9090 \-d /data/shared\_files

\# 3\. 指定固定 PIN 码

./landrop \-pin 6688

\# 4\. 完全关闭 PIN 码验证（适用于受信任的家庭内网）

./landrop \-no-pin

### 5.3 自动化云端构建 (GitHub Actions)

项目内置 `.github/workflows/build.yml`：

1. 将源码推送至 GitHub 仓库；  
2. 每次触发 `push` 或手动触发 `workflow_dispatch`，GitHub 会自动拉起 Ubuntu 构建机编译出 Windows/macOS/Linux 全套无依赖二进制，并打包至 Artifacts 供直接下载。

---

## 6\. 常见问题排查 (FAQ)

1. **手机扫码后页面无法打开**：  
   * **原因 1**：手机未连接与电脑相同的 Wi-Fi 网络。  
   * **原因 2**：路由器开启了“AP 隔离（Client Isolation）”，导致局域网内设备互相无法直连，需在路由器后台关闭该设置。  
   * **原因 3**：电脑系统防火墙（如 Windows Defender）拦截了入站连接，首次运行请在弹出的防火墙提示中勾选“允许局域网访问”。  
2. **大文件传输是否会吃满内存？**：  
   * 不会。文件上传采用 4MB 分片独立落地，合并过程使用 Go 的 `io.Copy` 流式管道传输，常驻内存占用通常维持在 15MB\~30MB 之间。  
3. **网页端复制文本未弹出成功提示**：  
   * 部分浏览器（如非 HTTPS 环境下的旧版 Safari）限制了 `navigator.clipboard` 异步写入权限，代码内已实现 `document.execCommand('copy')` 降级兜底方案，确保全平台兼容。

---

## 7\. 后续演进建议

* **局域网 mDNS 服务发现**：可集成 Bonjour / Zeroconf 广播，支持直接输入 `http://landrop.local:8080` 免 IP 访问。  
* **端到端加密（TLS）**：增加一键生成自签名证书选项，提升在开放 Wi-Fi 下的通信私密性。  
* **文件夹层级打包传输**：支持前端拖入整个文件夹结构并在服务端自动归档为 Zip 压缩包。

