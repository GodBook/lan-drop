package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"landrop/core/console"
	"landrop/core/network"
	"landrop/core/qrcode"
	"landrop/core/server"
	"landrop/webui"
)

// AppVersion is the current release version; CI overrides it at build time
// via -ldflags "-X main.AppVersion=<version>".
var AppVersion = "1.4.0"

// maxQRCodes caps how many network interfaces get a scannable code on startup.
const maxQRCodes = 3

// tempChunkMaxAge and the sweeper interval control cleanup of aborted uploads.
const (
	tempChunkMaxAge   = 2 * time.Hour
	tempSweepInterval = 30 * time.Minute
)

func main() {
	portFlag := flag.Int("p", 8087, "Service port (default: 8087)")
	dirFlag := flag.String("d", "", "Directory to save received files (default: ~/Downloads/LAN_Drop)")
	pinFlag := flag.String("pin", "", "Custom PIN access code (default: randomly generated 4-digit)")
	noPinFlag := flag.Bool("no-pin", false, "Disable PIN authentication")
	noBrowserFlag := flag.Bool("no-browser", false, "Do not auto-open the browser on start")
	versionFlag := flag.Bool("v", false, "Print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("LAN Drop v%s (%s/%s)\n", AppVersion, runtime.GOOS, runtime.GOARCH)
		return
	}

	console.EnableANSI()

	// 1. Detect Network LAN IPs (grouped: physical adapters are phone-reachable,
	// virtual ones like WSL are not and stay as text reference only)
	lan := network.GetLANInfo()
	ips := lan.Physical
	if len(ips) == 0 {
		ips = lan.Virtual // degenerate case: no physical private IP at all
		if len(ips) == 0 {
			ips = []string{"127.0.0.1"}
		}
	}
	primaryIP := ips[0]

	// 2. Resolve available port
	port := network.FindAvailablePort(*portFlag)

	// 3. Resolve upload directory
	uploadDir := *dirFlag
	if uploadDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			uploadDir = filepath.Join(home, "Downloads", "LAN_Drop")
		} else {
			uploadDir = "./LAN_Drop_Files"
		}
	}
	_ = os.MkdirAll(uploadDir, 0755)

	// 4. Resolve PIN: random by default, empty when auth is disabled
	pin := *pinFlag
	if *noPinFlag {
		pin = ""
	} else if pin == "" {
		pin = server.GenerateRandomPIN(4)
	}

	// 5. Setup embedded web FS (shared package, also used by the desktop app)
	webFS := webui.Assets()

	// 6. Initialize Server
	cfg := server.NewConfig(port, primaryIP, uploadDir, pin, webFS)
	srv := server.NewServer(cfg)

	baseURL := fmt.Sprintf("http://%s:%d", primaryIP, port)
	fullURL := baseURL
	if cfg.PIN != "" {
		fullURL = fmt.Sprintf("%s/?pin=%s", baseURL, cfg.PIN)
	}

	// 7. Render Terminal Banner & QR Code
	fmt.Println()
	fmt.Println("==================================================================")
	fmt.Printf("   ⚡ LAN Drop v%s - 局域网极简跨设备文件与文本极速快传站\n", AppVersion)
	fmt.Println("==================================================================")
	fmt.Printf(" 🌐 局域网访问地址 : \033[1;36m%s\033[0m\n", fullURL)
	if cfg.PIN != "" {
		fmt.Printf(" 🔒 访问 PIN 码    : \033[1;33m%s\033[0m (扫码可直接免密进入)\n", cfg.PIN)
	} else {
		fmt.Println(" 🔓 访问认证       : 已关闭 PIN 保护")
	}
	fmt.Printf(" 📂 文件存储目录   : %s\n", uploadDir)
	if backup := append(append([]string{}, ips[1:]...), lan.Virtual...); len(backup) > 0 {
		fmt.Printf(" 💡 备用/虚拟 IP  : %v (虚拟网卡不可扫码)\n", backup)
	}
	fmt.Println("------------------------------------------------------------------")

	// One QR per physical NIC so the right one can be scanned when several
	// real adapters exist (e.g. Ethernet + Wi-Fi). Virtual adapters never
	// get a QR: phones cannot reach them.
	qrIPs := ips
	if len(qrIPs) > maxQRCodes {
		qrIPs = qrIPs[:maxQRCodes]
	}
	for i, ip := range qrIPs {
		label := " 📱 手机扫码直达"
		if len(qrIPs) > 1 {
			label = fmt.Sprintf(" 📱 二维码 %d/%d（网卡 %s）", i+1, len(qrIPs), ip)
		}
		fmt.Println(label + " (用微信/系统相机扫描)：")
		fmt.Println()
		qrURL := fmt.Sprintf("http://%s:%d", ip, port)
		if cfg.PIN != "" {
			qrURL = fmt.Sprintf("%s/?pin=%s", qrURL, cfg.PIN)
		}
		fmt.Print(qrcode.PrintTerminal(qrURL))
		if i < len(qrIPs)-1 {
			fmt.Println("------------------------------------------------------------------")
		}
	}
	fmt.Println("==================================================================")
	fmt.Println(" [提示] 按 Ctrl + C 可安全停止服务")
	fmt.Println()

	// 8. Start HTTP Server
	httpServer := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%d", port),
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Minute, // Support long big-file chunk uploads
		WriteTimeout: 30 * time.Minute,
	}

	// Graceful shutdown channel
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	// Sweep orphaned chunk directories from aborted uploads
	go func() {
		srv.CleanupTempChunks(tempChunkMaxAge)
		ticker := time.NewTicker(tempSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			srv.CleanupTempChunks(tempChunkMaxAge)
		}
	}()

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	if !*noBrowserFlag {
		// Include the PIN for the local operator's browser (it is already
		// displayed in this terminal); avoids a 401 loop on the WebSocket.
		localURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		if cfg.PIN != "" {
			localURL = fmt.Sprintf("%s/?pin=%s", localURL, cfg.PIN)
		}
		openBrowser(localURL)
	}

	<-stopChan
	fmt.Println("\n正在关闭 LAN Drop 服务...")

	// Real graceful shutdown: stop accepting, close WS clients, drain requests
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	srv.Shutdown()
	fmt.Println("已安全退出。")
}

// openBrowser launches the default browser at url, best-effort.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
