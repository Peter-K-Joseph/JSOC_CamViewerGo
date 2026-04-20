package streaming

// DvripSource connects to a Dahua / CP-Plus camera's proprietary DVRIP service
// on TCP port 37777, negotiates login with SofiaHash auth, sends OPMonitorClaim
// + OPMonitorStart, and reads the resulting DHAV-framed H.264/H.265 stream.
//
// Unlike WsSource and RtspSource the camera delivers fully-assembled Annex-B
// NAL units directly — no RTP depacketisation is needed.
//
// Protocol reference: https://github.com/AlexxIT/go2rtc (MIT)

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"
)

const (
	dvripCmdLogin          uint16 = 1000
	dvripCmdMonitorClaim   uint16 = 1413
	dvripCmdMonitorStart   uint16 = 1410
	dvripDefaultPort              = 37777
	dvripConnectTimeout           = 10 * time.Second
	dvripRWTimeout                = 30 * time.Second
	dvripHeaderSize               = 20
)

// DvripSource implements cameraSource for Dahua's TCP 37777 private protocol.
type DvripSource struct {
	host     string
	port     int
	username string
	password string
	channel  int
	subtype  int // 0 = Main, 1 = Extra1
	track    *Track

	stopCh  chan struct{}
	stopped atomic.Bool

	keepaliveInterval time.Duration
}

