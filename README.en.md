# ⚡ LAN Drop

**A minimalist LAN transfer station** — a single-file, zero-dependency, cross-platform tool that lets phones, tablets and computers exchange files and sync clipboard text over the same Wi-Fi. No cables, no accounts, no third-party cloud.

**中文文档：[README.md](README.md)**

[![Release](https://img.shields.io/github/v/release/GodBook/lan-drop?color=blue&label=Release)](https://github.com/GodBook/lan-drop/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/GodBook/lan-drop/build.yml?label=CI)](https://github.com/GodBook/lan-drop/actions)
[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Dependencies](https://img.shields.io/badge/Dependencies-0-success)](go.mod)

![Desktop UI](docs/screenshot-desktop.png)

## ✨ Highlights

| Feature | Description |
| :--- | :--- |
| 📦 **Single binary, zero deps** | All web assets embedded at compile time via `//go:embed`; nothing to install on the target machine |
| 📱 **Scan to connect** | Renders a QR code in the terminal (one per NIC); point any phone camera at it and you're in |
| 🧠 **Smart NIC detection** | Filters Docker / VMware / WSL / Hyper-V / VPN virtual adapters and prefers real physical LAN IPs |
| 🔒 **Session-grade security** | Dynamic 4-digit PIN with constant-time comparison and lockout after repeated failures; login mints a random 256-bit session token (the PIN itself is never a cookie); WebSocket Origin checking blocks CSWSH |
| 🚀 **Parallel chunked upload** | 4MB chunks, 3 concurrent streams, temp-spill + atomic merge on the server; steady-state memory 15–30MB for any file size |
| ⏸ **Upload resume** | Re-select the same file after an interruption and already-uploaded chunks are skipped (`/api/upload/status` + client-side fingerprint memory), even across page refreshes |
| 📶 **Resumable downloads** | Full HTTP `Range` support for multi-threaded acceleration and interrupted-download recovery |
| 💬 **Real-time text broadcast** | Hand-rolled RFC-6455 WebSocket with keepalive pings and oversized-frame protection; instant push to every device, new devices sync the last 50 messages |
| 💾 **Persistent history** | The text feed is persisted to disk and survives restarts |
| 🖼 **Clipboard image paste** | Ctrl+V a screenshot on the PC and it transfers to the phone as an image file |
| 👀 **Inline media preview** | Click to preview images / video / audio; RFC 5987 encoding keeps non-ASCII filenames correct on download |
| 🔔 **Desktop notifications** | Background tab still notifies you on new files and messages |
| 🧹 **Self-cleaning** | Aborted-upload temp directories are swept by TTL; failed merges roll back cleanly |
| 🧩 **Stdlib only** | 100% Go standard library on the backend — no supply-chain surface |

## 🚀 Quick Start

Grab a binary from [**Releases**](https://github.com/GodBook/lan-drop/releases) (Windows / macOS Intel & Apple Silicon / Linux x64 & arm64), or:

```bash
# Docker
docker run -d --name landrop -p 8087:8087 -v landrop-data:/data ghcr.io/godbook/lan-drop:latest

# From source (Go 1.22+)
git clone https://github.com/GodBook/lan-drop.git
cd lan-drop && go build -ldflags="-s -w" -o landrop . && ./landrop
```

Run it and the terminal shows the LAN URL, a PIN, and scannable QR codes; the browser opens automatically on the PC.

```bash
./landrop              # port 8087, saves to ~/Downloads/LAN_Drop, random PIN
./landrop -p 9090      # custom port (auto-increments if occupied)
./landrop -pin 6688    # fixed PIN
./landrop -no-pin      # disable PIN (trusted home networks only)
./landrop -v           # print version
```

## 🔌 API

Authentication via `?pin=` query param, `X-PIN` header, or the `landrop_session` cookie minted by `POST /api/auth` (5 consecutive failures lock the source IP for 30s).

| Path | Method | Description |
| :--- | :--- | :--- |
| `/api/info` | GET | Hostname, IP, port, storage dir |
| `/api/auth` | POST | `{"pin":"1234"}` → session cookie |
| `/api/upload/chunk` | POST | Upload one 4MB chunk (multipart) |
| `/api/upload/status` | GET | Which chunks of a `file_id` already arrived (resume core) |
| `/api/upload/complete` | POST | Atomic merge; missing chunks are rejected with rollback |
| `/api/files` | GET / POST | List / delete received files |
| `/api/download/{name}` | GET | Download with `Range` support; `?inline=1` for preview |
| `/api/ws` | GET (WS) | Real-time text channel (Origin checked) |
| `/api/text/send` | POST | HTTP fallback text broadcast (1MB cap) |
| `/api/text/feed` | GET | Last 50 text messages |

## 🛠️ CI/CD

Every push runs gofmt / `go vet` / `go test -race`, then builds all platform binaries. Pushing a `v*` tag creates a GitHub Release with all binaries and publishes multi-arch (amd64/arm64) Docker images to GHCR.

## 📄 License

[MIT License](LICENSE)
