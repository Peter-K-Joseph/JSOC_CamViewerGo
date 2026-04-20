//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

const plistLabel = "com.jsoc.camviewer"

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Exec}}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>{{.LogDir}}/jsoc-stdout.log</string>
  <key>StandardErrorPath</key>
  <string>{{.LogDir}}/jsoc-stderr.log</string>
</dict>
</plist>
`))

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist"), nil
}

// Enable installs and loads a LaunchAgent for the current user.
func Enable() error {
	exe, err := execPath()
	if err != nil {
		return fmt.Errorf("autostart: resolve executable: %w", err)
	}
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, "Library", "Logs")
	_ = os.MkdirAll(logDir, 0755)

	path, err := plistPath()
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(path), 0755)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("autostart: write plist: %w", err)
	}
	defer f.Close()

	if err := plistTmpl.Execute(f, map[string]string{
		"Label":  plistLabel,
		"Exec":   exe,
		"LogDir": logDir,
	}); err != nil {
		return fmt.Errorf("autostart: render plist: %w", err)
	}

	// Unload first (ignore error — may not be loaded yet).
	exec.Command("launchctl", "unload", path).Run() //nolint:errcheck
	if out, err := exec.Command("launchctl", "load", path).CombinedOutput(); err != nil {
		return fmt.Errorf("autostart: launchctl load: %s", out)
	}
	return nil
}

// Disable unloads and removes the LaunchAgent.
func Disable() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	exec.Command("launchctl", "unload", path).Run() //nolint:errcheck
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("autostart: remove plist: %w", err)
	}
	return nil
}

// IsEnabled reports whether the LaunchAgent plist exists.
func IsEnabled() (bool, error) {
	path, err := plistPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
