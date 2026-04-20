package streaming

import (
	"bufio"
	"encoding/base64"
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// WsSource connects to a Dahua camera's /rtspoverwebsocket endpoint,
// performs the RTSP-over-WS handshake, and publishes AccessUnits to a Track.
type WsSource struct {
	host     string
	port     int
	username string
	password string
	cookies  []*http.Cookie
	channel  int
	subtype  int
	track    *Track

	stopCh  chan struct{}
	stopped atomic.Bool

	keepaliveInterval time.Duration
}

func NewWsSource(host string, port int, username, password string,
	cookies []*http.Cookie, channel, subtype int,
	track *Track, keepaliveInterval time.Duration) *WsSource {

	return &WsSource{
		host:              host,
		port:              port,
		username:          username,
		password:          password,
		cookies:           cookies,
		channel:           channel,
		subtype:           subtype,
		track:             track,
		stopCh:            make(chan struct{}),
		keepaliveInterval: keepaliveInterval,
	}
}

// Run starts the WebSocket ingestion loop. It reconnects on error until Stop() is called.
func (s *WsSource) Run() {
	backoff := time.Second
	for {
		if s.stopped.Load() {
			return
		}
		if err := s.runOnce(); err != nil {
			log.Printf("[ws_source] %s:%d error: %v — retry in %s", s.host, s.port, err, backoff)
		}
		select {
		case <-s.stopCh:
			return
		case <-time.After(backoff):
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}

func (s *WsSource) Stop() {
	if s.stopped.CompareAndSwap(false, true) {
		close(s.stopCh)
	}
}

func (s *WsSource) runOnce() error {
	wsURL := fmt.Sprintf("ws://%s:%d/rtspoverwebsocket", s.host, s.port)

	hdr := http.Header{}
	// Basic auth
	req, _ := http.NewRequest("GET", wsURL, nil)
	req.SetBasicAuth(s.username, s.password)
	hdr.Set("Authorization", req.Header.Get("Authorization"))
	// Forward session cookies in request format (name=value; name2=value2)
	var cookies []string
	for _, c := range s.cookies {
		cookies = append(cookies, fmt.Sprintf("%s=%s", c.Name, c.Value))
	}
	if len(cookies) > 0 {
		hdr.Set("Cookie", strings.Join(cookies, "; "))
		log.Printf("[ws_source] WebSocket headers: %d cookies: %v", len(s.cookies), strings.Join(cookies, "; "))
	} else {
		log.Printf("[ws_source] WARNING: No RPC2 session cookies available for WebSocket upgrade")
	}

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(wsURL, hdr)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[ws_source] connected to %s", wsURL)

	// ── RTSP handshake over WS ──
	cseq := 1
	var auth *rtspDigest

	// Use the browser-style unauthenticated RTSP URI, then answer the Digest
	// challenge inside the RTSP-over-WS tunnel.
	rtspBaseURL := fmt.Sprintf("rtsp://%s:%d/cam/realmonitor?channel=%d&subtype=%d&proto=Private3",
		s.host, s.port, s.channel, s.subtype)
	rtspStreamURL := rtspBaseURL

	_, cseq, auth, err = s.rtspRequest(conn, cseq, "OPTIONS", rtspBaseURL, rtspBaseURL, "", auth)
	if err != nil {
		return err
	}

	descResp, nextCSeq, nextAuth, err := s.rtspRequest(conn, cseq, "DESCRIBE", rtspBaseURL, rtspBaseURL, "Accept: application/sdp\r\n", auth)
	if err != nil {
		return err
	}
	cseq, auth = nextCSeq, nextAuth

	codec, payloadType, clockRate := parseSDPCodec(descResp)
	log.Printf("[ws_source] codec=%s pt=%d clock=%d", codec, payloadType, clockRate)
	if sps, pps, vps := parseSDPParameterSets(descResp); len(sps) > 0 || len(pps) > 0 || len(vps) > 0 {
		s.track.UpdateParams(codec, sps, pps, vps)
		log.Printf("[ws_source] seeded codec params from SDP: sps=%d pps=%d vps=%d", len(sps), len(pps), len(vps))
	}

	// SETUP — use the track control URL from the SDP; fall back to /trackID=0.
	setupURL := sdpTrackControl(descResp, rtspStreamURL)
	log.Printf("[ws_source] SETUP url=%s", setupURL)
	setupResp, nextCSeq, nextAuth, err := s.rtspRequest(conn, cseq, "SETUP", setupURL, rtspBaseURL, "Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n", auth)
	if err != nil {
		return err
	}
	cseq, auth = nextCSeq, nextAuth
	sessionID := parseSession(setupResp)

	// PLAY
	_, nextCSeq, auth, err = s.rtspRequest(conn, cseq, "PLAY", rtspBaseURL, rtspBaseURL, fmt.Sprintf("Session: %s\r\nRange: npt=0.000-\r\n", sessionID), auth)
	if err != nil {
		return err
	}
	cseq = nextCSeq

	log.Printf("[ws_source] streaming started session=%s", sessionID)

	// ── RTP read loop ──
	var h264dp H264Depacketizer
	var h265dp H265Depacketizer

	// writeMu serialises WebSocket writes: keepalive goroutine and the read
	// loop both call sendRTSP, but gorilla/websocket disallows concurrent writes.
	var writeMu sync.Mutex
	cseqMu := cseq // local copy owned by keepalive goroutine

	// keepalive goroutine — fires independently of the read loop so that a
	// silent camera (no frames) doesn't cause the 30-second read deadline to
	// expire before we send OPTIONS.
	kaStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(s.keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-kaStop:
				return
			case <-s.stopCh:
				return
			case <-ticker.C:
				writeMu.Lock()
				_ = s.sendRTSP(conn, buildRTSPRequest("OPTIONS", rtspBaseURL, cseqMu, fmt.Sprintf("Session: %s\r\n", sessionID), auth, rtspBaseURL))
				cseqMu++
				writeMu.Unlock()
			}
		}
	}()
	defer close(kaStop)

	// Read deadline is 3× the keepalive interval so we always get at least two
	// keepalive opportunities before the connection is considered dead.
	readDeadline := s.keepaliveInterval * 3
	if readDeadline < 60*time.Second {
		readDeadline = 60 * time.Second
	}

	for {
		select {
		case <-s.stopCh:
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(readDeadline))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("ws read: %w", err)
		}

		// Process all interleaved frames in the message
		data := msg
		for len(data) >= 4 {
			if data[0] != 0x24 {
				// Could be RTSP text (keepalive response) — skip to next $
				idx := bytes.IndexByte(data[1:], 0x24)
				if idx < 0 {
					break
				}
				data = data[idx+1:]
				continue
			}
			channel := data[1]
			frameLen := int(binary.BigEndian.Uint16(data[2:4]))
			data = data[4:]
			if frameLen > len(data) {
				break
			}
			frame := data[:frameLen]
			data = data[frameLen:]

			if channel != 0 {
				// RTCP on channel 1 — ignore
				continue
			}

			pkt, err := ParseRTPPacket(frame)
			if err != nil {
				continue
			}

			var aus []AccessUnit
			switch codec {
			case "h264":
				aus = h264dp.Push(pkt)
				if h264dp.SPS != nil || h264dp.PPS != nil {
					s.track.UpdateParams("h264", h264dp.SPS, h264dp.PPS, nil)
				}
			case "h265":
				aus = h265dp.Push(pkt)
				if h265dp.SPS != nil || h265dp.PPS != nil {
					s.track.UpdateParams("h265", h265dp.SPS, h265dp.PPS, h265dp.VPS)
				}
			}
			for _, au := range aus {
				s.track.Publish(au)
			}
		}
	}
}

