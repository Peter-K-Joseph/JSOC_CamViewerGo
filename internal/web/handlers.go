package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jsoc/camviewer/internal/auth"
	"github.com/jsoc/camviewer/internal/discovery"
	"github.com/jsoc/camviewer/internal/store"
)

// ─── Page handlers ──────────────────────────────────────────────────────────

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	host := resolveHost(r, s.rtspHost)
	cameras := s.store.List()
	pubs := make([]store.CameraPublic, 0, len(cameras))
	for _, c := range cameras {
		pubs = append(pubs, s.toPublicHost(c, host))
	}
	sett := s.settings.Get()
	s.render(w, "dashboard.html", page("dashboard", map[string]interface{}{
		"Cameras":        pubs,
		"DirectMode":     sett.DirectStreamMode,
		"DirectWindowed": sett.DirectStreamWindowed,
	}))
}

func (s *Server) handleDiscoverPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "discover.html", page("discover", nil))
}

func (s *Server) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	cameras := s.store.List()
	pubs := make([]store.CameraPublic, 0, len(cameras))
	for _, c := range cameras {
		pubs = append(pubs, s.toPublic(c))
	}
	s.render(w, "config.html", page("config", map[string]interface{}{"Cameras": pubs}))
}

// page merges a "Page" key into a data map for sidebar active-link highlighting.
func page(name string, data map[string]interface{}) map[string]interface{} {
	if data == nil {
		data = map[string]interface{}{}
	}
	data["Page"] = name
	return data
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, ok := s.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "login.html", page("", map[string]interface{}{"Camera": s.toPublic(cam)}))
}

func (s *Server) handleViewPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, ok := s.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	host := resolveHost(r, s.rtspHost)
	rtspURL := fmt.Sprintf("rtsp://%s:%d/%s/%s", host, s.rtspPort, s.prefix, cam.StreamKey)
	s.render(w, "viewer.html", page("", map[string]interface{}{
		"Camera":  s.toPublicHost(cam, host),
		"RTSPURL": rtspURL,
	}))
}

// ─── API handlers ────────────────────────────────────────────────────────────

