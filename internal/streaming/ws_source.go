package streaming

import (
	"bufio"
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
	// Forward session cookies
	for _, c := range s.cookies {
		hdr.Add("Cookie", c.String())
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

	// OPTIONS
	if err := s.sendRTSP(conn, cseq, fmt.Sprintf(
		"OPTIONS rtsp://%s:%d/ RTSP/1.0\r\nCSeq: %d\r\n\r\n", s.host, s.port, cseq)); err != nil {
		return err
	}
	if _, err := s.readRTSP(conn); err != nil {
		return fmt.Errorf("OPTIONS response: %w", err)
	}
	cseq++

	// DESCRIBE
	rtspURL := fmt.Sprintf("rtsp://%s:%d/cam/realmonitor?channel=%d&subtype=%d",
		s.host, s.port, s.channel, s.subtype)
	if err := s.sendRTSP(conn, cseq, fmt.Sprintf(
		"DESCRIBE %s RTSP/1.0\r\nCSeq: %d\r\nAccept: application/sdp\r\n\r\n",
		rtspURL, cseq)); err != nil {
		return err
	}
	descResp, err := s.readRTSP(conn)
	if err != nil {
		return fmt.Errorf("DESCRIBE response: %w", err)
	}
	cseq++

	codec, payloadType, clockRate := parseSDPCodec(descResp)
	log.Printf("[ws_source] codec=%s pt=%d clock=%d", codec, payloadType, clockRate)

	// SETUP
	if err := s.sendRTSP(conn, cseq, fmt.Sprintf(
		"SETUP %s/trackID=0 RTSP/1.0\r\nCSeq: %d\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n\r\n",
		rtspURL, cseq)); err != nil {
		return err
	}
	setupResp, err := s.readRTSP(conn)
	if err != nil {
		return fmt.Errorf("SETUP response: %w", err)
	}
	sessionID := parseSession(setupResp)
	cseq++

	// PLAY
	if err := s.sendRTSP(conn, cseq, fmt.Sprintf(
		"PLAY %s RTSP/1.0\r\nCSeq: %d\r\nSession: %s\r\nRange: npt=0.000-\r\n\r\n",
		rtspURL, cseq, sessionID)); err != nil {
		return err
	}
	if _, err := s.readRTSP(conn); err != nil {
		return fmt.Errorf("PLAY response: %w", err)
	}
	cseq++

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
				_ = s.sendRTSP(conn, cseqMu, fmt.Sprintf(
					"OPTIONS rtsp://%s:%d/ RTSP/1.0\r\nCSeq: %d\r\nSession: %s\r\n\r\n",
					s.host, s.port, cseqMu, sessionID))
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

// sendRTSP writes an RTSP request as a WebSocket binary message.
func (s *WsSource) sendRTSP(conn *websocket.Conn, _ int, msg string) error {
	return conn.WriteMessage(websocket.BinaryMessage, []byte(msg))
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
