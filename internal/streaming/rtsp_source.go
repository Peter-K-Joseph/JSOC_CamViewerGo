package streaming

import (
	"bufio"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RtspSource connects to a camera's native RTSP server over TCP (default port 554),
// negotiates the session with Digest auth, and publishes AccessUnits to a Track.
// It mirrors the reconnect behaviour of WsSource.
type RtspSource struct {
	host     string
	port     int // typically 554
	username string
	password string
	channel  int
	subtype  int
	track    *Track

	stopCh  chan struct{}
	stopped atomic.Bool

	keepaliveInterval time.Duration
}

func NewRtspSource(host string, port int, username, password string,
	channel, subtype int, track *Track, keepaliveInterval time.Duration) *RtspSource {
	return &RtspSource{
		host:              host,
		port:              port,
		username:          username,
		password:          password,
		channel:           channel,
		subtype:           subtype,
		track:             track,
		stopCh:            make(chan struct{}),
		keepaliveInterval: keepaliveInterval,
	}
}

// Run starts the TCP RTSP ingestion loop. It reconnects on error until Stop() is called.
func (s *RtspSource) Run() error {
	backoff := time.Second
	authFailures := 0
	const maxAuthFailures = 3
	var lastErr error
	for {
		if s.stopped.Load() {
			return lastErr
		}
		err := s.runOnce()
		if err != nil {
			lastErr = err
			log.Printf("[rtsp_source] %s:%d error: %v — retry in %s", s.host, s.port, err, backoff)

			if isAuthError(err) {
				authFailures++
				if authFailures >= maxAuthFailures {
					log.Printf("[rtsp_source] %s:%d giving up after %d auth failures", s.host, s.port, authFailures)
					return lastErr
				}
			} else {
				authFailures = 0
			}
		} else {
			authFailures = 0
			lastErr = nil
		}
		select {
		case <-s.stopCh:
			return lastErr
		case <-time.After(backoff):
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
}

func (s *RtspSource) Stop() {
	if s.stopped.CompareAndSwap(false, true) {
		close(s.stopCh)
	}
}

func (s *RtspSource) runOnce() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("tcp dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[rtsp_source] connected to %s", addr)

	r := bufio.NewReader(conn)
	cseq := 1

	baseURL := fmt.Sprintf("rtsp://%s:%d/", s.host, s.port)
	rtspURL := fmt.Sprintf("rtsp://%s:%d/cam/realmonitor?channel=%d&subtype=%d",
		s.host, s.port, s.channel, s.subtype)

	// ── OPTIONS ──────────────────────────────────────────────────────────────
	if err := rtspSend(conn, cseq, "OPTIONS", baseURL, "", nil); err != nil {
		return err
	}
	if _, err := rtspReadResponse(r); err != nil {
		return fmt.Errorf("OPTIONS: %w", err)
	}
	cseq++

	// ── DESCRIBE (may require Digest auth) ───────────────────────────────────
	if err := rtspSend(conn, cseq, "DESCRIBE", rtspURL, "Accept: application/sdp\r\n", nil); err != nil {
		return err
	}
	descResp, err := rtspReadResponse(r)
	if err != nil {
		return fmt.Errorf("DESCRIBE: %w", err)
	}

	var auth *rtspDigest
	if rtspStatus(descResp) == 401 {
		auth = parseRTSPDigestChallenge(descResp, s.username, s.password)
		if auth == nil {
			return fmt.Errorf("DESCRIBE: 401 but no Digest challenge")
		}
		cseq++
		if err := rtspSend(conn, cseq, "DESCRIBE", rtspURL, "Accept: application/sdp\r\n", auth); err != nil {
			return err
		}
		descResp, err = rtspReadResponse(r)
		if err != nil {
			return fmt.Errorf("DESCRIBE (auth): %w", err)
		}
		if rtspStatus(descResp) != 200 {
			return fmt.Errorf("DESCRIBE: status %d", rtspStatus(descResp))
		}
	}
	cseq++

	sdp := rtspBodyOf(descResp)
	codec, payloadType, clockRate := parseSDPCodec(sdp)
	log.Printf("[rtsp_source] codec=%s pt=%d clock=%d", codec, payloadType, clockRate)

	// ── SETUP ────────────────────────────────────────────────────────────────
	setupURL := rtspURL + "/trackID=0"
	if err := rtspSend(conn, cseq, "SETUP", setupURL,
		"Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n", auth); err != nil {
		return err
	}
	setupResp, err := rtspReadResponse(r)
	if err != nil {
		return fmt.Errorf("SETUP: %w", err)
	}
	sessionID := parseSession(setupResp)
	cseq++

	// ── PLAY ─────────────────────────────────────────────────────────────────
	if err := rtspSend(conn, cseq, "PLAY", rtspURL,
		fmt.Sprintf("Session: %s\r\nRange: npt=0.000-\r\n", sessionID), auth); err != nil {
		return err
	}
	if _, err := rtspReadResponse(r); err != nil {
		return fmt.Errorf("PLAY: %w", err)
	}
	cseq++

	log.Printf("[rtsp_source] streaming started session=%s", sessionID)

	// ── RTP read loop ─────────────────────────────────────────────────────────
	var h264dp H264Depacketizer
	var h265dp H265Depacketizer

	// keepalive runs in its own goroutine so blocking reads don't delay OPTIONS.
	var writeMu sync.Mutex
	kaCseq := cseq
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
				_ = rtspSend(conn, kaCseq, "OPTIONS", baseURL,
					fmt.Sprintf("Session: %s\r\n", sessionID), auth)
				kaCseq++
				writeMu.Unlock()
			}
		}
	}()
	defer close(kaStop)

	hdr := make([]byte, 4)
	for {
		select {
		case <-s.stopCh:
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		// Read 4-byte interleaved header: $ channel len_hi len_lo
		if _, err := io.ReadFull(r, hdr); err != nil {
			return fmt.Errorf("read header: %w", err)
		}

		if hdr[0] != '$' {
			// RTSP text response (e.g., keepalive reply) — discard until blank line.
			rtspDiscardText(r)
			continue
		}

		rtpChannel := hdr[1]
		frameLen := int(binary.BigEndian.Uint16(hdr[2:4]))
		frame := make([]byte, frameLen)
		if _, err := io.ReadFull(r, frame); err != nil {
			return fmt.Errorf("read frame: %w", err)
		}

		if rtpChannel != 0 {
			continue // RTCP on channel 1 — ignore
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

// ── RTSP wire helpers ─────────────────────────────────────────────────────────

func rtspSend(conn net.Conn, cseq int, method, uri, extra string, auth *rtspDigest) error {
	var sb strings.Builder
	sb.WriteString(method + " " + uri + " RTSP/1.0\r\n")
	fmt.Fprintf(&sb, "CSeq: %d\r\n", cseq)
	sb.WriteString("User-Agent: JSOC-CamViewer/1.0\r\n")
	if auth != nil {
		sb.WriteString("Authorization: " + auth.header(method, uri) + "\r\n")
	}
	sb.WriteString(extra)
	sb.WriteString("\r\n")
	_, err := conn.Write([]byte(sb.String()))
	return err
}

const maxRTSPBodyBytes = 1 << 20 // 1 MiB

// rtspReadResponse reads one complete RTSP response including any body.
func rtspReadResponse(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		sb.WriteString(line)
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	resp := sb.String()
	if cl := rtspContentLength(resp); cl > 0 {
		if cl > maxRTSPBodyBytes {
			return resp, fmt.Errorf("RTSP Content-Length %d exceeds limit", cl)
		}
		body := make([]byte, cl)
		if _, err := io.ReadFull(r, body); err != nil {
			return resp, err
		}
		resp += string(body)
	}
	return resp, nil
}

func rtspDiscardText(r *bufio.Reader) {
	for {
		line, err := r.ReadString('\n')
		if err != nil || strings.TrimSpace(line) == "" {
			return
		}
	}
}

func rtspStatus(resp string) int {
	// "RTSP/1.0 200 OK"
	parts := strings.Fields(resp)
	if len(parts) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(parts[1])
	return code
}

func rtspContentLength(resp string) int {
	for _, line := range strings.Split(resp, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			p := strings.SplitN(line, ":", 2)
			if len(p) == 2 {
				n, _ := strconv.Atoi(strings.TrimSpace(p[1]))
				return n
			}
		}
	}
	return 0
}

func rtspBodyOf(resp string) string {
	if idx := strings.Index(resp, "\r\n\r\n"); idx >= 0 {
		return resp[idx+4:]
	}
	return ""
}

// ── Digest auth ───────────────────────────────────────────────────────────────

type rtspDigest struct {
	username, password, realm, nonce string
}

func parseRTSPDigestChallenge(resp, username, password string) *rtspDigest {
	for _, line := range strings.Split(resp, "\r\n") {
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "www-authenticate:") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			continue
		}
		val := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(strings.ToLower(val), "digest") {
			continue
		}
		d := &rtspDigest{username: username, password: password}
		d.realm = rtspDigestField(val, "realm")
		d.nonce = rtspDigestField(val, "nonce")
		if d.realm != "" && d.nonce != "" {
			return d
		}
	}
	return nil
}

func (d *rtspDigest) header(method, uri string) string {
	ha1 := rtspMD5(d.username + ":" + d.realm + ":" + d.password)
	ha2 := rtspMD5(method + ":" + uri)
	resp := rtspMD5(ha1 + ":" + d.nonce + ":" + ha2)
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		d.username, d.realm, d.nonce, uri, resp)
}

func rtspMD5(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", h[:])
}

func rtspDigestField(header, key string) string {
	search := key + `="`
	lh := strings.ToLower(header)
	idx := strings.Index(lh, strings.ToLower(search))
	if idx < 0 {
		return ""
	}
	start := idx + len(search)
	end := strings.Index(header[start:], `"`)
	if end < 0 {
		return ""
	}
	return header[start : start+end]
}
