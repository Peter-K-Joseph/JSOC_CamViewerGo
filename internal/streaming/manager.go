package streaming

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jsoc/camviewer/internal/store"
)

// Manager owns all per-camera stream goroutines and their tracks.
type Manager struct {
	mu                sync.RWMutex
	streams           map[string]*streamEntry // keyed by camera ID
	keepaliveInterval time.Duration
}

type streamEntry struct {
	track   *Track
	source  *WsSource
	health  store.StreamHealth
	cookies []*http.Cookie
}

func NewManager(keepaliveInterval time.Duration) *Manager {
	return &Manager{
		streams:           make(map[string]*streamEntry),
		keepaliveInterval: keepaliveInterval,
	}
}

// Start begins streaming for a camera. Idempotent — stops existing source first.
func (m *Manager) Start(cam *store.Camera, cookies []*http.Cookie) {
	m.Stop(cam.ID)

	track := NewTrack(cam.StreamKey)
	source := NewWsSource(
		cam.IP, cam.Port,
		cam.Username, cam.Password,
		cookies,
		cam.Channel, cam.Subtype,
		track,
		m.keepaliveInterval,
	)

	entry := &streamEntry{
		track:   track,
		source:  source,
		health:  store.HealthStarting,
		cookies: cookies,
	}

	m.mu.Lock()
	m.streams[cam.ID] = entry
	m.mu.Unlock()

	go func() {
		m.setHealth(cam.ID, store.HealthStarting)
		source.Run() // blocks until Stop() called or permanent error
		m.setHealth(cam.ID, store.HealthOffline)
	}()

	// Flip to "ok" after a brief moment if still running.
	go func() {
		time.Sleep(3 * time.Second)
		m.mu.RLock()
		e, ok := m.streams[cam.ID]
		m.mu.RUnlock()
		if ok && e.health == store.HealthStarting {
			m.setHealth(cam.ID, store.HealthOK)
		}
	}()

	log.Printf("[manager] started stream for camera %s (%s)", cam.ID, cam.Name)
}

// Stop halts the stream for a camera.
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	entry, ok := m.streams[id]
	if ok {
		delete(m.streams, id)
	}
	m.mu.Unlock()

	if ok && entry.source != nil {
		entry.source.Stop()
		log.Printf("[manager] stopped stream for camera %s", id)
	}
}

// Track returns the live Track for a camera stream key, or nil.
func (m *Manager) Track(streamKey string) *Track {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.streams {
		if e.track.Key == streamKey {
			return e.track
		}
	}
	return nil
}

// Health returns the current stream health for a camera.
func (m *Manager) Health(id string) store.StreamHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.streams[id]; ok {
		return e.health
	}
	return store.HealthUnknown
}

// RTSPPath returns the path component for VLC: "cam/<stream_key>".
func RTSPPath(prefix, streamKey string) string {
	return fmt.Sprintf("%s/%s", prefix, streamKey)
}

func (m *Manager) setHealth(id string, h store.StreamHealth) {
	m.mu.Lock()
	if e, ok := m.streams[id]; ok {
		e.health = h
	}
	m.mu.Unlock()
}
