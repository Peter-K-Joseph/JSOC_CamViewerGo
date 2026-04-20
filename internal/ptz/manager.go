package ptz

import (
	"log"
	"sync"
)

// Manager holds one Client per camera (keyed by camera ID).
// Clients are added after a successful ONVIF probe and removed when a camera
// is deleted.  All methods are safe for concurrent use.
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client // camera ID → client
}

// NewManager returns a ready Manager.
func NewManager() *Manager {
	return &Manager{clients: make(map[string]*Client)}
}

// Load probes the camera and stores the resulting client.
// If the probe fails the existing client (if any) is left unchanged and the
// error is returned to the caller.
func (m *Manager) Load(id, ip string, port int, username, password string) error {
	c, err := Probe(ip, port, username, password)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.clients[id] = c
	m.mu.Unlock()
	log.Printf("[ptz] camera %s: PTZ ready (profile=%s vs=%s)", id, c.ProfileToken, c.VSToken)
	return nil
}

// Get returns the client for a camera, or nil if none is registered.
func (m *Manager) Get(id string) *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[id]
}

// Remove drops the client for a camera (e.g. on camera delete).
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	delete(m.clients, id)
	m.mu.Unlock()
}

// Has reports whether a PTZ client is registered for the given camera.
func (m *Manager) Has(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.clients[id]
	return ok
}
