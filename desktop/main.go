// LAN Drop desktop app: a native WebView2 window wrapping the local server.
// The window loads http://127.0.0.1:<port>/?pin=... directly, so the existing
// web UI runs same-origin with zero protocol bridging. Closing the window
// shuts the server down gracefully.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jchv/go-webview2"

	"landrop/core/network"
	"landrop/core/server"
	"landrop/webui"
)

// AppVersion is overridden at build time via
// -ldflags "-X main.AppVersion=<version>".
var AppVersion = "1.3.1"

const (
	defaultPort       = 8087
	tempChunkMaxAge   = 2 * time.Hour
	tempSweepInterval = 30 * time.Minute
)

func main() {
	// Win32 windows are thread-affine: the OS thread that creates the window
	// must be the one pumping its messages. Pin the main goroutine to the
	// process' initial thread for the whole lifetime of the UI.
	runtime.LockOSThread()

	// 1. Network + port
	port := network.FindAvailablePort(defaultPort)
	lan := network.GetLANInfo()
	primaryIP := "127.0.0.1"
	if len(lan.Physical) > 0 {
		primaryIP = lan.Physical[0]
	} else if len(lan.Virtual) > 0 {
		primaryIP = lan.Virtual[0]
	}

	// 2. Upload directory
	uploadDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		uploadDir = filepath.Join(home, "Downloads", "LAN_Drop")
	} else {
		uploadDir = "./LAN_Drop_Files"
	}
	_ = os.MkdirAll(uploadDir, 0755)

	// 3. Server (random PIN; the desktop window signs in via ?pin=)
	cfg := server.NewConfig(port, primaryIP, uploadDir, server.GenerateRandomPIN(4), webui.Assets())
	srv := server.NewServer(cfg)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%d", port),
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Minute,
		WriteTimeout: 30 * time.Minute,
	}

	// Sweeper for aborted-upload leftovers
	go func() {
		srv.CleanupTempChunks(tempChunkMaxAge)
		ticker := time.NewTicker(tempSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			srv.CleanupTempChunks(tempChunkMaxAge)
		}
	}()

	serverErr := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// 4. Native window pointed at the local server
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  fmt.Sprintf("LAN Drop v%s", AppVersion),
			Width:  1120,
			Height: 780,
			Center: true,
		},
	})
	if w == nil {
		log.Fatalln("failed to create WebView2 window (需要 Windows 10/11 自带的 WebView2 运行时)")
	}
	w.SetSize(1120, 780, webview2.HintNone)
	w.Navigate(fmt.Sprintf("http://127.0.0.1:%d/?pin=%s", port, cfg.PIN))

	// If the HTTP server dies, close the window from the UI thread.
	go func() {
		if err := <-serverErr; err != nil {
			log.Printf("server error: %v", err)
			w.Dispatch(func() { w.Terminate() })
		}
	}()

	// Message loop on the main (locked) thread; blocks until window closes.
	w.Run()

	// 5. Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	srv.Shutdown()
}
