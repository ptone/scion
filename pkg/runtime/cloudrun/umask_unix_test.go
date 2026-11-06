//go:build unix

package cloudrun

import "syscall"

// setPermissiveUmask sets the process umask to 022 and returns a function
// that restores the previous one.
func setPermissiveUmask() func() {
	prev := syscall.Umask(0o022)
	return func() { syscall.Umask(prev) }
}
