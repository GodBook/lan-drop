// LAN Drop desktop app: a native WebView2 shell around the local server.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"landrop/core/discovery"
	"landrop/core/network"
	"landrop/core/server"
	"landrop/webui"
)

// AppVersion is overridden at build time via
// -ldflags "-X main.AppVersion=<version>".
var AppVersion = "1.5.0"

const (
	defaultPort       = 8087
	tempChunkMaxAge   = 2 * time.Hour
	tempSweepInterval = 30 * time.Minute
)

type desktopBridgeInfo struct {
	Adapters   []network.AdapterAddress `json:"adapters"`
	SelectedIP string                   `json:"selected_ip"`
	UploadDir  string                   `json:"upload_dir"`
	ConnectURL string                   `json:"connect_url"`
}

type desktopRuntime struct {
	mu         sync.Mutex
	settings   desktopSettings
	adapters   []network.AdapterAddress
	cfg        *server.Config
	window     webview2.WebView
	mdns       *discovery.Advertiser
	hostname   string
	port       int
	trayWindow uintptr
}

func main() {
	runtime.LockOSThread()

	instanceHandle, firstInstance, err := acquireSingleInstance()
	if err != nil {
		log.Fatalf("single-instance guard failed: %v", err)
	}
	if !firstInstance {
		return
	}
	defer windows.CloseHandle(instanceHandle)

	lan := network.GetLANInfo()
	adapters := append([]network.AdapterAddress(nil), lan.PhysicalAdapters...)
	if len(adapters) == 0 {
		for _, ip := range lan.Physical {
			adapters = append(adapters, network.AdapterAddress{Name: "局域网网卡", IP: ip})
		}
	}
	if len(adapters) == 0 {
		for _, ip := range lan.Virtual {
			adapters = append(adapters, network.AdapterAddress{Name: "备用网络", IP: ip})
		}
	}
	if len(adapters) == 0 {
		adapters = []network.AdapterAddress{{Name: "本机", IP: "127.0.0.1"}}
	}

	settings := loadDesktopSettings()
	selectedIP := choosePhysicalAdapter(adapters, settings.SelectedIP)
	settings.SelectedIP = selectedIP
	_ = os.MkdirAll(settings.UploadDir, 0755)
	_ = saveDesktopSettings(settings)

	port := network.FindAvailablePort(defaultPort)
	cfg := server.NewConfig(port, selectedIP, settings.UploadDir, server.GenerateRandomPIN(4), webui.Assets())
	srv := server.NewServer(cfg)
	httpServer := &http.Server{
		Addr:         fmt.Sprintf("0.0.0.0:%d", port),
		Handler:      srv.Handler(),
		ReadTimeout:  30 * time.Minute,
		WriteTimeout: 30 * time.Minute,
	}

	window := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  fmt.Sprintf("LAN Drop v%s", AppVersion),
			Width:  1120,
			Height: 780,
			Center: true,
		},
	})
	if window == nil {
		log.Fatalln("failed to create WebView2 window (需要 Windows 10/11 自带的 WebView2 运行时)")
	}
	window.SetSize(1120, 780, webview2.HintNone)

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "landrop"
	}
	desktop := &desktopRuntime{
		settings: settings,
		adapters: adapters,
		cfg:      cfg,
		window:   window,
		hostname: hostname,
		port:     port,
	}
	bindDesktopBridge(window, desktop)
	window.Init(`window.__LANDROP_DESKTOP__ = true;`)

	listener, err := net.Listen("tcp4", httpServer.Addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", httpServer.Addr, err)
	}
	serverErr := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()
	desktop.restartDiscovery()
	window.Navigate(fmt.Sprintf("http://127.0.0.1:%d/?pin=%s", port, url.QueryEscape(cfg.PIN)))

	go func() {
		srv.CleanupTempChunks(tempChunkMaxAge)
		ticker := time.NewTicker(tempSweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			srv.CleanupTempChunks(tempChunkMaxAge)
		}
	}()

	tray, trayErr := newDesktopTray(window, trayActions{
		OpenDirectory: func() { _ = desktop.openUploadDirectory() },
		CopyAddress:   func() { _ = desktop.copyConnectionAddress() },
		OpenSettings:  desktop.openSettings,
	})
	if trayErr != nil {
		log.Printf("system tray unavailable: %v", trayErr)
	} else {
		desktop.trayWindow = tray.hwnd
		defer tray.Close()
	}

	go func() {
		if err := <-serverErr; err != nil {
			log.Printf("server error: %v", err)
			if tray != nil {
				tray.Exit()
			} else {
				window.Dispatch(func() { window.Terminate() })
			}
		}
	}()

	window.Run()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	desktop.closeDiscovery()
	srv.Shutdown()
}

