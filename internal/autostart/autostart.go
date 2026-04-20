// Package autostart registers or removes the binary as a system boot service.
// The implementation is platform-specific; see autostart_*.go files.
package autostart

import "os"

// execPath returns the path of the running binary.
func execPath() (string, error) {
	return os.Executable()
}
