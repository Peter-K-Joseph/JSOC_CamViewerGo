package rtsp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/jsoc/camviewer/internal/streaming"
)

type sessionState int

const (
	stateInit    sessionState = iota
	stateReady
	statePlaying
)

// session handles one RTSP client TCP connection.
type session struct {
	conn      net.Conn
	reader    *bufio.Reader
	id        string
	state     sessionState
	trackFunc func(streamKey string) *streaming.Track
	host      string
	prefix    string

	track  *streaming.Track
	subCh  chan streaming.AccessUnit
	seqNum uint16
}

func newSession(conn net.Conn, trackFunc func(string) *streaming.Track, host, prefix string) *session {
	id := fmt.Sprintf("%08x", rand.Uint32())
	return &session{
		conn:      conn,
		reader:    bufio.NewReader(conn),
		id:        id,
		state:     stateInit,
		trackFunc: trackFunc,
		host:      host,
		prefix:    prefix,
	}
}

func (s *session) serve() {
	defer s.conn.Close()
	defer s.cleanup()

	for {
		s.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		method, uri, headers, err := s.readRequest()
		if err != nil {
			return
		}

		cseq := headers["cseq"]

		switch strings.ToUpper(method) {
		case "OPTIONS":
			s.respond(200, cseq, map[string]string{
				"Public": "OPTIONS, DESCRIBE, SETUP, PLAY, PAUSE, TEARDOWN",
			}, "")

		case "DESCRIBE":
			streamKey := s.streamKeyFromURI(uri)
			t := s.trackFunc(streamKey)
			if t == nil {
				s.respond(404, cseq, nil, "")
				return
			}
			codec, sps, pps, vps := t.Params()
			sdp := BuildSDP(s.host, streamKey, codec, sps, pps, vps)
			s.respond(200, cseq, map[string]string{
				"Content-Type":   "application/sdp",
				"Content-Length": fmt.Sprintf("%d", len(sdp)),
			}, sdp)

		case "SETUP":
			streamKey := s.streamKeyFromURI(uri)
			t := s.trackFunc(streamKey)
			if t == nil {
				s.respond(404, cseq, nil, "")
				return
			}
			s.track = t
			transport := headers["transport"]
			if !strings.Contains(transport, "TCP") && !strings.Contains(transport, "tcp") {
				// Force TCP interleaved
				transport = "RTP/AVP/TCP;unicast;interleaved=0-1"
			}
			s.state = stateReady
			s.respond(200, cseq, map[string]string{
				"Transport": transport,
				"Session":   s.id + ";timeout=60",
			}, "")

		case "PLAY":
			if s.state != stateReady || s.track == nil {
				s.respond(455, cseq, nil, "")
				continue
			}
			s.subCh = s.track.Subscribe()
			s.state = statePlaying
			s.respond(200, cseq, map[string]string{
				"Session":  s.id,
				"RTP-Info": fmt.Sprintf("url=%s;seq=0;rtptime=0", uri),
			}, "")

			// Stream loop in this goroutine — blocks until teardown/error
			s.streamLoop()
			return

		case "PAUSE":
			s.respond(200, cseq, map[string]string{"Session": s.id}, "")

		case "TEARDOWN":
			s.respond(200, cseq, map[string]string{"Session": s.id}, "")
			return

		default:
			s.respond(405, cseq, nil, "")
		}
	}
}

func (s *session) streamLoop() {
	for au := range s.subCh {
		if err := s.writeAccessUnit(au); err != nil {
			log.Printf("[rtsp] client %s write error: %v", s.conn.RemoteAddr(), err)
			return
		}
	}
}

const maxRTPPayload = 1400

// writeAccessUnit re-packetizes an AnnexB AccessUnit into RTP and sends over TCP interleaved.
func (s *session) writeAccessUnit(au streaming.AccessUnit) error {
	// Strip AnnexB start codes and collect raw NAL units
	nals := splitAnnexB(au.Data)
	ts := au.Timestamp

	for i, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		marker := i == len(nals)-1

		if len(nal) <= maxRTPPayload {
			// Single NAL unit packet
			pkt := s.buildRTPPacket(96, ts, marker, nal)
			if err := s.writeInterleaved(0, pkt); err != nil {
				return err
			}
		} else {
			// FU-A fragmentation (H.264 only path; H.265 uses same logic)
			nalHdr := nal[0]
			nalType := nalHdr & 0x1f
			nal = nal[1:]
			first := true
			for len(nal) > 0 {
				chunk := nal
				if len(chunk) > maxRTPPayload-2 {
					chunk = nal[:maxRTPPayload-2]
				}
				nal = nal[len(chunk):]
				last := len(nal) == 0

				fuIndicator := (nalHdr & 0xe0) | 28
				fuHeader := nalType
				if first {
					fuHeader |= 0x80
				}
				if last {
					fuHeader |= 0x40
				}
				payload := append([]byte{fuIndicator, fuHeader}, chunk...)
				pkt := s.buildRTPPacket(96, ts, last && marker, payload)
				if err := s.writeInterleaved(0, pkt); err != nil {
					return err
				}
				first = false
			}
		}
		s.seqNum++
	}
	return nil
}

