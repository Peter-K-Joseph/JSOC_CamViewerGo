package web

import (
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// proxyClient has a dial timeout but no overall request timeout — MJPEG streams run indefinitely.
var proxyClient = &http.Client{
	// Disable automatic redirect following so we can handle auth ourselves.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
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

	// If the camera returned 401 with a Digest challenge, retry with Digest auth.
	if resp.StatusCode == 401 {
		resp.Body.Close()
		challenge := resp.Header.Get("WWW-Authenticate")
		if digestAuth := proxyParseDigest(challenge, cam.Username, cam.Password); digestAuth != "" {
			req2, err := http.NewRequestWithContext(r.Context(), "GET", mjpegURL, nil)
			if err == nil {
				req2.Header.Set("Authorization", digestAuth)
				req2.Header.Set("User-Agent", "JSOC-CamViewer/1.0")
				resp2, err := proxyClient.Do(req2)
				if err != nil {
					http.Error(w, "camera unreachable: "+err.Error(), http.StatusBadGateway)
					return
				}
				resp = resp2
				// fall through to response handling below
			}
		}
	}
	defer resp.Body.Close()

	// NEVER forward 4xx from the camera — the browser interprets 401 as an
	// auth challenge and shows a native username/password popup.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		http.Error(w, "camera authentication failed", http.StatusBadGateway)
		return
	}

	// Forward only safe headers; hop-by-hop and security headers must not be forwarded.
	allowed := map[string]bool{"Content-Type": true, "Cache-Control": true}
	for k, vs := range resp.Header {
		if allowed[k] {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Stream bytes; io.Copy returns when the client disconnects or camera drops.
	io.Copy(w, resp.Body) //nolint:errcheck
}

// proxyParseDigest parses a WWW-Authenticate Digest challenge and returns the
// Authorization header value, or "" if the challenge is not Digest.
func proxyParseDigest(challenge, username, password string) string {
	lower := strings.ToLower(challenge)
	if !strings.HasPrefix(lower, "digest") {
		return ""
	}
	realm := proxyDigestField(challenge, "realm")
	nonce := proxyDigestField(challenge, "nonce")
	if realm == "" || nonce == "" {
		return ""
	}
	// RFC 2069 Digest (no qop).
	ha1 := fmt.Sprintf("%x", md5.Sum([]byte(username+":"+realm+":"+password)))
	ha2 := fmt.Sprintf("%x", md5.Sum([]byte("GET:"+"/cgi-bin/mjpg/video.cgi")))
	response := fmt.Sprintf("%x", md5.Sum([]byte(ha1+":"+nonce+":"+ha2)))
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		username, realm, nonce, "/cgi-bin/mjpg/video.cgi", response)
}

func proxyDigestField(header, name string) string {
	idx := strings.Index(strings.ToLower(header), strings.ToLower(name)+"=")
	if idx < 0 {
		return ""
	}
	val := header[idx+len(name)+1:]
	if len(val) > 0 && val[0] == '"' {
		val = val[1:]
		end := strings.IndexByte(val, '"')
		if end >= 0 {
			return val[:end]
		}
	}
	end := strings.IndexAny(val, ", ")
	if end >= 0 {
		return val[:end]
	}
	return val
}
