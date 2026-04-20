package web

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jsoc/camviewer/internal/store"
	"github.com/jsoc/camviewer/internal/streaming"
)

type Server struct {
	router    *chi.Mux
	templates map[string]*template.Template // one set per page
	store     *store.Store
	manager   *streaming.Manager
	rtspHost  string
	rtspPort  int
	prefix    string
}

func NewServer(
	st *store.Store,
	mgr *streaming.Manager,
	rtspHost string,
	rtspPort int,
	prefix string,
	staticFS http.FileSystem,
) *Server {
	srv := &Server{
		store:    st,
		manager:  mgr,
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

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(staticFS)))

	r.Get("/", s.handleDashboard)
	r.Get("/discover", s.handleDiscoverPage)
	r.Get("/config", s.handleConfigPage)
	r.Get("/cameras/{id}/login", s.handleLoginPage)
	r.Get("/cameras/{id}/view", s.handleViewPage)

	r.Get("/ws/stream/{streamKey}", s.handleWSStream)
	r.Get("/ws/camera/{id}", s.handleWSStreamByID)

	r.Post("/api/discover", s.apiDiscover)
	r.Get("/api/cameras", s.apiListCameras)
	r.Post("/api/cameras", s.apiAddCamera)
	r.Get("/api/cameras/{id}", s.apiGetCamera)
	r.Delete("/api/cameras/{id}", s.apiDeleteCamera)
	r.Post("/api/cameras/{id}/login", s.apiLogin)
	r.Post("/api/cameras/{id}/restart", s.apiRestart)

	return r
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
	}
	pub.Password = ""
	if pub.HasCredentials {
		pub.StreamRTSPURL = fmt.Sprintf("rtsp://%s:%d/%s/%s", host, s.rtspPort, s.prefix, cam.StreamKey)
	}
	return pub
}

// buildTemplates creates one template.Template per page using Clone().
//
// Go's html/template requires Clone() to get independent {{block}} override
// namespaces. Simply calling t.New(name).Parse() without Clone shares the
// block namespace across all pages in the same associated set, causing
// {{define "content"}} from one page to overwrite another's.
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

	// Correct Go template inheritance pattern:
	//   1. Parse baseTmpl into a named "base.html" template.
	//   2. Clone() it for each page — Clone() deep-copies the block namespace so
	//      each page gets an independent registry.
	//   3. Parse() just the page body ({{define}} blocks only) into that clone —
	//      this overrides the block defaults without touching any other clone.
	base := template.Must(
		template.New("base.html").Funcs(funcs).Parse(baseTmpl),
	)

	pages := map[string]string{
		"dashboard.html": dashboardTmpl,
		"discover.html":  discoverTmpl,
		"config.html":    configTmpl,
		"login.html":     loginTmpl,
		"viewer.html":    viewerTmpl,
	}

	sets := make(map[string]*template.Template, len(pages))
	for name, body := range pages {
		clone := template.Must(base.Clone())
		template.Must(clone.Parse(body))
		sets[name] = clone
	}
	return sets
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
