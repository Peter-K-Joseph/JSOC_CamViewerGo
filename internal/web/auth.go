package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName = "jsoc_session"
	sessionTTL        = 24 * time.Hour
	sessionGCInterval = time.Hour
)

// ── In-memory session store ──────────────────────────────────────────────────

type sessionStore struct {
	mu     sync.RWMutex
	tokens map[string]time.Time // token → expiry
}

func newSessionStore() *sessionStore {
	s := &sessionStore{tokens: make(map[string]time.Time)}
	go s.gc()
	return s
}

func (s *sessionStore) create() string {
	b := make([]byte, 32)
	rand.Read(b) //nolint:errcheck // crypto/rand.Read never fails on supported OS
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.tokens[token] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return token
}

func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.RLock()
	exp, ok := s.tokens[token]
	s.mu.RUnlock()
	return ok && time.Now().Before(exp)
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

func (s *sessionStore) invalidateAll() {
	s.mu.Lock()
	s.tokens = make(map[string]time.Time)
	s.mu.Unlock()
}

func (s *sessionStore) gc() {
	for range time.Tick(sessionGCInterval) {
		now := time.Now()
		s.mu.Lock()
		for k, exp := range s.tokens {
			if now.After(exp) {
				delete(s.tokens, k)
			}
		}
		s.mu.Unlock()
	}
}

// ── Middleware ────────────────────────────────────────────────────────────────

// requireAuth returns a middleware that enforces session authentication.
// - API paths (/api/, /ws/) get a 401 JSON response.
// - All other paths are redirected to /ui/login.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !s.sessions.valid(cookie.Value) {
			if isAPIorWS(r.URL.Path) {
				jsonError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			target := "/ui/login"
			if r.URL.Path != "/" {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAPIorWS(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/")
}

// ── Login / logout page handlers ─────────────────────────────────────────────

func (s *Server) handleAppLoginPage(w http.ResponseWriter, r *http.Request) {
	// If already logged in, go to dashboard.
	if cookie, err := r.Cookie(sessionCookieName); err == nil && s.sessions.valid(cookie.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := r.URL.Query().Get("next")
	s.renderPlain(w, "app-login.html", map[string]interface{}{
		"Next":  next,
		"Error": "",
	})
}

func (s *Server) handleAppLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	next := r.FormValue("next")

	if password != s.effectivePassword() {
		s.renderPlain(w, "app-login.html", map[string]interface{}{
			"Next":  next,
			"Error": "Incorrect password.",
		})
		return
	}

	token := s.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	redirect := "/"
	if next != "" && strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		redirect = next
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

func (s *Server) handleAppLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}
