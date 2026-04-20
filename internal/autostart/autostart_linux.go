//go:build linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const serviceName = "jsoc-camviewer"

// ── systemd user service ──────────────────────────────────────────────────────

func systemdUnitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", serviceName+".service")
}

func writeSystemdUnit(exe string) error {
	path := systemdUnitPath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	content := fmt.Sprintf(`[Unit]
Description=JSOC Camera Viewer NVR
After=network.target

[Service]
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
	return os.WriteFile(path, []byte(content), 0644)
}

func hasSystemd() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// ── XDG autostart fallback ────────────────────────────────────────────────────

func xdgDesktopPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "autostart", serviceName+".desktop")
}

func writeDesktopFile(exe string) error {
	path := xdgDesktopPath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=JSOC Camera Viewer
Exec=%s
Hidden=false
NoDisplay=false
X-GNOME-Autostart-enabled=true
Comment=JSOC NVR Camera Viewer
`, exe)
	return os.WriteFile(path, []byte(content), 0644)
}

// ── Public API ────────────────────────────────────────────────────────────────

// Enable registers the service using systemd (preferred) or XDG autostart.
func Enable() error {
	exe, err := execPath()
	if err != nil {
		return fmt.Errorf("autostart: resolve executable: %w", err)
	}

	if hasSystemd() {
		if err := writeSystemdUnit(exe); err != nil {
			return fmt.Errorf("autostart: write unit: %w", err)
		}
		exec.Command("systemctl", "--user", "daemon-reload").Run() //nolint:errcheck
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", serviceName).CombinedOutput(); err != nil {
			// Not fatal — unit written; will start on next login even if enable failed.
			_ = fmt.Sprintf("autostart: systemctl enable: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}

	// XDG fallback for non-systemd desktops.
	return writeDesktopFile(exe)
}

// Disable removes the service registration.
func Disable() error {
	if hasSystemd() {
		exec.Command("systemctl", "--user", "disable", "--now", serviceName).Run() //nolint:errcheck
		if err := os.Remove(systemdUnitPath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("autostart: remove unit: %w", err)
		}
		exec.Command("systemctl", "--user", "daemon-reload").Run() //nolint:errcheck
		return nil
	}
	if err := os.Remove(xdgDesktopPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("autostart: remove desktop: %w", err)
	}
	return nil
}

// IsEnabled reports whether an autostart entry exists.
func IsEnabled() (bool, error) {
	if hasSystemd() {
		_, err := os.Stat(systemdUnitPath())
		if os.IsNotExist(err) {
			return false, nil
		}
		return err == nil, err
	}
	_, err := os.Stat(xdgDesktopPath())
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