// sendRTSP writes an RTSP request as a WebSocket text message.
// Dahua cameras expect RTSP control messages as text frames; binary causes them to ignore the request.
func (s *WsSource) sendRTSP(conn *websocket.Conn, msg string) error {
	return conn.WriteMessage(websocket.TextMessage, []byte(msg))
}

func (s *WsSource) rtspRequest(conn *websocket.Conn, cseq int, method, requestURI, authURI, extra string, auth *rtspDigest) (string, int, *rtspDigest, error) {
	resp, err := s.rtspExchange(conn, cseq, method, requestURI, authURI, extra, auth)
	if err != nil {
		return "", cseq, auth, err
	}
	code := rtspStatusCode(resp)
	log.Printf("[ws_source] %s: %s", method, rtspStatusLine(resp))
	if code == 401 {
		challenge := parseRTSPDigestChallenge(resp, s.username, s.password)
		if challenge == nil {
			return resp, cseq + 1, auth, fmt.Errorf("%s: 401 without Digest challenge", method)
		}
		cseq++
		resp, err = s.rtspExchange(conn, cseq, method, requestURI, authURI, extra, challenge)
		if err != nil {
			return "", cseq, challenge, err
		}
		code = rtspStatusCode(resp)
		log.Printf("[ws_source] %s auth: %s", method, rtspStatusLine(resp))
		if code != 200 {
			return resp, cseq + 1, challenge, fmt.Errorf("%s: status %d", method, code)
		}
		return resp, cseq + 1, challenge, nil
	}
	if code != 200 {
		return resp, cseq + 1, auth, fmt.Errorf("%s: status %d", method, code)
	}
	return resp, cseq + 1, auth, nil
}

func (s *WsSource) rtspExchange(conn *websocket.Conn, cseq int, method, requestURI, authURI, extra string, auth *rtspDigest) (string, error) {
	msg := buildRTSPRequest(method, requestURI, cseq, extra, auth, authURI)
	if err := s.sendRTSP(conn, msg); err != nil {
		return "", err
	}
	resp, err := s.readRTSP(conn)
	if err != nil {
		return "", fmt.Errorf("%s response: %w", method, err)
	}
	return resp, nil
}

