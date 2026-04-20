// Package settings manages user-configurable preferences that persist across
// restarts.  Unlike config.go (environment/startup flags), these can be
// changed at runtime through the web UI.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Protocol represents which transport the server uses to ingest a camera stream.
type Protocol string

const (
	ProtocolWS    Protocol = "ws"    // Dahua /rtspoverwebsocket (default)
	ProtocolRTSP  Protocol = "rtsp"  // Standard RTSP TCP port 554
	ProtocolRTMP  Protocol = "rtmp"  // Camera-push RTMP (requires camera config — future)
	ProtocolDVRIP Protocol = "dvrip" // Dahua private protocol TCP port 37777
)

// Settings holds all user-configurable preferences.
type Settings struct {
	// AdminPassword overrides the startup-generated password when non-empty.
	// Stored as plain text (file is chmod 600); this is a local NVR application.
	AdminPassword string `json:"admin_password,omitempty"`

	// AutoStart registers / de-registers the binary as a system boot service.
	AutoStart bool `json:"auto_start"`

	// DirectStreamMode disables the server-side WS→fMP4 pipeline.
	// When true, the browser fetches the camera MJPEG stream via a thin proxy.
	DirectStreamMode bool `json:"direct_stream_mode"`

	// DirectStreamWindowed opens each camera in its own fullscreen browser
	// window (only meaningful when DirectStreamMode is true).
	DirectStreamWindowed bool `json:"direct_stream_windowed"`

	// StreamProtocol is the primary protocol the server uses to connect to cameras.
	StreamProtocol Protocol `json:"stream_protocol"`

	// StreamProtocolFallback enables automatic fallback when the primary
	// protocol fails.  Order: WS → RTSP → (RTMP when implemented).
	StreamProtocolFallback bool `json:"stream_protocol_fallback"`
}

func defaults() *Settings {
	return &Settings{
		StreamProtocol:         ProtocolWS,
		StreamProtocolFallback: true,
	}
}

// Store is a thread-safe persistent settings store.
type Store struct {
	mu   sync.RWMutex
	path string
	data *Settings
}

// New loads (or creates) settings from dataDir/settings.json.
func New(dataDir string) (*Store, error) {
	s := &Store{
		path: filepath.Join(dataDir, "settings.json"),
		data: defaults(),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Get returns a copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.data
}

// Update applies a partial update (only non-zero fields in patch are written)
// and persists to disk.
func (s *Store) Update(patch Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Merge: we use JSON round-trip so that false booleans are applied too.
	// Instead, accept the patch as the authoritative full state.
	s.data = &patch
	// Fill in any zero-value protocols with the default.
	if s.data.StreamProtocol == "" {
		s.data.StreamProtocol = ProtocolWS
	}
	return s.save()
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s.save() // write defaults
	}
	if err != nil {
		return fmt.Errorf("read settings.json: %w", err)
	}
	if err := json.Unmarshal(data, s.data); err != nil {
		return fmt.Errorf("parse settings.json: %w", err)
	}
	if s.data.StreamProtocol == "" {
		s.data.StreamProtocol = ProtocolWS
	}
	return nil
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
