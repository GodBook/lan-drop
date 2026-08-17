package main

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	wmClose          = 0x0010
	wmSize           = 0x0005
	wmNull           = 0x0000
	wmLButtonDblClk  = 0x0203
	wmRButtonUp      = 0x0205
	wmContextMenu    = 0x007B
	wmTrayCallback   = 0x8001
	sizeMinimized    = 1
	gwlpWndProc      = ^uintptr(3)
	swHide           = 0
	swRestore        = 9
	nimAdd           = 0
	nimDelete        = 2
	nifMessage       = 1
	nifIcon          = 2
	nifTip           = 4
	mfString         = 0
	mfSeparator      = 0x0800
	tpmRightButton   = 0x0002
	tpmReturnCommand = 0x0100
	idiApplication   = 32512
	cfUnicodeText    = 13
	gmemMoveable     = 0x0002
	gmemZeroInit     = 0x0040

	menuShow = 1001 + iota
	menuOpenDirectory
	menuCopyAddress
	menuSettings
	menuExit
)

var (
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	ntdll                 = windows.NewLazySystemDLL("ntdll.dll")
	procShellNotifyIconW  = shell32.NewProc("Shell_NotifyIconW")
	procSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW   = user32.NewProc("CallWindowProcW")
	procLoadIconW         = user32.NewProc("LoadIconW")
	procCreatePopupMenu   = user32.NewProc("CreatePopupMenu")
	procAppendMenuW       = user32.NewProc("AppendMenuW")
	procTrackPopupMenu    = user32.NewProc("TrackPopupMenu")
	procDestroyMenu       = user32.NewProc("DestroyMenu")
	procGetCursorPos      = user32.NewProc("GetCursorPos")
	procPostMessageW      = user32.NewProc("PostMessageW")
	procOpenClipboard     = user32.NewProc("OpenClipboard")
	procCloseClipboard    = user32.NewProc("CloseClipboard")
	procEmptyClipboard    = user32.NewProc("EmptyClipboard")
	procSetClipboardData  = user32.NewProc("SetClipboardData")
	procGlobalAlloc       = kernel32.NewProc("GlobalAlloc")
	procGlobalLock        = kernel32.NewProc("GlobalLock")
	procGlobalUnlock      = kernel32.NewProc("GlobalUnlock")
	procGlobalFree        = kernel32.NewProc("GlobalFree")
	procRtlMoveMemory     = ntdll.NewProc("RtlMoveMemory")
)

type notifyIconData struct {
	Size             uint32
	Window           uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             windows.GUID
	BalloonIcon      uintptr
}

type nativePoint struct {
	X int32
	Y int32
}

type trayActions struct {
	OpenDirectory func()
	CopyAddress   func()
	OpenSettings  func()
}

type desktopTray struct {
	window   webview2.WebView
	hwnd     uintptr
	data     notifyIconData
	oldProc  uintptr
	callback uintptr
	actions  trayActions
	exiting  atomic.Bool
}

func newDesktopTray(window webview2.WebView, actions trayActions) (*desktopTray, error) {
	hwnd := uintptr(window.Window())
	tray := &desktopTray{window: window, hwnd: hwnd, actions: actions}
	tray.callback = windows.NewCallback(tray.windowProc)
	oldProc, _, callErr := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, tray.callback)
	if oldProc == 0 && callErr != windows.ERROR_SUCCESS {
		return nil, fmt.Errorf("replace desktop window procedure: %w", callErr)
	}
	tray.oldProc = oldProc

	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	tray.data = notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Window:          hwnd,
		ID:              1,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmTrayCallback,
		Icon:            icon,
	}
	copy(tray.data.Tip[:], windows.StringToUTF16("LAN Drop - 局域网快传"))
	result, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&tray.data)))
	if result == 0 {
		procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, tray.oldProc)
		return nil, fmt.Errorf("add system tray icon: %w", err)
	}
	return tray, nil
}

func (t *desktopTray) Close() {
	if t == nil {
		return
	}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.data)))
	if t.oldProc != 0 {
		procSetWindowLongPtrW.Call(t.hwnd, gwlpWndProc, t.oldProc)
	}
}

func (t *desktopTray) Exit() {
	if t == nil {
		return
	}
	t.window.Dispatch(func() {
		t.exiting.Store(true)
		t.window.Terminate()
	})
}

func (t *desktopTray) showWindow() {
	procShowWindow.Call(t.hwnd, swRestore)
	procSetForegroundWindow.Call(t.hwnd)
}

func (t *desktopTray) windowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		if !t.exiting.Load() {
			procShowWindow.Call(hwnd, swHide)
			return 0
		}
	case wmSize:
		if wParam == sizeMinimized {
			procShowWindow.Call(hwnd, swHide)
			return 0
		}
	case wmTrayCallback:
		switch lParam {
		case wmLButtonDblClk:
			t.showWindow()
			return 0
		case wmRButtonUp, wmContextMenu:
			t.showMenu()
			return 0
		}
	}
	result, _, _ := procCallWindowProcW.Call(t.oldProc, hwnd, message, wParam, lParam)
	return result
}

func (t *desktopTray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendTrayMenu(menu, mfString, menuShow, "显示主窗口")
	appendTrayMenu(menu, mfString, menuOpenDirectory, "打开接收目录")
	appendTrayMenu(menu, mfString, menuCopyAddress, "复制连接地址")
	appendTrayMenu(menu, mfString, menuSettings, "连接与存储设置")
	appendTrayMenu(menu, mfSeparator, 0, "")
	appendTrayMenu(menu, mfString, menuExit, "退出 LAN Drop")

	var point nativePoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&point)))
	procSetForegroundWindow.Call(t.hwnd)
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCommand, uintptr(point.X), uintptr(point.Y), 0, t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, wmNull, 0, 0)

	switch command {
	case menuShow:
		t.showWindow()
	case menuOpenDirectory:
		go t.actions.OpenDirectory()
	case menuCopyAddress:
		go t.actions.CopyAddress()
	case menuSettings:
		t.showWindow()
		go t.actions.OpenSettings()
	case menuExit:
		t.exiting.Store(true)
		t.window.Terminate()
	}
}

func appendTrayMenu(menu, flags, id uintptr, label string) {
	var pointer uintptr
	if label != "" {
		text, _ := windows.UTF16PtrFromString(label)
		pointer = uintptr(unsafe.Pointer(text))
	}
	procAppendMenuW.Call(menu, flags, id, pointer)
}

func copyTextToClipboard(hwnd uintptr, text string) error {
	opened, _, err := procOpenClipboard.Call(hwnd)
	if opened == 0 {
		return fmt.Errorf("open clipboard: %w", err)
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	utf16Text := windows.StringToUTF16(text)
	size := uintptr(len(utf16Text) * 2)
	memory, _, err := procGlobalAlloc.Call(gmemMoveable|gmemZeroInit, size)
	if memory == 0 {
		return fmt.Errorf("allocate clipboard memory: %w", err)
	}
	pointer, _, err := procGlobalLock.Call(memory)
	if pointer == 0 {
		procGlobalFree.Call(memory)
		return fmt.Errorf("lock clipboard memory: %w", err)
	}
	procRtlMoveMemory.Call(pointer, uintptr(unsafe.Pointer(&utf16Text[0])), size)
	procGlobalUnlock.Call(memory)
	result, _, err := procSetClipboardData.Call(cfUnicodeText, memory)
	if result == 0 {
		procGlobalFree.Call(memory)
		return fmt.Errorf("set clipboard data: %w", err)
	}
	return nil
}
