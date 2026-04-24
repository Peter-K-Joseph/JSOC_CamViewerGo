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
	proto := m.activeProtocol()

	// Select source based on current settings.
	src := m.buildSource(cam, cookies, track, proto)

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

		// ── Protocol fallback ────────────────────────────────────────────────
		// If the primary protocol failed and fallback is enabled, try the
		// alternate protocol (WS → RTSP or RTSP → WS).
		if m.shouldFallback(cam.ID, proto) {
			alt := m.fallbackProtocol(proto)
			log.Printf("[manager] primary %s failed for %s (%s), falling back to %s",
				proto, cam.ID, cam.Name, alt)
			altSrc := m.buildSource(cam, cookies, track, alt)

			m.mu.Lock()
			if e, ok := m.streams[cam.ID]; ok {
				e.source = altSrc
				e.health = store.HealthStarting
			}
			m.mu.Unlock()

			altSrc.Run()
		}

		m.setHealth(cam.ID, store.HealthOffline)
	}()

	// Flip to "ok" after a brief moment if still running.
	go func() {
		time.Sleep(3 * time.Second)
		m.mu.Lock()
		if e, ok := m.streams[cam.ID]; ok && e.health == store.HealthStarting {
			e.health = store.HealthOK
		}
		m.mu.Unlock()
	}()

	log.Printf("[manager] started stream for camera %s (%s) via %s", cam.ID, cam.Name, proto)
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

// buildSource creates the appropriate source type for the given protocol.
func (m *Manager) buildSource(cam *store.Camera, cookies []*http.Cookie, track *Track, proto settings.Protocol) cameraSource {
	switch proto {
	case settings.ProtocolRTSP:
		// RTSP uses port 554 by default, NOT the camera's HTTP port.
		return NewRtspSource(
			cam.IP, 554,
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

// shouldFallback checks whether the stream is still registered (not Stop'd)
// and protocol fallback is enabled in settings.
func (m *Manager) shouldFallback(camID string, primary settings.Protocol) bool {
	m.mu.RLock()
	_, registered := m.streams[camID]
	m.mu.RUnlock()
	if !registered {
		return false // Stop() was called — don't retry
	}
	if m.settings == nil {
		return false
	}
	s := m.settings.Get()
	if !s.StreamProtocolFallback {
		return false
	}
	// Only WS↔RTSP fallback makes sense; RTMP is push-only.
	return primary == settings.ProtocolWS || primary == settings.ProtocolRTSP
}

// fallbackProtocol returns the alternate protocol for a given primary.
func (m *Manager) fallbackProtocol(primary settings.Protocol) settings.Protocol {
	if primary == settings.ProtocolWS {
		return settings.ProtocolRTSP
	}
	return settings.ProtocolWS
}

func (m *Manager) activeProtocol() settings.Protocol {
	if m.settings != nil {
		return m.settings.Get().StreamProtocol
	}
	return settings.ProtocolWS
}
