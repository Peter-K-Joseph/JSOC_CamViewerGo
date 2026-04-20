package auth

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jsoc/camviewer/internal/netutil"
)

const rtspAuthPort = 554

// LoginWithFallback tries Dahua RPC2 first, then falls back to RTSP Digest
// validation for cameras whose HTTP RPC2 endpoint is unavailable.
func LoginWithFallback(host string, port int, username, password string) (*Session, error) {
	sess, err := Login(host, port, username, password)
	if err == nil {
		return sess, nil
	}
	rpcErr := err

	normalizedHost, normalizedPort, normErr := netutil.NormalizeHostPort(host, port)
	if normErr != nil {
		return nil, fmt.Errorf("invalid camera address: %w", normErr)
	}

	if err := ValidateRTSP(normalizedHost, rtspAuthPort, username, password); err != nil {
		return nil, fmt.Errorf("%v; RTSP fallback failed: %w", rpcErr, err)
	}

	return &Session{
		Host:     normalizedHost,
		Port:     normalizedPort,
		Username: username,
		Password: password,
	}, nil
}

// ValidateRTSP verifies credentials by performing an RTSP DESCRIBE request.
func ValidateRTSP(host string, port int, username, password string) error {
	var err error
	host, port, err = netutil.NormalizeHostPort(host, port)
	if err != nil {
		return fmt.Errorf("invalid RTSP address: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	cseq := 1
	baseURL := fmt.Sprintf("rtsp://%s:%d/", host, port)
	rtspURL := fmt.Sprintf("rtsp://%s:%d/cam/realmonitor?channel=1&subtype=0", host, port)

	if err := rtspAuthSend(conn, cseq, "OPTIONS", baseURL, "", nil); err != nil {
		return err
	}
	if _, err := rtspAuthReadResponse(reader); err != nil {
		return fmt.Errorf("OPTIONS: %w", err)
	}
	cseq++

	if err := rtspAuthSend(conn, cseq, "DESCRIBE", rtspURL, "Accept: application/sdp\r\n", nil); err != nil {
		return err
	}
	resp, err := rtspAuthReadResponse(reader)
	if err != nil {
		return fmt.Errorf("DESCRIBE: %w", err)
	}

	switch status := rtspAuthStatus(resp); status {
	case 200:
		return nil
	case 401:
		digest := parseRTSPAuthDigest(resp, username, password)
		if digest == nil {
			return fmt.Errorf("DESCRIBE returned 401 without Digest challenge")
		}
		cseq++
		if err := rtspAuthSend(conn, cseq, "DESCRIBE", rtspURL, "Accept: application/sdp\r\n", digest); err != nil {
			return err
		}
		resp, err = rtspAuthReadResponse(reader)
		if err != nil {
			return fmt.Errorf("DESCRIBE auth: %w", err)
		}
		if status := rtspAuthStatus(resp); status != 200 {
			return fmt.Errorf("DESCRIBE auth status %d", status)
		}
		return nil
	default:
		return fmt.Errorf("DESCRIBE status %d", status)
	}
}

func rtspAuthSend(conn net.Conn, cseq int, method, uri, extra string, auth *rtspAuthDigest) error {
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

func rtspAuthReadResponse(r *bufio.Reader) (string, error) {
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
	if cl := rtspAuthContentLength(resp); cl > 0 {
		body := make([]byte, cl)
		if _, err := io.ReadFull(r, body); err != nil {
			return resp, err
		}
		resp += string(body)
	}
	return resp, nil
}

func rtspAuthStatus(resp string) int {
	parts := strings.Fields(resp)
	if len(parts) < 2 {
		return 0
	}
	code, _ := strconv.Atoi(parts[1])
	return code
}

func rtspAuthContentLength(resp string) int {
	for _, line := range strings.Split(resp, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				n, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
				return n
			}
		}
	}
	return 0
}

type rtspAuthDigest struct {
	username string
	password string
	realm    string
	nonce    string
}

func parseRTSPAuthDigest(resp, username, password string) *rtspAuthDigest {
	for _, line := range strings.Split(resp, "\r\n") {
		if !strings.HasPrefix(strings.ToLower(line), "www-authenticate:") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		value := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(strings.ToLower(value), "digest") {
			continue
		}
		digest := &rtspAuthDigest{
			username: username,
			password: password,
			realm:    rtspAuthDigestField(value, "realm"),
			nonce:    rtspAuthDigestField(value, "nonce"),
		}
		if digest.realm != "" && digest.nonce != "" {
			return digest
		}
	}
	return nil
}

func (d *rtspAuthDigest) header(method, uri string) string {
	ha1 := rtspAuthMD5(d.username + ":" + d.realm + ":" + d.password)
	ha2 := rtspAuthMD5(method + ":" + uri)
	response := rtspAuthMD5(ha1 + ":" + d.nonce + ":" + ha2)
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		d.username, d.realm, d.nonce, uri, response)
}

func rtspAuthMD5(s string) string {
	hash := md5.Sum([]byte(s))
	return fmt.Sprintf("%x", hash[:])
}

func rtspAuthDigestField(header, key string) string {
	search := key + `="`
	idx := strings.Index(strings.ToLower(header), strings.ToLower(search))
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
