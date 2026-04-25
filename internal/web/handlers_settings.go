package web

import (
	"encoding/json"
	"net/http"

	"github.com/jsoc/camviewer/internal/autostart"
	"github.com/jsoc/camviewer/internal/settings"
	"github.com/jsoc/camviewer/internal/store"
	"github.com/jsoc/camviewer/internal/streaming"
)

// handlePreferencesPage renders the Preferences page.
func (s *Server) handlePreferencesPage(w http.ResponseWriter, r *http.Request) {
	autostartEnabled, _ := autostart.IsEnabled()
	sett := s.settings.Get()
	sett.AutoStart = autostartEnabled // reflect real OS state
	s.render(w, "preferences.html", page("preferences", map[string]interface{}{
		"Settings": sett,
	}))
}

// apiGetSettings returns the current settings as JSON.
func (s *Server) apiGetSettings(w http.ResponseWriter, r *http.Request) {
	sett := s.settings.Get()
	// Reflect live OS autostart state.
	if enabled, err := autostart.IsEnabled(); err == nil {
		sett.AutoStart = enabled
	}
	jsonOK(w, sett)
}

// apiUpdateSettings applies a full settings update and handles side effects.
func (s *Server) apiUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var patch settings.Settings
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, "invalid JSON", 400)
		return
	}

	current := s.settings.Get()

	// ── AutoStart side effect ────────────────────────────────────────────────
	if patch.AutoStart != current.AutoStart {
		var autostartErr error
		if patch.AutoStart {
			autostartErr = autostart.Enable()
		} else {
			autostartErr = autostart.Disable()
		}
		if autostartErr != nil {
			jsonError(w, "autostart: "+autostartErr.Error(), 500)
			return
		}
	}

	// ── DirectStreamMode side effect ─────────────────────────────────────────
	if patch.DirectStreamMode != current.DirectStreamMode {
		if patch.DirectStreamMode {
			// Stop all running streams — browser will talk directly to cameras.
			s.manager.StopAll()
		} else {
			// Resume streaming for every camera that has credentials.
			for _, cam := range s.store.List() {
				if cam.Username != "" && cam.Password != "" && cam.Enabled {
					c := cam
					go s.manager.Start(c, nil)
				}
			}
		}
	}

	if err := s.settings.Update(patch); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, s.settings.Get())
}

// apiChangePassword validates the current password and sets a new one.
// All existing sessions are invalidated so every connected client must re-login.
func (s *Server) apiChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", 400)
		return
	}

	if !passwordsMatch(body.Current, s.effectivePassword()) {
		jsonError(w, "Current password is incorrect.", 401)
		return
	}
	if len(body.New) < 6 {
		jsonError(w, "New password must be at least 6 characters.", 400)
		return
	}
	if body.New != body.Confirm {
		jsonError(w, "New passwords do not match.", 400)
		return
	}

	sett := s.settings.Get()
	sett.AdminPassword = body.New
	if err := s.settings.Update(sett); err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	// Invalidate all sessions — callers must re-login with the new password.
	s.sessions.invalidateAll()

	jsonOK(w, map[string]bool{"ok": true})
}

// handleHealthPage renders the Stream Health page.
func (s *Server) handleHealthPage(w http.ResponseWriter, r *http.Request) {
	sett := s.settings.Get()
	cameras := s.store.List()
	pubs := make([]store.CameraPublic, 0, len(cameras))
	for _, c := range cameras {
		pubs = append(pubs, s.toPublic(c))
	}
	s.render(w, "health.html", page("health", map[string]interface{}{
		"Cameras":          pubs,
		"HealthMonitoring": sett.HealthMonitoring,
	}))
}

// apiStreamHealth returns diagnostics for all active streams as JSON.
func (s *Server) apiStreamHealth(w http.ResponseWriter, r *http.Request) {
	sett := s.settings.Get()
	if !sett.HealthMonitoring {
		jsonOK(w, map[string]string{"status": "disabled"})
		return
	}

	diags := s.manager.AllDiags()
	cameras := s.store.List()

	type cameraHealth struct {
		ID             string              `json:"id"`
		Name           string              `json:"name"`
		IP             string              `json:"ip"`
		Port           int                 `json:"port"`
		HasCredentials bool                `json:"has_credentials"`
		Diag           streaming.StreamDiag `json:"diag"`
	}

	out := make([]cameraHealth, 0, len(cameras))
	for _, cam := range cameras {
		d, ok := diags[cam.ID]
		if !ok {
			d = streaming.StreamDiag{Health: store.HealthUnknown}
		}
		out = append(out, cameraHealth{
			ID:             cam.ID,
			Name:           cam.Name,
			IP:             cam.IP,
			Port:           cam.Port,
			HasCredentials: cam.Username != "" && cam.Password != "",
			Diag:           d,
		})
	}
	jsonOK(w, out)
}

