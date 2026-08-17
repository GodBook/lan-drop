//go:build linux || darwin || freebsd

package server

import (
	"fmt"
	"syscall"
)

func availableDiskBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, fmt.Errorf("filesystem reported invalid block size")
	}
	if stat.Bavail <= 0 {
		if stat.Bavail == 0 {
			return 0, nil
		}
		return 0, fmt.Errorf("filesystem reported invalid available blocks")
	}
	blockSize := uint64(stat.Bsize)
	availableBlocks := uint64(stat.Bavail)
	if availableBlocks > ^uint64(0)/blockSize {
		return ^uint64(0), nil
	}
	return availableBlocks * blockSize, nil
}
