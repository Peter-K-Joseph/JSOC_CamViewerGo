package store

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// ── Add / List / Get ──────────────────────────────────────────────────────────

func TestAdd_And_List(t *testing.T) {
	s := newTestStore(t)

	cam, err := s.Add("Front Door", "192.168.1.10", 80, "Dahua", "IPC-HDW")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if cam.ID == "" {
		t.Error("camera ID should be non-empty")
	}
	if cam.Name != "Front Door" {
		t.Errorf("Name = %q, want Front Door", cam.Name)
	}
	if cam.StreamKey == "" {
		t.Error("StreamKey should be generated")
	}
	if !cam.Enabled {
		t.Error("camera should be enabled by default")
	}

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	if list[0].ID != cam.ID {
		t.Error("listed camera ID mismatch")
	}
}

func TestAdd_NormalizesCameraAddress(t *testing.T) {
	s := newTestStore(t)

	cam, err := s.Add("Front Door", "http//192.168.1.40/onvif/device_service", 80, "Dahua", "IPC-HDW")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if cam.IP != "192.168.1.40" {
		t.Errorf("IP = %q, want 192.168.1.40", cam.IP)
	}
	if cam.Port != 80 {
		t.Errorf("Port = %d, want 80", cam.Port)
	}
}

func TestGet_Found(t *testing.T) {
	s := newTestStore(t)
	cam, _ := s.Add("Cam A", "10.0.0.1", 80, "", "")

	got, ok := s.Get(cam.ID)
	if !ok {
		t.Fatal("Get should find existing camera")
	}
	if got.ID != cam.ID {
		t.Errorf("Get ID = %q, want %q", got.ID, cam.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, ok := s.Get("nonexistent-id")
	if ok {
		t.Error("Get should return false for unknown ID")
	}
}

func TestFindByIP(t *testing.T) {
	s := newTestStore(t)
	s.Add("Cam A", "10.0.0.1", 80, "", "")
	s.Add("Cam B", "10.0.0.2", 80, "", "")

	cam, ok := s.FindByIP("10.0.0.2")
	if !ok {
		t.Fatal("FindByIP should find camera by IP")
	}
	if cam.Name != "Cam B" {
		t.Errorf("FindByIP name = %q, want Cam B", cam.Name)
	}

	_, ok = s.FindByIP("10.0.0.99")
	if ok {
		t.Error("FindByIP should return false for unknown IP")
	}
}

// ── UpdateCredentials ─────────────────────────────────────────────────────────

func TestUpdateCredentials(t *testing.T) {
	s := newTestStore(t)
	cam, _ := s.Add("Cam", "10.0.0.1", 80, "", "")

	updated, err := s.UpdateCredentials(cam.ID, "admin", "secret")
	if err != nil {
		t.Fatalf("UpdateCredentials: %v", err)
	}
	if updated.Username != "admin" || updated.Password != "secret" {
		t.Errorf("credentials not updated: %q / %q", updated.Username, updated.Password)
	}

	// Verify persisted.
	got, _ := s.Get(cam.ID)
	if got.Username != "admin" {
		t.Error("updated username not persisted")
	}
}

func TestUpdateCredentials_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpdateCredentials("bad-id", "u", "p")
	if err == nil {
		t.Error("expected error for unknown camera ID")
	}
}

// ── UpdateONVIFCredentials ────────────────────────────────────────────────────

func TestUpdateONVIFCredentials(t *testing.T) {
	s := newTestStore(t)
	cam, _ := s.Add("Cam", "10.0.0.1", 80, "", "")

	updated, err := s.UpdateONVIFCredentials(cam.ID, "onvif-user", "onvif-pass", true)
	if err != nil {
		t.Fatalf("UpdateONVIFCredentials: %v", err)
	}
	if updated.ONVIFUsername != "onvif-user" {
		t.Errorf("ONVIFUsername = %q, want onvif-user", updated.ONVIFUsername)
	}
	if !updated.PTZEnabled {
		t.Error("PTZEnabled should be true")
	}

	got, _ := s.Get(cam.ID)
	if got.PTZEnabled != true {
		t.Error("PTZEnabled not persisted")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	cam, _ := s.Add("Cam", "10.0.0.1", 80, "", "")

	if err := s.Delete(cam.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get(cam.ID); ok {
		t.Error("camera should be gone after Delete")
	}
	if len(s.List()) != 0 {
		t.Error("List should be empty after deleting only camera")
	}
}

func TestDelete_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("bad-id"); err == nil {
		t.Error("expected error deleting unknown ID")
	}
}

// ── StreamKey uniqueness ──────────────────────────────────────────────────────

func TestStreamKey_Unique(t *testing.T) {
	s := newTestStore(t)
	// Same name → stream keys must be deduplicated.
	cam1, _ := s.Add("Front", "10.0.0.1", 80, "", "")
	cam2, _ := s.Add("Front", "10.0.0.2", 80, "", "")

	if cam1.StreamKey == cam2.StreamKey {
		t.Errorf("duplicate stream keys: %q", cam1.StreamKey)
	}
}

// ── Persistence across restarts ───────────────────────────────────────────────

func TestPersistence(t *testing.T) {
	dir := t.TempDir()

	s1, _ := New(dir)
	cam, _ := s1.Add("Persistent Cam", "10.1.1.1", 80, "Dahua", "XYZ")
	s1.UpdateCredentials(cam.ID, "admin", "pass123")

	// Open a new store from the same directory — simulates restart.
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	got, ok := s2.Get(cam.ID)
	if !ok {
		t.Fatal("camera not found after reload")
	}
	if got.Name != "Persistent Cam" {
		t.Errorf("Name = %q after reload", got.Name)
	}
	if got.Username != "admin" {
		t.Errorf("Username = %q after reload", got.Username)
	}
}

func TestLoad_NormalizesCameraAddress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cameras.json")
	raw := `[{
  "id": "cam-1",
  "name": "Legacy Cam",
  "ip": "http//192.168.1.40/onvif/device_service",
  "port": 80,
  "stream_key": "legacy-cam",
  "channel": 1,
  "enabled": true,
  "added_at": "2026-04-20T10:00:00Z"
}]`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, ok := s.Get("cam-1")
	if !ok {
		t.Fatal("camera not found after load")
	}
	if got.IP != "192.168.1.40" {
		t.Errorf("IP = %q, want 192.168.1.40", got.IP)
	}
}

// ── Slugify ───────────────────────────────────────────────────────────────────

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Front Door", "front-door"},
		{"Camera #1!", "camera-1"}, // runs of non-alnum collapse to single dash; trailing dash trimmed
		{"  spaces  ", "spaces"},   // leading/trailing dashes trimmed
		{"", "cam"},
		{"ALLCAPS", "allcaps"},
	}
	for _, c := range cases {
		got := slugify(c.in)
		if got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── Atomic save (temp file rename) ───────────────────────────────────────────

func TestSave_AtomicNoTempFileLeft(t *testing.T) {
	s := newTestStore(t)
	s.Add("Cam", "10.0.0.1", 80, "", "")

	dir := filepath.Dir(s.path)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("stale tmp file after save: %s", e.Name())
		}
	}
}
