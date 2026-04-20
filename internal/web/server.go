package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jsoc/camviewer/internal/ptz"
	"github.com/jsoc/camviewer/internal/settings"
	"github.com/jsoc/camviewer/internal/store"
	"github.com/jsoc/camviewer/internal/streaming"
)

type Server struct {
	router    *chi.Mux
	templates map[string]*template.Template // one set per page
	store     *store.Store
	manager   *streaming.Manager
	ptz       *ptz.Manager
	settings  *settings.Store
	sessions  *sessionStore
	password  string
	rtspHost  string
	rtspPort  int
	prefix    string
}

func NewServer(
	st *store.Store,
	mgr *streaming.Manager,
	ptzMgr *ptz.Manager,
	sett *settings.Store,
	password string,
	rtspHost string,
	rtspPort int,
	prefix string,
	staticFS http.FileSystem,
) *Server {
	srv := &Server{
		store:    st,
		manager:  mgr,
		ptz:      ptzMgr,
		settings: sett,
		sessions: newSessionStore(),
		password: password,
		rtspHost: rtspHost,
		rtspPort: rtspPort,
		prefix:   prefix,
	}
	srv.templates = buildTemplates()
	srv.router = srv.buildRouter(staticFS)
	return srv
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) buildRouter(staticFS http.FileSystem) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Public: static assets and app login — no auth required.
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(staticFS)))
	r.Get("/ui/login", s.handleAppLoginPage)
	r.Post("/ui/login", s.handleAppLoginPost)

	// Everything else requires a valid session.
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)

		r.Post("/ui/logout", s.handleAppLogout)

		// ── Page routes ───────────────────────────────────────────────────────
		r.Get("/", s.handleDashboard)
		r.Get("/discover", s.handleDiscoverPage)
		r.Get("/config", s.handleConfigPage)
		r.Get("/preferences", s.handlePreferencesPage)
		r.Get("/cameras/{id}/login", s.handleLoginPage)
		r.Get("/cameras/{id}/view", s.handleViewPage)
		r.Get("/cameras/{id}/direct", s.handleDirectPage)

		// ── WebSocket streams ─────────────────────────────────────────────────
		r.Get("/ws/stream/{streamKey}", s.handleWSStream)
		r.Get("/ws/camera/{id}", s.handleWSStreamByID)

		// ── MJPEG proxy ───────────────────────────────────────────────────────
		r.Get("/proxy/cameras/{id}/stream", s.handleMJPEGProxy)

		// ── API ───────────────────────────────────────────────────────────────
		r.Post("/api/discover", s.apiDiscover)
		r.Get("/api/cameras", s.apiListCameras)
		r.Post("/api/cameras", s.apiAddCamera)
		r.Get("/api/cameras/{id}", s.apiGetCamera)
		r.Delete("/api/cameras/{id}", s.apiDeleteCamera)
		r.Post("/api/cameras/{id}/login", s.apiLogin)
		r.Post("/api/cameras/{id}/restart", s.apiRestart)
		r.Post("/api/cameras/{id}/onvif-login", s.apiONVIFLogin)
		r.Post("/api/cameras/{id}/ptz", s.apiPTZ)
		r.Get("/api/settings", s.apiGetSettings)
		r.Post("/api/settings", s.apiUpdateSettings)
		r.Post("/api/change-password", s.apiChangePassword)
	})

	return r
}

// handleDirectPage serves the fullscreen direct-stream page (no sidebar).
func (s *Server) handleDirectPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, ok := s.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderPlain(w, "direct.html", map[string]interface{}{"Camera": s.toPublic(cam)})
}

// effectivePassword returns the active login password.
// A password set via the UI (stored in settings) takes priority over the
// startup-generated / environment-variable password.
func (s *Server) effectivePassword() string {
	if p := s.settings.Get().AdminPassword; p != "" {
		return p
	}
	return s.password
}

func (s *Server) toPublic(cam *store.Camera) store.CameraPublic {
	return s.toPublicHost(cam, s.rtspHost)
}

func (s *Server) toPublicHost(cam *store.Camera, host string) store.CameraPublic {
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	pub := store.CameraPublic{
		Camera:         *cam,
		HasCredentials: cam.Username != "" && cam.Password != "",
		Health:         s.manager.Health(cam.ID),
		HasPTZ:         s.ptz.Has(cam.ID),
	}
	pub.Password = ""
	pub.ONVIFPassword = ""
	if pub.HasCredentials {
		pub.StreamRTSPURL = fmt.Sprintf("rtsp://%s:%d/%s/%s", host, s.rtspPort, s.prefix, cam.StreamKey)
	}
	return pub
}

// buildTemplates creates one template.Template per page.
//
// Correct Go template inheritance pattern:
//  1. Parse baseTmpl into a named "base.html" template.
//  2. Clone() for each page — gives each page an independent block namespace.
//  3. Parse() just the page body ({{define}} blocks) into that clone.
func buildTemplates() map[string]*template.Template {
	funcs := template.FuncMap{
		"inc": func(i int) int { return i + 1 },
		"not": func(b bool) bool { return !b },
		"emptySlots": func(n int) []struct{} {
			for _, size := range []int{1, 4, 9, 16} {
				if n <= size {
					return make([]struct{}, size-n)
				}
			}
			return nil
		},
	}

	base := template.Must(
		template.New("base.html").Funcs(funcs).Parse(baseTmpl),
	)

	pages := map[string]string{
		"dashboard.html":   dashboardTmpl,
		"discover.html":    discoverTmpl,
		"config.html":      configTmpl,
		"login.html":       loginTmpl,
		"viewer.html":      viewerTmpl,
		"preferences.html": preferencesTmpl,
	}

	sets := make(map[string]*template.Template, len(pages)+2)
	for name, body := range pages {
		clone := template.Must(base.Clone())
		template.Must(clone.Parse(body))
		sets[name] = clone
	}

	// Standalone templates — no sidebar.
	sets["app-login.html"] = template.Must(
		template.New("app-login.html").Parse(appLoginTmpl),
	)
	sets["direct.html"] = template.Must(
		template.New("direct.html").Parse(directTmpl),
	)
	return sets
}

// renderPlain executes a standalone template (no base.html wrapper).
func (s *Server) renderPlain(w http.ResponseWriter, name string, data any) {
	t, ok := s.templates[name]
	if !ok {
		http.Error(w, "unknown template: "+name, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		log.Printf("[web] template %s: %v", name, err)
	}
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	t, ok := s.templates[name]
	if !ok {
		http.Error(w, "unknown template: "+name, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base.html", data); err != nil {
		log.Printf("[web] template %s: %v", name, err)
		http.Error(w, "render error", 500)
	}
}
