package netutil

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeHostPort accepts either a bare host/IP or a URL-ish camera address
// and returns the host plus a port. Paths such as /onvif/device_service are
// intentionally discarded because downstream code appends service paths itself.
func NormalizeHostPort(address string, defaultPort int) (string, int, error) {
	if defaultPort <= 0 {
		defaultPort = 80
	}

	s := strings.TrimSpace(address)
	if s == "" {
		return "", 0, fmt.Errorf("empty address")
	}

	// Users sometimes type "http//host" instead of "http://host"; normalize the
	// common camera URL schemes before handing the value to net/url.
	lower := strings.ToLower(s)
	for _, scheme := range []string{"http", "https", "rtsp"} {
		prefix := scheme + "//"
		if strings.HasPrefix(lower, prefix) {
			s = scheme + "://" + s[len(prefix):]
			break
		}
	}

	if !strings.Contains(s, "://") {
		s = "http://" + s
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", 0, err
	}

	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return "", 0, fmt.Errorf("missing host in %q", address)
	}

	port := defaultPort
	if rawPort := u.Port(); rawPort != "" {
		parsed, err := strconv.Atoi(rawPort)
		if err != nil || parsed <= 0 || parsed > 65535 {
			return "", 0, fmt.Errorf("invalid port %q", rawPort)
		}
		port = parsed
	}

	return host, port, nil
}