func NewDvripSource(host string, port int, username, password string,
	channel, subtype int, track *Track, keepaliveInterval time.Duration) *DvripSource {
	if port <= 0 {
		port = dvripDefaultPort
	}
	return &DvripSource{
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

// Run starts the DVRIP ingestion loop, reconnecting on error until Stop() is called.
func (s *DvripSource) Run() {
	backoff := time.Second
	for {
		if s.stopped.Load() {
			return
		}
		if err := s.runOnce(); err != nil {
			log.Printf("[dvrip] %s:%d error: %v — retry in %s", s.host, s.port, err, backoff)
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

func (s *DvripSource) Stop() {
	if s.stopped.CompareAndSwap(false, true) {
		close(s.stopCh)
	}
}

// ── Core session ──────────────────────────────────────────────────────────────

type dvripConn struct {
	conn    net.Conn
	rd      *bufio.Reader
	session uint32
	seq     uint32
	buf     []byte // DHAV frame accumulator
}

func (c *dvripConn) writeCmd(code uint16, payload []byte) error {
	hdr := make([]byte, dvripHeaderSize, dvripHeaderSize+len(payload))
	hdr[0] = 0xFF
	binary.LittleEndian.PutUint32(hdr[4:], c.session)
	binary.LittleEndian.PutUint32(hdr[8:], c.seq)
	binary.LittleEndian.PutUint16(hdr[14:], code)
	binary.LittleEndian.PutUint32(hdr[16:], uint32(len(payload)))
	c.seq++
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	_, err := c.conn.Write(append(hdr, payload...))
	return err
}

// readChunk reads one DVRIP header + payload, updates session ID, returns payload.
func (c *dvripConn) readChunk() ([]byte, error) {
	c.conn.SetReadDeadline(time.Now().Add(dvripRWTimeout)) //nolint:errcheck
	hdr := make([]byte, dvripHeaderSize)
	if _, err := io.ReadFull(c.rd, hdr); err != nil {
		return nil, err
	}
	if hdr[0] != 0xFF {
		return nil, fmt.Errorf("dvrip: bad magic 0x%02X", hdr[0])
	}
	c.session = binary.LittleEndian.Uint32(hdr[4:])
	size := binary.LittleEndian.Uint32(hdr[16:])
	if size == 0 {
		return nil, nil
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(c.rd, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// readJSON reads one command response and unmarshals it.
// Dahua success codes: 100 (OK) and 515 (stream already claimed).
func (c *dvripConn) readJSON() (map[string]interface{}, error) {
	b, err := c.readChunk()
	if err != nil {
		return nil, err
	}
	// Strip trailing "\x0A\x00" if present.
	b = bytes.TrimRight(b, "\x00\x0a")
	res := map[string]interface{}{}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("dvrip: json: %w (raw: %q)", err, b)
	}
	ret, _ := res["Ret"].(float64)
	if ret != 100 && ret != 515 {
		return nil, fmt.Errorf("dvrip: server error Ret=%.0f body=%q", ret, b)
	}
	return res, nil
}

// readPacket accumulates chunks until one complete DHAV frame is ready.
// DHAV frames start with the prefix {0x00, 0x00, 0x01} and the type byte is
// at offset 3 (0xFC/0xFE = I-frame, 0xFD = P-frame, 0xFA/0xF9 = audio).
func (c *dvripConn) readPacket() (pType byte, frame []byte, err error) {
	for {
		// Ensure we have at least 16 bytes to determine type + size.
		for len(c.buf) < 16 {
			chunk, e := c.readChunk()
			if e != nil {
				return 0, nil, e
			}
			c.buf = append(c.buf, chunk...)
		}

		if !bytes.HasPrefix(c.buf, []byte{0x00, 0x00, 0x01}) {
			// Stale data / keepalive — resync by scanning for the next prefix.
			idx := bytes.Index(c.buf[1:], []byte{0x00, 0x00, 0x01})
			if idx < 0 {
				c.buf = c.buf[:0]
				continue
			}
			c.buf = c.buf[idx+1:]
			continue
		}

		pType = c.buf[3]
		var totalSize int
		switch pType {
		case 0xFC, 0xFE: // I-frame — 16-byte header
			totalSize = int(binary.LittleEndian.Uint32(c.buf[12:])) + 16
		case 0xFD: // P-frame — 8-byte header
			totalSize = int(binary.LittleEndian.Uint32(c.buf[4:])) + 8
		case 0xFA, 0xF9: // Audio / unknown — 8-byte header, 16-bit length
			totalSize = int(binary.LittleEndian.Uint16(c.buf[6:])) + 8
		default:
			// Unknown type — skip 4 bytes and resync.
			c.buf = c.buf[4:]
			continue
		}

		// Read more chunks until the full frame is in the buffer.
		for len(c.buf) < totalSize {
			chunk, e := c.readChunk()
			if e != nil {
				return 0, nil, e
			}
			c.buf = append(c.buf, chunk...)
		}

		frame = make([]byte, totalSize)
		copy(frame, c.buf[:totalSize])
		c.buf = c.buf[totalSize:]
		return pType, frame, nil
	}
}

// ── runOnce ───────────────────────────────────────────────────────────────────

func (s *DvripSource) runOnce() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	raw, err := net.DialTimeout("tcp", addr, dvripConnectTimeout)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer raw.Close()

	c := &dvripConn{
		conn: raw,
		rd:   bufio.NewReader(raw),
	}

	log.Printf("[dvrip] connected to %s", addr)

	// ── Login ─────────────────────────────────────────────────────────────────
	streamType := "Main"
	if s.subtype == 1 {
		streamType = "Extra1"
	}
	loginPayload := fmt.Sprintf(
		`{"EncryptType":"MD5","LoginType":"DVRIP-Web","PassWord":"%s","UserName":"%s"}`+"\x0A\x00",
		dvripSofiaHash(s.password), s.username,
	)
	if err := c.writeCmd(dvripCmdLogin, []byte(loginPayload)); err != nil {
		return fmt.Errorf("login write: %w", err)
	}
	if _, err := c.readJSON(); err != nil {
		return fmt.Errorf("login response: %w", err)
	}
	log.Printf("[dvrip] logged in (session 0x%08X)", c.session)

	// ── OPMonitorClaim ────────────────────────────────────────────────────────
	streamParam := fmt.Sprintf(
		`{"Channel":%d,"CombinMode":"NONE","StreamType":"%s","TransMode":"TCP"}`,
		s.channel-1, streamType, // DVRIP channels are 0-indexed
	)
	claimPayload := fmt.Sprintf(
		`{"Name":"OPMonitor","SessionID":"0x%08X","OPMonitor":{"Action":"Claim","Parameter":%s}}`+"\x0A\x00",
		c.session, streamParam,
	)
	if err := c.writeCmd(dvripCmdMonitorClaim, []byte(claimPayload)); err != nil {
		return fmt.Errorf("claim write: %w", err)
	}
	if _, err := c.readJSON(); err != nil {
		return fmt.Errorf("claim response: %w", err)
	}

	// ── OPMonitorStart ────────────────────────────────────────────────────────
	startPayload := fmt.Sprintf(
		`{"Name":"OPMonitor","SessionID":"0x%08X","OPMonitor":{"Action":"Start","Parameter":%s}}`+"\x0A\x00",
		c.session, streamParam,
	)
	if err := c.writeCmd(dvripCmdMonitorStart, []byte(startPayload)); err != nil {
		return fmt.Errorf("start write: %w", err)
	}
	// The server may or may not send a JSON response before streaming begins.
	// Some cameras send nothing; we'll handle that by checking the first read.

	log.Printf("[dvrip] streaming started (channel %d, %s)", s.channel, streamType)

	// ── Media read loop ───────────────────────────────────────────────────────
	var codec string
	var fps uint32
	var videoTS uint32
	var videoDT uint32
	paramsSet := false

	keepaliveTicker := time.NewTicker(s.keepaliveInterval)
	defer keepaliveTicker.Stop()

	for {
		// Check stop signal between frames.
		select {
		case <-s.stopCh:
			return nil
		case <-keepaliveTicker.C:
			// Send an OPTIONS-equivalent keepalive (re-claim with Claim action).
			// Some cameras close idle connections; this keeps the session alive.
			ping := fmt.Sprintf(
				`{"Name":"OPMonitor","SessionID":"0x%08X","OPMonitor":{"Action":"Claim","Parameter":%s}}`+"\x0A\x00",
				c.session, streamParam,
			)
			c.writeCmd(dvripCmdMonitorClaim, []byte(ping)) //nolint:errcheck
		default:
		}

		pType, frame, err := c.readPacket()
		if err != nil {
			return fmt.Errorf("read packet: %w", err)
		}

		switch pType {
		case 0xFC, 0xFE: // I-frame (keyframe)
			if len(frame) < 16 {
				continue
			}
			mediaCode := frame[4]
			fps = uint32(frame[5])
			if fps == 0 {
				fps = 25
			}
			ts := binary.LittleEndian.Uint32(frame[8:])

			// Determine codec on first I-frame.
			if !paramsSet {
				switch mediaCode {
				case 0x02, 0x12:
					codec = "h264"
				case 0x03, 0x13, 0x43, 0x53:
					codec = "h265"
				default:
					log.Printf("[dvrip] unsupported video codec 0x%02X", mediaCode)
					continue
				}
				videoTS = ts
				videoDT = 90000 / fps
				paramsSet = true
			} else {
				videoTS += videoDT
			}

			annexb := frame[16:]
			extractAndSetParams(s.track, codec, annexb)

			s.track.Publish(AccessUnit{
				Codec:     codec,
				Timestamp: videoTS,
				Data:      annexb,
				Keyframe:  true,
			})

		case 0xFD: // P-frame
			if !paramsSet || len(frame) < 8 {
				continue
			}
			videoTS += videoDT
			s.track.Publish(AccessUnit{
				Codec:     codec,
				Timestamp: videoTS,
				Data:      frame[8:],
				Keyframe:  false,
			})

		case 0xFA: // Audio — ignored for now (no audio track in fmp4 pipeline)
		case 0xF9: // Unknown control frame — skip
		}
	}
}

// ── Annex-B parameter extraction ──────────────────────────────────────────────

// extractAndSetParams scans an Annex-B bitstream for SPS/PPS (H.264) or
// VPS/SPS/PPS (H.265) and calls UpdateParams on the track.
func extractAndSetParams(t *Track, codec string, annexb []byte) {
	nalus := splitAnnexB(annexb)
	var sps, pps, vps []byte
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			continue
		}
		switch codec {
		case "h264":
			nalType := nalu[0] & 0x1F
			switch nalType {
			case 7:
				sps = nalu
			case 8:
				pps = nalu
			}
		case "h265":
			nalType := (nalu[0] & 0x7E) >> 1
			switch nalType {
			case 32:
				vps = nalu
			case 33:
				sps = nalu
			case 34:
				pps = nalu
			}
		}
	}
	if sps != nil || pps != nil || vps != nil {
		t.UpdateParams(codec, sps, pps, vps)
	}
}

// splitAnnexB splits an Annex-B buffer on 4-byte start codes (00 00 00 01),
// returning raw NALU bytes without the start code.
func splitAnnexB(b []byte) [][]byte {
	startCode := []byte{0x00, 0x00, 0x00, 0x01}
	var nalus [][]byte
	for len(b) > 0 {
		if !bytes.HasPrefix(b, startCode) {
			b = b[1:]
			continue
		}
		b = b[4:] // skip start code
		next := bytes.Index(b, startCode)
		if next < 0 {
			nalus = append(nalus, b)
			break
		}
		nalus = append(nalus, b[:next])
		b = b[next:]
	}
	return nalus
}

// ── SofiaHash ─────────────────────────────────────────────────────────────────

// dvripSofiaHash computes the Dahua proprietary 8-char password hash.
// Each output character is chosen from a 62-char printable set by pairing
// consecutive MD5 bytes and taking their sum modulo 62.
func dvripSofiaHash(password string) string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	hash := md5.Sum([]byte(password))
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[i] = chars[(uint16(hash[2*i])+uint16(hash[2*i+1]))%62]
	}
	return string(out)
}
