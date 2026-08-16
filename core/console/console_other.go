//go:build !windows

package console

// EnableANSI is a no-op on platforms whose terminals handle ANSI natively.
func EnableANSI() {}
