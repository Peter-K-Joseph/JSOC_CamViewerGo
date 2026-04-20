//go:build windows

package autostart

import (
	"fmt"
	"os/exec"
	"strings"
)

const (
	regKey       = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	regValueName = "JSOCCamViewer"
)

// Enable adds the binary to the Windows HKCU startup registry key.
func Enable() error {
	exe, err := execPath()
	if err != nil {
		return fmt.Errorf("autostart: resolve executable: %w", err)
	}
	// Quote the path in case it contains spaces.
	out, err := exec.Command("reg", "add", regKey,
		"/v", regValueName,
		"/t", "REG_SZ",
		"/d", `"`+exe+`"`,
		"/f",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("autostart: reg add: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Disable removes the startup registry value. Succeeds silently if absent.
func Disable() error {
	out, err := exec.Command("reg", "delete", regKey,
		"/v", regValueName,
		"/f",
	).CombinedOutput()
	if err != nil && !regNotFound(out) {
		return fmt.Errorf("autostart: reg delete: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// IsEnabled reports whether the startup registry value exists.
func IsEnabled() (bool, error) {
	out, err := exec.Command("reg", "query", regKey,
		"/v", regValueName,
	).CombinedOutput()
	if err != nil {
		if regNotFound(out) {
			return false, nil
		}
		return false, fmt.Errorf("autostart: reg query: %s", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// regNotFound returns true when reg.exe output indicates the key/value was not found.
func regNotFound(out []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(out)))
	return strings.Contains(s, "unable to find") || strings.Contains(s, "not found")
}
