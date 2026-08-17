//go:build windows

package server

import (
	"fmt"
	"path/filepath"
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func availableDiskBytes(path string) (uint64, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	pathPointer, err := syscall.UTF16PtrFromString(absolutePath)
	if err != nil {
		return 0, err
	}

	var freeBytesAvailable uint64
	result, _, callErr := getDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(pathPointer)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		0,
		0,
	)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return 0, callErr
		}
		return 0, fmt.Errorf("GetDiskFreeSpaceExW failed")
	}
	return freeBytesAvailable, nil
}
