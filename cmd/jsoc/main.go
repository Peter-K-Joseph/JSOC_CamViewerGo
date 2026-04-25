package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jsoc/camviewer/internal/auth"
	"github.com/jsoc/camviewer/internal/config"
	"github.com/jsoc/camviewer/internal/netutil"
	"github.com/jsoc/camviewer/internal/ptz"
	"github.com/jsoc/camviewer/internal/rtsp"
	"github.com/jsoc/camviewer/internal/settings"
	"github.com/jsoc/camviewer/internal/store"
	"github.com/jsoc/camviewer/internal/streaming"
	"github.com/jsoc/camviewer/internal/web"
)

//go:embed static
var staticFiles embed.FS

func main() {
	cfg := config.Load()

	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	if err := setupFileLogging(cfg.DataDir); err != nil {
		log.Fatalf("setup file logging: %v", err)
	}

	// ── Persistent stores ─────────────────────────────────────────────────────
	st, err := store.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	sett, err := settings.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("open settings: %v", err)
	}
	startupPassword := resolveStartupPassword(cfg.AdminPassword, sett.Get().AdminPassword)
	if err := sett.EnsureAdminPassword(startupPassword); err != nil {
		log.Fatalf("persist admin password: %v", err)
	}

	// ── Streaming manager ─────────────────────────────────────────────────────
	mgr := streaming.NewManager(
		time.Duration(cfg.NativeWSKeepaliveInterval*float64(time.Second)),
		sett,
	)
	ptzMgr := ptz.NewManager()

	// ── Start camera streams (skipped in direct-stream mode) ──────────────────
	currentSettings := sett.Get()
	for _, cam := range st.List() {
		if cam.Username != "" && cam.Password != "" && cam.Enabled {
			if !currentSettings.DirectStreamMode {
				go func(c *store.Camera) {
					// Re-authenticate to obtain fresh session cookies.
					// Without cookies the WebSocket source cannot connect
					// to Dahua cameras' /rtspoverwebsocket endpoint.
					var cookies []*http.Cookie
					sess, err := auth.LoginWithFallback(c.IP, c.Port, c.Username, c.Password)
					if err != nil {
						log.Printf("[startup] auth %s (%s): %v — starting without cookies", c.Name, c.IP, err)
					} else {
						cookies = sess.Cookies
					}
					mgr.Start(c, cookies)
				}(cam)
			}
		}
		// Restore PTZ clients for cameras that had ONVIF configured previously.
		if cam.PTZEnabled && cam.ONVIFUsername != "" {
			go func(c *store.Camera) {
				if err := ptzMgr.Load(c.ID, c.IP, c.Port, c.ONVIFUsername, c.ONVIFPassword); err != nil {
					log.Printf("[ptz] restore %s (%s): %v", c.Name, c.IP, err)
				}
			}(cam)
		}
	}

	// ── Bind both servers before starting either ───────────────────────────────
	rtspLn, rtspPort, err := netutil.ListenTCP("0.0.0.0", cfg.RTSPPort)
	if err != nil {
		log.Fatalf("rtsp bind: %v", err)
	}
	if rtspPort != cfg.RTSPPort {
		log.Printf("[rtsp] port %d busy, using %d", cfg.RTSPPort, rtspPort)
	}

	httpLn, httpPort, err := netutil.ListenTCP(cfg.HTTPHost, cfg.HTTPPort)
	if err != nil {
		log.Fatalf("http bind: %v", err)
	}
	if httpPort != cfg.HTTPPort {
		log.Printf("[http] port %d busy, using %d", cfg.HTTPPort, httpPort)
	}

	go func() {
		log.Printf("[rtsp] → rtsp://%s:%d/%s/<stream-key>", cfg.RTSPHost, rtspPort, cfg.StreamPathPrefix)
		if err := rtsp.Serve(rtspLn, mgr.Track, cfg.RTSPHost, cfg.StreamPathPrefix); err != nil {
			log.Fatalf("rtsp serve: %v", err)
		}
	}()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("embed static: %v", err)
	}
	staticFS := http.FS(sub)
	webSrv := web.NewServer(
		st, mgr, ptzMgr, sett,
		startupPassword,
		cfg.RTSPHost, rtspPort, cfg.StreamPathPrefix,
		staticFS,
	)

	log.Printf("[jsoc] HTTP → http://%s:%d", cfg.HTTPHost, httpPort)
	log.Printf("[jsoc] Data → %s", cfg.DataDir)
	if currentSettings.DirectStreamMode {
		log.Printf("[jsoc] Direct stream mode active — server pipeline disabled")
	}

	if err := http.Serve(httpLn, webSrv.Handler()); err != nil {
		log.Fatalf("http serve: %v", err)
	}
}

func resolveStartupPassword(envPassword, storedPassword string) string {
	if envPassword != "" {
		return envPassword
	}
	if storedPassword != "" {
		return storedPassword
	}
	pw := randomPassword()
	log.Printf("╔══════════════════════════════════════════╗")
	log.Printf("║  JSOC NVR — no JSOC_PASSWORD set         ║")
	log.Printf("║  Generated password: %-20s  ║", pw)
	log.Printf("║  Saved in settings.json for reuse        ║")
	log.Printf("╚══════════════════════════════════════════╝")
	return pw
}

func randomPassword() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "changeme"
	}
	return hex.EncodeToString(b)
}

func setupFileLogging(dataDir string) error {
	logsDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logsDir, 0700); err != nil {
		return err
	}
	allFile, err := os.OpenFile(filepath.Join(logsDir, "system.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	errFile, err := os.OpenFile(filepath.Join(logsDir, "system.error"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		allFile.Close()
		return err
	}

	log.SetOutput(&splitLogWriter{
		all: io.MultiWriter(os.Stdout, allFile),
		err: io.MultiWriter(os.Stderr, errFile),
	})
	return nil
}

type splitLogWriter struct {
	all io.Writer
	err io.Writer
}

func (w *splitLogWriter) Write(p []byte) (int, error) {
	n, err := w.all.Write(p)
	if isErrorLogLine(p) {
		_, _ = w.err.Write(p)
	}
	return n, err
}

func isErrorLogLine(p []byte) bool {
	low := bytes.ToLower(p)
	return bytes.Contains(low, []byte(" error")) ||
		bytes.Contains(low, []byte("failed")) ||
		bytes.Contains(low, []byte("fatal")) ||
		bytes.Contains(low, []byte("panic")) ||
		bytes.Contains(low, []byte("unauthorized"))
}
