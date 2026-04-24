package discovery

import (
	"encoding/xml"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	wsDiscoveryAddr = "239.255.255.250:3702"
	probeTemplate   = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
            xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"
            xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
  <s:Header>
    <a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</a:Action>
    <a:MessageID>uuid:%s</a:MessageID>
    <a:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To>
  </s:Header>
  <s:Body>
    <d:Probe>
      <d:Types>dn:NetworkVideoTransmitter</d:Types>
    </d:Probe>
  </s:Body>
</s:Envelope>`
)

type Device struct {
	IP           string   `json:"ip"`
	Port         int      `json:"port"`
	XAddrs       []string `json:"xaddrs"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
}

type probeMatch struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		ProbeMatches struct {
			Matches []struct {
				XAddrs string `xml:"XAddrs"`
				Scopes string `xml:"Scopes"`
				Types  string `xml:"Types"`
			} `xml:"ProbeMatch"`
		} `xml:"ProbeMatches"`
	} `xml:"Body"`
}

func Discover(timeout time.Duration) ([]Device, error) {
	msgID := uuid.NewString()

	probe := fmt.Sprintf(probeTemplate, msgID)

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}
	defer conn.Close()

	dst, err := net.ResolveUDPAddr("udp4", wsDiscoveryAddr)
	if err != nil {
		return nil, err
	}

	if _, err := conn.WriteToUDP([]byte(probe), dst); err != nil {
		return nil, fmt.Errorf("send probe: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(timeout))

	seen := map[string]bool{}
	var devices []Device
	buf := make([]byte, 65536)

	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}

		var match probeMatch
		if xmlErr := xml.Unmarshal(buf[:n], &match); xmlErr != nil {
			continue
		}

		for _, pm := range match.Body.ProbeMatches.Matches {
			xaddrs := strings.Fields(pm.XAddrs)
			if len(xaddrs) == 0 {
				continue
			}

			ip := src.IP.String()
			if seen[ip] {
				continue
			}

			port := 80
			mfr, model := parseScopes(pm.Scopes)

			// Try to extract port from first xaddr
			for _, xa := range xaddrs {
				if h, p, err2 := parseXAddr(xa); err2 == nil {
					ip = h
					port = p
					break
				}
			}

			seen[ip] = true
			devices = append(devices, Device{
				IP:           ip,
				Port:         port,
				XAddrs:       xaddrs,
				Manufacturer: mfr,
				Model:        model,
			})
		}
	}

	return devices, nil
}

func parseScopes(scopes string) (manufacturer, model string) {
	for _, scope := range strings.Fields(scopes) {
		scope = strings.TrimPrefix(scope, "onvif://www.onvif.org/")
		parts := strings.SplitN(scope, "/", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(parts[0])
		val := parts[1]
		switch key {
		case "mfr", "manufacturer", "make":
			manufacturer = val
		case "hardware", "model":
			model = val
		}
	}
	return
}

func parseXAddr(xaddr string) (host string, port int, err error) {
	// strip scheme
	s := xaddr
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// strip path
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	h, p, err2 := net.SplitHostPort(s)
	if err2 != nil {
		return xaddr, 80, nil
	}
	port = 80
	fmt.Sscanf(p, "%d", &port)
	return h, port, nil
}