func (s *Server) apiDiscover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TimeoutS int `json:"timeout_s"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.TimeoutS <= 0 || body.TimeoutS > 15 {
		body.TimeoutS = 5
	}
	devices, err := discovery.Discover(time.Duration(body.TimeoutS) * time.Second)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, devices)
}

func (s *Server) apiListCameras(w http.ResponseWriter, r *http.Request) {
	host := resolveHost(r, s.rtspHost)
	cameras := s.store.List()
	pubs := make([]store.CameraPublic, 0, len(cameras))
	for _, c := range cameras {
		pubs = append(pubs, s.toPublicHost(c, host))
	}
	jsonOK(w, pubs)
}

func (s *Server) apiAddCamera(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string `json:"name"`
		IP           string `json:"ip"`
		Port         int    `json:"port"`
		Manufacturer string `json:"manufacturer"`
		Model        string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", 400)
		return
	}
	if body.Name == "" || body.IP == "" {
		jsonError(w, "name and ip required", 400)
		return
	}
	if body.Port == 0 {
		body.Port = 80
	}
	cam, err := s.store.Add(body.Name, body.IP, body.Port, body.Manufacturer, body.Model)
	if err != nil {
		code := 500
		if strings.HasPrefix(err.Error(), "invalid camera address") {
			code = 400
		}
		jsonError(w, err.Error(), code)
		return
	}
	w.WriteHeader(201)
	jsonOK(w, s.toPublic(cam))
}

func (s *Server) apiGetCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, ok := s.store.Get(id)
	if !ok {
		jsonError(w, "not found", 404)
		return
	}
	jsonOK(w, s.toPublic(cam))
}

func (s *Server) apiDeleteCamera(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.manager.Stop(id)
	if err := s.store.Delete(id); err != nil {
		jsonError(w, err.Error(), 404)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", 400)
		return
	}
	cam, ok := s.store.Get(id)
	if !ok {
		jsonError(w, "not found", 404)
		return
	}
	sess, err := auth.Login(cam.IP, cam.Port, body.Username, body.Password)
	if err != nil {
		jsonError(w, fmt.Sprintf("auth failed: %v", err), 401)
		return
	}
	updated, err := s.store.UpdateCredentials(id, body.Username, body.Password)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	s.manager.Start(updated, sess.Cookies)
	jsonOK(w, s.toPublic(updated))
}

func (s *Server) apiRestart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, ok := s.store.Get(id)
	if !ok {
		jsonError(w, "not found", 404)
		return
	}
	if cam.Username == "" || cam.Password == "" {
		jsonError(w, "no credentials stored", 400)
		return
	}
	sess, err := auth.Login(cam.IP, cam.Port, cam.Username, cam.Password)
	if err != nil {
		jsonError(w, fmt.Sprintf("re-auth failed: %v", err), 401)
		return
	}
	s.manager.Start(cam, sess.Cookies)
	jsonOK(w, s.toPublic(cam))
}

// apiONVIFLogin probes a camera's ONVIF service with the supplied credentials.
// On success it saves those credentials, marks PTZ enabled, and loads the
// client into the PTZ manager.  If the probe fails the stream continues
// working — PTZ is simply unavailable.
func (s *Server) apiONVIFLogin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", 400)
		return
	}
	cam, ok := s.store.Get(id)
	if !ok {
		jsonError(w, "not found", 404)
		return
	}

	// Fall back to stream credentials if ONVIF fields left empty.
	username := body.Username
	if username == "" {
		username = cam.Username
	}
	password := body.Password
	if password == "" {
		password = cam.Password
	}

	if err := s.ptz.Load(id, cam.IP, cam.Port, username, password); err != nil {
		jsonError(w, fmt.Sprintf("ONVIF probe failed: %v", err), 422)
		return
	}

	updated, err := s.store.UpdateONVIFCredentials(id, username, password, true)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	jsonOK(w, map[string]interface{}{"ok": true, "has_ptz": updated.PTZEnabled})
}

// apiPTZ dispatches PTZ / focus commands to the camera's ONVIF client.
// Body: {"action":"move","pan":0.5,"tilt":0,"zoom":0}
//
//	{"action":"stop"}
//	{"action":"focus","speed":0.5}   (+= far, -= near)
//	{"action":"focus-stop"}
//	{"action":"focus-auto"}
//	{"action":"focus-manual"}
func (s *Server) apiPTZ(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Action string  `json:"action"`
		Pan    float64 `json:"pan"`
		Tilt   float64 `json:"tilt"`
		Zoom   float64 `json:"zoom"`
		Speed  float64 `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", 400)
		return
	}

	client := s.ptz.Get(id)
	if client == nil {
		jsonError(w, "PTZ not configured for this camera", 404)
		return
	}

	var err error
	switch body.Action {
	case "move":
		err = client.ContinuousMove(clamp(body.Pan), clamp(body.Tilt), clamp(body.Zoom))
	case "stop":
		err = client.Stop()
	case "focus":
		err = client.FocusMove(clamp(body.Speed))
	case "focus-stop":
		err = client.FocusStop()
	case "focus-auto":
		err = client.SetFocusAuto(true)
	case "focus-manual":
		err = client.SetFocusAuto(false)
	default:
		jsonError(w, "unknown action: "+body.Action, 400)
		return
	}
	if err != nil {
		jsonError(w, err.Error(), 502)
		return
	}
	jsonOK(w, map[string]bool{"ok": true})
}

func clamp(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func resolveHost(r *http.Request, configured string) string {
	if configured != "" && configured != "0.0.0.0" {
		return configured
	}
	host := r.Host
	if strings.Contains(host, ":") {
		h, _, err := net.SplitHostPort(host)
		if err == nil {
			return h
		}
	}
	return host
}
