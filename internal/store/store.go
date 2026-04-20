package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// FlexTime unmarshals both RFC3339 (Go) and naive ISO8601 (Python) timestamps.
type FlexTime struct{ time.Time }

func (ft *FlexTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			ft.Time = t
			return nil
		}
	}
	return fmt.Errorf("cannot parse time %q", s)
}

func (ft FlexTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + ft.Time.Format(time.RFC3339Nano) + `"`), nil
}

type StreamHealth string

const (
	HealthUnknown    StreamHealth = "unknown"
	HealthOK         StreamHealth = "ok"
	HealthAuthFailed StreamHealth = "auth-failed"
	HealthOffline    StreamHealth = "offline"
	HealthStarting   StreamHealth = "starting"
)

type Camera struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	IP             string   `json:"ip"`
	Port           int      `json:"port"`
	Username       string   `json:"username,omitempty"`
	Password       string   `json:"password,omitempty"`
	StreamKey      string   `json:"stream_key"`
	Channel        int      `json:"channel"`
	Subtype        int      `json:"subtype"`
	Enabled        bool     `json:"enabled"`
	Manufacturer   string   `json:"manufacturer,omitempty"`
	Model          string   `json:"model,omitempty"`
	AddedAt        FlexTime `json:"added_at"`
	ONVIFUsername  string   `json:"onvif_username,omitempty"`
	ONVIFPassword  string   `json:"onvif_password,omitempty"`
	PTZEnabled     bool     `json:"ptz_enabled,omitempty"`
}

type CameraPublic struct {
	Camera
	Password       string       `json:"password,omitempty"`
	HasCredentials bool         `json:"has_credentials"`
	Health         StreamHealth `json:"health"`
	StreamRTSPURL  string       `json:"stream_rtsp_url,omitempty"`
	HasPTZ         bool         `json:"has_ptz"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	cameras  []*Camera
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Store{path: filepath.Join(dataDir, "cameras.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) List() []*Camera {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Camera, len(s.cameras))
	copy(out, s.cameras)
	return out
}

func (s *Store) Get(id string) (*Camera, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cameras {
		if c.ID == id {
			cp := *c
			return &cp, true
		}
	}
	return nil, false
}

func (s *Store) FindByIP(ip string) (*Camera, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cameras {
		if c.IP == ip {
			cp := *c
			return &cp, true
		}
	}
	return nil, false
}

func (s *Store) Add(name, ip string, port int, manufacturer, model string) (*Camera, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cam := &Camera{
		ID:           uuid.New().String(),
		Name:         name,
		IP:           ip,
		Port:         port,
		StreamKey:    slugify(name),
		Channel:      1,
		Subtype:      0,
		Enabled:      true,
		Manufacturer: manufacturer,
		Model:        model,
		AddedAt:      FlexTime{time.Now()},
	}
	cam.StreamKey = s.uniqueKey(cam.StreamKey)
	s.cameras = append(s.cameras, cam)
	if err := s.save(); err != nil {
		return nil, err
	}
	cp := *cam
	return &cp, nil
}

func (s *Store) UpdateCredentials(id, username, password string) (*Camera, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.cameras {
		if c.ID == id {
			c.Username = username
			c.Password = password
			if err := s.save(); err != nil {
				return nil, err
			}
			cp := *c
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("camera %s not found", id)
}

// UpdateONVIFCredentials stores ONVIF credentials and marks PTZ as enabled.
// Pass empty username/password to clear PTZ (disables PTZ).
func (s *Store) UpdateONVIFCredentials(id, username, password string, enabled bool) (*Camera, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.cameras {
		if c.ID == id {
			c.ONVIFUsername = username
			c.ONVIFPassword = password
			c.PTZEnabled = enabled
			if err := s.save(); err != nil {
				return nil, err
			}
			cp := *c
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("camera %s not found", id)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.cameras {
		if c.ID == id {
			s.cameras = append(s.cameras[:i], s.cameras[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("camera %s not found", id)
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.cameras = []*Camera{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cameras.json: %w", err)
	}
	// Support both bare array (Go format) and {"cameras":[...]} (Python format).
	if err := json.Unmarshal(data, &s.cameras); err == nil {
		return nil
	}
	var wrapped struct {
		Cameras []*Camera `json:"cameras"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return fmt.Errorf("parse cameras.json: %w", err)
	}
	s.cameras = wrapped.Cameras
	// Re-save in Go format so future loads are bare arrays.
	return s.save()
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.cameras, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) uniqueKey(base string) string {
	key := base
	for n := 2; ; n++ {
		taken := false
		for _, c := range s.cameras {
			if c.StreamKey == key {
				taken = true
				break
			}
		}
		if !taken {
			return key
		}
		key = fmt.Sprintf("%s-%d", base, n)
	}
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "cam"
	}
	return s
}