func buildRTSPRequest(method, requestURI string, cseq int, extra string, auth *rtspDigest, authURI string) string {
	var sb strings.Builder
	sb.WriteString(method + " " + requestURI + " RTSP/1.0\r\n")
	fmt.Fprintf(&sb, "CSeq: %d\r\n", cseq)
	if auth != nil {
		sb.WriteString("Authorization: " + auth.header(method, authURI) + "\r\n")
	}
	sb.WriteString(extra)
	sb.WriteString("\r\n")
	return sb.String()
}

// readRTSP reads WebSocket messages until it assembles a complete RTSP response.
func (s *WsSource) readRTSP(conn *websocket.Conn) (string, error) {
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var buf bytes.Buffer
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return "", err
		}
		// If starts with 0x24 it's an interleaved RTP frame — queue it back (not possible with gorilla)
		// In practice the camera sends RTSP text before RTP, so this is safe.
		if len(msg) > 0 && msg[0] == 0x24 {
			// Got RTP before response — unusual; just ignore and keep reading
			continue
		}
		buf.Write(msg)
		if bytes.Contains(buf.Bytes(), []byte("\r\n\r\n")) {
			return buf.String(), nil
		}
	}
}

func parseSDPCodec(sdp string) (codec string, payloadType uint8, clockRate uint32) {
	codec = "h264"
	payloadType = 96
	clockRate = 90000

	scanner := bufio.NewScanner(strings.NewReader(sdp))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "a=rtpmap:") {
			// a=rtpmap:96 H264/90000
			var pt int
			var name string
			var rate uint32
			fmt.Sscanf(strings.TrimPrefix(line, "a=rtpmap:"), "%d %s", &pt, &name)
			fmt.Sscanf(name, "%[^/]/%d", &name, &rate)
			payloadType = uint8(pt)
			if rate > 0 {
				clockRate = rate
			}
			nameLow := strings.ToLower(name)
			if strings.Contains(nameLow, "265") || strings.Contains(nameLow, "hevc") {
				codec = "h265"
			} else {
				codec = "h264"
			}
		}
	}
	return
}

func parseSDPParameterSets(sdp string) (sps, pps, vps []byte) {
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if !strings.HasPrefix(strings.ToLower(line), "a=fmtp:") {
			continue
		}
		params := line
		for _, field := range strings.Split(params, ";") {
			field = strings.TrimSpace(field)
			lower := strings.ToLower(field)
			switch {
			case strings.HasPrefix(lower, "sprop-sps="):
				if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(field[len("sprop-sps="):])); err == nil {
					sps = decoded
				}
			case strings.HasPrefix(lower, "sprop-pps="):
				if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(field[len("sprop-pps="):])); err == nil {
					pps = decoded
				}
			case strings.HasPrefix(lower, "sprop-vps="):
				if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(field[len("sprop-vps="):])); err == nil {
					vps = decoded
				}
			case strings.HasPrefix(lower, "sprop-parameter-sets="):
				values := strings.TrimSpace(field[len("sprop-parameter-sets="):])
				parts := strings.Split(values, ",")
				if len(parts) >= 2 {
					if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[0])); err == nil {
						sps = decoded
					}
					if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1])); err == nil {
						pps = decoded
					}
				}
			}
		}
	}
	return
}

// sdpTrackControl extracts the first video track's a=control URL from an SDP body.
// If the value is relative (no scheme), it is appended to baseURL.
// Falls back to baseURL+"/trackID=0" if no control attribute is found.
func sdpTrackControl(sdp, baseURL string) string {
	inVideo := false
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "m=video") {
			inVideo = true
			continue
		}
		if strings.HasPrefix(line, "m=") {
			inVideo = false
			continue
		}
		if inVideo && strings.HasPrefix(line, "a=control:") {
			ctrl := strings.TrimPrefix(line, "a=control:")
			ctrl = strings.TrimSpace(ctrl)
			if ctrl == "" || ctrl == "*" {
				break
			}
			if strings.HasPrefix(ctrl, "rtsp://") {
				return ctrl
			}
			return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(ctrl, "/")
		}
	}
	return baseURL + "/trackID=0"
}

// rtspEscape percent-encodes characters that are not safe in an RTSP URL userinfo field.
func rtspEscape(s string) string {
	var out strings.Builder
	for _, b := range []byte(s) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~' {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

// rtspStatusLine returns the first line of an RTSP response for logging.
func rtspStatusLine(resp string) string {
	if idx := strings.Index(resp, "\r\n"); idx >= 0 {
		return resp[:idx]
	}
	return strings.TrimSpace(resp)
}

// rtspStatusCode parses the numeric status code from an RTSP response line.
func rtspStatusCode(resp string) int {
	line := rtspStatusLine(resp)
	// "RTSP/1.0 200 OK"
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	var code int
	fmt.Sscanf(parts[1], "%d", &code)
	return code
}

func parseSession(resp string) string {
	for _, line := range strings.Split(resp, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "session:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				s := strings.TrimSpace(parts[1])
				// Session may have ;timeout=N suffix
				if idx := strings.Index(s, ";"); idx >= 0 {
					s = s[:idx]
				}
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
