package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"landrop/internal/network"
	"landrop/internal/qrcode"
	"landrop/internal/server"
)

//go:embed web/*
var embeddedWebFS embed.FS

// AppVersion is the current release version of LAN Drop.
const AppVersion = "1.0.0"

func main() {
	portFlag := flag.Int("p", 8087, "Service port (default: 8087)")
	dirFlag := flag.String("d", "", "Directory to save received files (default: ~/Downloads/LAN_Drop)")
	pinFlag := flag.String("pin", "", "Custom PIN access code (default: randomly generated 4-digit)")
	noPinFlag := flag.Bool("no-pin", false, "Disable PIN authentication")
	flag.Parse()

	// 1. Detect Network LAN IP
	ips, err := network.GetLocalIPs()
	if err != nil || len(ips) == 0 {
		ips = []string{"127.0.0.1"}
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

	// 4. Resolve PIN
	pin := *pinFlag
	if *noPinFlag {
		pin = ""
	}

	// 5. Setup embedded web FS
	webFS, err := fs.Sub(embeddedWebFS, "web")
	if err != nil {
		log.Fatalf("Failed to initialize embedded web filesystem: %v", err)
	}

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
	if len(ips) > 1 {
		fmt.Printf(" 💡 备用局域网 IP  : %v\n", ips[1:])
	}
	fmt.Println("------------------------------------------------------------------")
	fmt.Println(" 📱 手机扫码直达 (用微信/系统相机扫描下方二维码)：")
	fmt.Println()

	qrOutput := qrcode.PrintTerminal(fullURL)
	fmt.Print(qrOutput)
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

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-stopChan
	fmt.Println("\n正在关闭 LAN Drop 服务...")
}
