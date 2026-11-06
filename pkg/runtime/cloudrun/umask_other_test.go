//go:build !unix

package cloudrun

// setPermissiveUmask is a no-op where there is no umask.
func setPermissiveUmask() func() { return func() {} }
