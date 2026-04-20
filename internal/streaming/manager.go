package streaming

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jsoc/camviewer/internal/settings"
	"github.com/jsoc/camviewer/internal/store"
)

// cameraSource is implemented by WsSource and RtspSource.
type cameraSource interface {
	Run()
	Stop()
}

// Manager owns all per-camera stream goroutines and their tracks.
type Manager struct {
	mu                sync.RWMutex
	streams           map[string]*streamEntry
	keepaliveInterval time.Duration
	settings          *settings.Store // nil → default to WS
}

type streamEntry struct {
	track   *Track
	source  cameraSource
	health  store.StreamHealth
	cookies []*http.Cookie
}

func NewManager(keepaliveInterval time.Duration, st *settings.Store) *Manager {
	return &Manager{
		streams:           make(map[string]*streamEntry),
		keepaliveInterval: keepaliveInterval,
		settings:          st,
	}
}

// Start begins streaming for a camera. Idempotent — stops existing source first.
func (m *Manager) Start(cam *store.Camera, cookies []*http.Cookie) {
	m.Stop(cam.ID)

	track := NewTrack(cam.StreamKey)

	// Select source based on current settings.
	src := m.buildSource(cam, cookies, track)

	entry := &streamEntry{
		track:   track,
		source:  src,
		health:  store.HealthStarting,
		cookies: cookies,
	}

	m.mu.Lock()
	m.streams[cam.ID] = entry
	m.mu.Unlock()

	go func() {
		m.setHealth(cam.ID, store.HealthStarting)
		src.Run() // blocks until Stop() called or permanent error
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

	log.Printf("[manager] started stream for camera %s (%s) via %s", cam.ID, cam.Name, m.activeProtocol())
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

// StopAll halts every active stream (used when switching to direct mode at runtime).
func (m *Manager) StopAll() {
	m.mu.Lock()
	entries := make(map[string]*streamEntry, len(m.streams))
	for k, v := range m.streams {
		entries[k] = v
	}
	m.streams = make(map[string]*streamEntry)
	m.mu.Unlock()

	for id, e := range entries {
		if e.source != nil {
			e.source.Stop()
			log.Printf("[manager] stopped stream for camera %s (stop-all)", id)
		}
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

// buildSource creates the appropriate source type based on the current settings.
func (m *Manager) buildSource(cam *store.Camera, cookies []*http.Cookie, track *Track) cameraSource {
	proto := settings.ProtocolWS
	if m.settings != nil {
		proto = m.settings.Get().StreamProtocol
	}

	switch proto {
	case settings.ProtocolRTSP:
		return NewRtspSource(
			cam.IP, 554,
			cam.Username, cam.Password,
			cam.Channel, cam.Subtype,
			track, m.keepaliveInterval,
		)
	case settings.ProtocolDVRIP:
		return NewDvripSource(
			cam.IP, 0, // 0 → uses dvripDefaultPort (37777)
			cam.Username, cam.Password,
			cam.Channel, cam.Subtype,
			track, m.keepaliveInterval,
		)
	case settings.ProtocolRTMP:
		// RTMP is camera-push only — not implementable as a pull source.
		// Fall back to WS and log a warning.
		log.Printf("[manager] RTMP pull not supported; using WS for camera %s", cam.Name)
		fallthrough
	default: // ProtocolWS
		return NewWsSource(
			cam.IP, cam.Port,
			cam.Username, cam.Password,
			cookies,
			cam.Channel, cam.Subtype,
			track, m.keepaliveInterval,
		)
	}
}

func (m *Manager) activeProtocol() settings.Protocol {
	if m.settings != nil {
		return m.settings.Get().StreamProtocol
	}
	return settings.ProtocolWS
}
