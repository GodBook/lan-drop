//go:build !windows && !linux && !darwin && !freebsd

package server

import "fmt"

func availableDiskBytes(path string) (uint64, error) {
	return 0, fmt.Errorf("disk space checks are not implemented for %q", path)
}
