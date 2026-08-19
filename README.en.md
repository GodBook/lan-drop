# ⚡ LAN Drop

**A minimalist LAN transfer station** — a single-file, zero-dependency, cross-platform tool that lets phones, tablets and computers exchange files and sync clipboard text over the same Wi-Fi. No cables, no accounts, no third-party cloud.

**中文文档：[README.md](README.md)**

[![Release](https://img.shields.io/github/v/release/GodBook/lan-drop?color=blue&label=Release)](https://github.com/GodBook/lan-drop/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/GodBook/lan-drop/build.yml?label=CI)](https://github.com/GodBook/lan-drop/actions)
[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Dependencies](https://img.shields.io/badge/Dependencies-0-success)](go.mod)

![Desktop UI](docs/screenshot-desktop.png)

## 🆕 What's new in 1.5.0

- Security hardening: the Android source is now tracked, the exposed signing key is removed, QR PINs are exchanged for random session cookies and cleaned with a `303` redirect, with shared lockout and `no-referrer` protection.
- Safer transfers: 5 MiB chunk caps, total chunk/file-size limits, disk-headroom checks, multipart memory limits and a concurrency gate; uploads support pause, resume, cancel and automatic failed-chunk retries.
- Reliable QR codes: Medium error correction and Versions 1–5 are explicit boundaries, oversized payloads fail clearly, and CI includes an independent scanner decode test.
- Productized desktop app: single-instance behavior, system tray, minimize-to-tray, open-folder/copy-address actions, storage and adapter switching, plus startup-race fixes.
- Better discovery and file management: search, type filters, pagination, batch deletion, exponential WebSocket backoff, mDNS and Android NSD discovery.

Before publishing, configure a completely new Android signing key in GitHub Actions Secrets. The legacy key was exposed; users upgrading from older APKs must uninstall before installing 1.5.0.

## ✨ Highlights

| Feature | Description |
| :--- | :--- |
| 🖥 **Desktop app** | `LAN-Drop-Desktop-windows-x64.exe`: ships with the LAN Drop app icon, single instance, native tray, minimize/close to tray, open receive folder, copy address, and switch adapter or storage directory at runtime |
| 📱 **Android app** | `landrop-android.apk`: discovers computers through NSD, with QR/manual connection and origin-only recent-server history |
| 📦 **Single binary, zero deps** | All web assets embedded at compile time via `//go:embed`; nothing to install on the target machine |
| 📱 **Scan to connect** | Shows a QR code in the Windows desktop app or CLI terminal; point any phone camera at it and you're in |
| 🧠 **Adapter selection** | Filters virtual adapters and lets desktop users select any physical LAN adapter, avoiding unreachable QR addresses on multi-NIC machines |
| 📡 **Zero-input discovery** | CLI/desktop advertise `_landrop._tcp.local.` over mDNS and Android discovers it with NSD; the PIN is never advertised and QR remains the fallback |
| 🔒 **Session-grade security** | Constant-time PIN checks with shared brute-force lockout; QR PINs are exchanged for a random 256-bit session cookie and removed from the URL by `303`, with `Referrer-Policy: no-referrer`; WebSocket Origin checks block CSWSH |
| 🚀 **Bounded chunked upload** | 4 MiB client chunks over 3 streams; the server caps chunks at 5 MiB, 4096 chunks and 20 GiB per file, reserves disk headroom, then merges atomically |
| ⏸ **Transfer controls** | Pause, resume, cancel, and up to 3 automatic retries per failed chunk; re-selecting the same file skips chunks already present, even after a refresh |
| 📶 **Resumable downloads** | Full HTTP `Range` support for multi-threaded acceleration and interrupted-download recovery |
| 💬 **Real-time text broadcast** | Hand-rolled RFC-6455 WebSocket with keepalive pings and oversized-frame protection; instant push to every device, new devices sync the last 50 messages |
| 💾 **Persistent history** | The text feed is persisted to disk and survives restarts |
| 🖼 **Clipboard image paste** | Ctrl+V a screenshot on the PC and it transfers to the phone as an image file |
| 👀 **Inline media preview** | Click to preview images / video / audio; RFC 5987 encoding keeps non-ASCII filenames correct on download |
| 🔎 **File management** | Search, type filters, pagination, select-page and batch delete, backed by a directory index cache that avoids repeated scans when unchanged |
| 🔔 **Desktop notifications** | Background tab still notifies you on new files and messages |
| 🧹 **Self-cleaning** | Aborted-upload temp directories are swept by TTL; failed merges roll back cleanly |
| 🧩 **Stdlib only** | 100% Go standard library on the backend — no supply-chain surface |

## 🚀 Quick Start

Grab a build from [**Releases**](https://github.com/GodBook/lan-drop/releases): **Windows desktop app** (`LAN-Drop-Desktop-windows-x64.exe`, native window with a connection QR dialog), **Android client** (`landrop-android.apk`), or CLI binaries for Windows / macOS / Linux. Alternatively:

```bash
# Docker
docker run -d --name landrop -p 8087:8087 -v landrop-data:/data ghcr.io/godbook/lan-drop:latest

# From source (Go 1.22+)
git clone https://github.com/GodBook/lan-drop.git
cd lan-drop && go build -ldflags="-s -w" -o landrop . && ./landrop
```

Run it and the terminal shows the LAN URL, a PIN, and scannable QR codes; the browser opens automatically on the PC. The Windows desktop app stays in the tray when minimized or closed and exposes connection/storage settings from its tray menu. Android automatically lists nearby LAN Drop services through NSD.

```bash
./landrop              # port 8087, saves to ~/Downloads/LAN_Drop, random PIN
./landrop -p 9090      # custom port (auto-increments if occupied)
./landrop -pin 6688    # fixed PIN
./landrop -no-pin      # disable PIN (trusted home networks only)
./landrop -v           # print version
```

## 🔌 API

Prefer the `landrop_session` cookie minted by `POST /api/auth`. A QR's root-page `?pin=` is validated once and removed with a `303` redirect. Compatibility query PINs and `X-PIN` share the same failure counter and lockout as `/api/auth`, so they cannot bypass brute-force protection.

| Path | Method | Description |
| :--- | :--- | :--- |
| `/api/info` | GET | Hostname, IP, port, storage dir |
| `/api/qr` | GET | Auth-protected connection QR as SVG; `?format=json` returns the URL and PIN |
| `/api/auth` | POST | `{"pin":"1234"}` → session cookie |
| `/api/upload/chunk` | POST | Upload one chunk with `file_id`, index/count, filename, `file_size`, and payload (5 MiB maximum) |
| `/api/upload/status` | GET | Which chunks of a `file_id` already arrived (resume core) |
| `/api/upload/complete` | POST | Cancellable atomic merge with declared `file_size`; missing or inconsistent chunks are rejected |
| `/api/upload/cancel` | POST | Cancel a `file_id`, stop queued chunks/merge, and clean temporary state |
| `/api/files` | GET | List files with `q`, `type`, `page`, and `page_size` filters |
| `/api/files/delete` | POST | Delete one `filename` or up to 100 `filenames` |
| `/api/download/{name}` | GET | Download with `Range` support; `?inline=1` for preview |
| `/api/ws` | GET (WS) | Real-time text channel (Origin checked) |
| `/api/text/send` | POST | HTTP fallback text broadcast (1MB cap) |
| `/api/text/feed` | GET | Last 50 text messages |

## 🛠️ CI/CD

Every push runs gofmt / `go vet` / `go test -race`, then builds all platform binaries. Branch builds produce a debug-signed Android APK. A `v*` tag requires a replacement Android signing key from GitHub Actions Secrets, creates a signed release APK and GitHub Release, and publishes multi-arch Docker images to GHCR. The compromised legacy keystore is no longer stored or used; see [android/签名配置.md](android/签名配置.md).

## 📄 License

[MIT License](LICENSE)