func (s *session) buildRTPPacket(pt uint8, ts uint32, marker bool, payload []byte) []byte {
	buf := make([]byte, 12+len(payload))
	buf[0] = 0x80 // version=2
	buf[1] = pt
	if marker {
		buf[1] |= 0x80
	}
	binary.BigEndian.PutUint16(buf[2:4], s.seqNum)
	binary.BigEndian.PutUint32(buf[4:8], ts)
	binary.BigEndian.PutUint32(buf[8:12], 0x12345678) // fixed SSRC
	copy(buf[12:], payload)
	return buf
}

func (s *session) writeInterleaved(channel byte, data []byte) error {
	hdr := []byte{0x24, channel, byte(len(data) >> 8), byte(len(data))}
	s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := s.conn.Write(hdr); err != nil {
		return err
	}
	_, err := s.conn.Write(data)
	return err
}

func (s *session) readRequest() (method, uri string, headers map[string]string, err error) {
	headers = map[string]string{}

	line, err := s.reader.ReadString('\n')
	if err != nil {
		return
	}
	line = strings.TrimSpace(line)
	parts := strings.Fields(line)
	if len(parts) < 2 {
		err = fmt.Errorf("invalid request line: %q", line)
		return
	}
	method = parts[0]
	uri = parts[1]

	for {
		hline, herr := s.reader.ReadString('\n')
		if herr != nil {
			err = herr
			return
		}
		hline = strings.TrimSpace(hline)
		if hline == "" {
			break
		}
		idx := strings.Index(hline, ":")
		if idx < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(hline[:idx]))
		val := strings.TrimSpace(hline[idx+1:])
		headers[key] = val
	}
	return
}

func (s *session) respond(code int, cseq string, extra map[string]string, body string) {
	status := rtspStatus(code)
	sb := fmt.Sprintf("RTSP/1.0 %d %s\r\nCSeq: %s\r\n", code, status, cseq)
	for k, v := range extra {
		sb += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	if body != "" {
		sb += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	} else {
		sb += "\r\n"
	}
	s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.conn.Write([]byte(sb))
}

func (s *session) streamKeyFromURI(uri string) string {
	// rtsp://host:port/cam/my-camera  →  "my-camera"
	// uri may be just /cam/my-camera
	path := uri
	if idx := strings.Index(path, "://"); idx >= 0 {
		path = path[idx+3:]
		if slash := strings.Index(path, "/"); slash >= 0 {
			path = path[slash:]
		}
	}
	// strip /cam/ prefix and /trackID=N suffix
	path = strings.TrimPrefix(path, "/"+s.prefix+"/")
	if idx := strings.Index(path, "/"); idx >= 0 {
		path = path[:idx]
	}
	return path
}

func (s *session) cleanup() {
	if s.subCh != nil && s.track != nil {
		s.track.Unsubscribe(s.subCh)
		s.subCh = nil
	}
}

func rtspStatus(code int) string {
	switch code {
	case 200:
		return "OK"
	case 404:
		return "Not Found"
	case 405:
		return "Method Not Allowed"
	case 455:
		return "Method Not Valid in This State"
	default:
		return "Error"
	}
}

// splitAnnexB splits AnnexB byte stream into raw NAL units (strips start codes).
func splitAnnexB(data []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i < len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			if data[i+2] == 1 {
				if start >= 0 {
					nals = append(nals, data[start:i])
				}
				start = i + 3
				i += 2
			} else if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
				if start >= 0 {
					nals = append(nals, data[start:i])
				}
				start = i + 4
				i += 3
			}
		}
	}
	if start >= 0 && start < len(data) {
		nals = append(nals, data[start:])
	}
	return nals
}

