//go:build windows

package console

import (
	"os"
	"syscall"
	"unsafe"
)

const enableVirtualTerminalProcessing = 0x0004

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// EnableANSI turns on VT processing for stdout so ANSI color escapes render on
// the classic Windows console instead of printing as raw text.
func EnableANSI() {
	handle := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	ok, _, _ := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	if ok != 0 {
		_, _, _ = procSetConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing))
	}
}