func bindDesktopBridge(window webview2.WebView, desktop *desktopRuntime) {
	_ = window.Bind("desktopGetSettings", func() desktopBridgeInfo {
		return desktop.info()
	})
	_ = window.Bind("desktopSetAdapter", func(ip string) (desktopBridgeInfo, error) {
		return desktop.setAdapter(ip)
	})
	_ = window.Bind("desktopChooseUploadDir", func() (desktopBridgeInfo, error) {
		return desktop.chooseUploadDirectory()
	})
	_ = window.Bind("desktopOpenUploadDir", func() error {
		return desktop.openUploadDirectory()
	})
	_ = window.Bind("desktopCopyConnectionAddress", func() error {
		return desktop.copyConnectionAddress()
	})
}

func (d *desktopRuntime) info() desktopBridgeInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.infoLocked()
}

func (d *desktopRuntime) connectURL() string {
	address := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(d.cfg.HostIP(), strconv.Itoa(d.port)),
		Path:   "/",
	}
	query := address.Query()
	query.Set("pin", d.cfg.PIN)
	address.RawQuery = query.Encode()
	return address.String()
}

func (d *desktopRuntime) setAdapter(ip string) (desktopBridgeInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	valid := false
	for _, adapter := range d.adapters {
		if adapter.IP == ip {
			valid = true
			break
		}
	}
	if !valid {
		return desktopBridgeInfo{}, fmt.Errorf("未知网卡地址 %q", ip)
	}
	if err := d.cfg.SetHostIP(ip); err != nil {
		return desktopBridgeInfo{}, err
	}
	d.settings.SelectedIP = ip
	if err := saveDesktopSettings(d.settings); err != nil {
		return desktopBridgeInfo{}, err
	}
	d.restartDiscoveryLocked()
	d.notifySettingsChanged()
	return d.infoLocked(), nil
}

func (d *desktopRuntime) chooseUploadDirectory() (desktopBridgeInfo, error) {
	current := d.cfg.UploadDir()
	selected, err := chooseUploadDirectory(current)
	if err != nil {
		return d.info(), nil
	}
	if err := d.cfg.SetUploadDir(selected); err != nil {
		return desktopBridgeInfo{}, err
	}
	d.mu.Lock()
	d.settings.UploadDir = selected
	err = saveDesktopSettings(d.settings)
	info := d.infoLocked()
	d.mu.Unlock()
	if err != nil {
		return desktopBridgeInfo{}, err
	}
	d.notifySettingsChanged()
	return info, nil
}

func (d *desktopRuntime) openUploadDirectory() error {
	return openDirectory(d.cfg.UploadDir())
}

func (d *desktopRuntime) copyConnectionAddress() error {
	hwnd := d.trayWindow
	if hwnd == 0 {
		hwnd = uintptr(d.window.Window())
	}
	return copyTextToClipboard(hwnd, d.connectURL())
}

func (d *desktopRuntime) openSettings() {
	d.window.Dispatch(func() {
		d.window.Eval(`window.dispatchEvent(new Event("landrop:open-desktop-settings"));`)
	})
}

func (d *desktopRuntime) notifySettingsChanged() {
	d.window.Dispatch(func() {
		d.window.Eval(`window.dispatchEvent(new Event("landrop:desktop-settings-changed"));`)
	})
}

func (d *desktopRuntime) infoLocked() desktopBridgeInfo {
	return desktopBridgeInfo{
		Adapters:   append([]network.AdapterAddress(nil), d.adapters...),
		SelectedIP: d.cfg.HostIP(),
		UploadDir:  d.cfg.UploadDir(),
		ConnectURL: d.connectURL(),
	}
}

func (d *desktopRuntime) restartDiscovery() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restartDiscoveryLocked()
}

func (d *desktopRuntime) restartDiscoveryLocked() {
	if d.mdns != nil {
		_ = d.mdns.Close()
		d.mdns = nil
	}
	advertiser, err := discovery.Start(d.hostname, d.cfg.HostIP(), d.port, d.cfg.PIN != "")
	if err != nil {
		log.Printf("mDNS discovery disabled: %v", err)
		return
	}
	d.mdns = advertiser
}

func (d *desktopRuntime) closeDiscovery() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mdns != nil {
		_ = d.mdns.Close()
		d.mdns = nil
	}
}
