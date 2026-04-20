package web

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// proxyClient has a dial timeout but no overall request timeout — MJPEG streams run indefinitely.
var proxyClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
	},
}

// handleMJPEGProxy fetches a Dahua/CP-Plus MJPEG stream from the camera and
// relays it verbatim to the browser.  The camera URL pattern works for both
// Dahua and CP-Plus (same firmware).
//
// Route: GET /proxy/cameras/{id}/stream
func (s *Server) handleMJPEGProxy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cam, ok := s.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if cam.Username == "" || cam.Password == "" {
		http.Error(w, "camera not configured", http.StatusBadRequest)
		return
	}

	// Dahua / CP-Plus MJPEG sub-stream endpoint.
	mjpegURL := fmt.Sprintf("http://%s:%d/cgi-bin/mjpg/video.cgi?channel=%d&subtype=%d",
		cam.IP, cam.Port, cam.Channel, cam.Subtype)

	req, err := http.NewRequestWithContext(r.Context(), "GET", mjpegURL, nil)
	if err != nil {
		http.Error(w, "proxy error", http.StatusInternalServerError)
		return
	}
	req.SetBasicAuth(cam.Username, cam.Password)
	req.Header.Set("User-Agent", "JSOC-CamViewer/1.0")

	resp, err := proxyClient.Do(req)
	if err != nil {
		http.Error(w, "camera unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Forward camera headers (Content-Type for multipart/x-mixed-replace is essential).
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Stream bytes; io.Copy returns when the client disconnects or camera drops.
	io.Copy(w, resp.Body) //nolint:errcheck
}
