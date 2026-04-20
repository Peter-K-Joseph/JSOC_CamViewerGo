package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jsoc/camviewer/internal/config"
	"github.com/jsoc/camviewer/internal/netutil"
	"github.com/jsoc/camviewer/internal/rtsp"
	"github.com/jsoc/camviewer/internal/store"
	"github.com/jsoc/camviewer/internal/streaming"
	"github.com/jsoc/camviewer/internal/web"
)

func main() {
	cfg := config.Load()

	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	st, err := store.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	mgr := streaming.NewManager(time.Duration(cfg.NativeWSKeepaliveInterval * float64(time.Second)))

	for _, cam := range st.List() {
		if cam.Username != "" && cam.Password != "" && cam.Enabled {
			go mgr.Start(cam, nil)
		}
	}

	// Bind both servers before starting either, so we know the actual ports.
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

	staticFS := http.Dir("static")
	webSrv := web.NewServer(st, mgr, cfg.RTSPHost, rtspPort, cfg.StreamPathPrefix, staticFS)

	log.Printf("[jsoc] HTTP → http://%s:%d", cfg.HTTPHost, httpPort)
	log.Printf("[jsoc] Data → %s", cfg.DataDir)

	if err := http.Serve(httpLn, webSrv.Handler()); err != nil {
		log.Fatalf("http serve: %v", err)
	}
}
