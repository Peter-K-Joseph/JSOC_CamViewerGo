// Package ptz provides a minimal hand-rolled ONVIF SOAP client for PTZ and
// Imaging (focus) control.  No external dependencies — only stdlib.
package ptz

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // ONVIF WS-Security mandates SHA1
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jsoc/camviewer/internal/netutil"
)

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s)) //nolint:errcheck // bytes.Buffer.Write never errors
	return b.String()
}

// ── SOAP / WS-Security helpers ───────────────────────────────────────────────

func wsseHeader(username, password string, timeOffset time.Duration) string {
	nonce := make([]byte, 16)
	rand.Read(nonce) //nolint:errcheck
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)

	// Use the camera's clock (our clock + offset) so the Created timestamp
	// is within the camera's acceptance window.  Dahua / CP-Plus firmware
	// rejects WS-Security if Created is more than ~30 s off.
	created := time.Now().Add(timeOffset).UTC().Format("2006-01-02T15:04:05Z")

	// PasswordDigest = Base64(SHA1(nonce_raw ‖ created ‖ password))
	h := sha1.New() //nolint:gosec
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))

	return fmt.Sprintf(
		`<wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
                       xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">
  <wsse:UsernameToken>
    <wsse:Username>%s</wsse:Username>
    <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password>
    <wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</wsse:Nonce>
    <wsu:Created>%s</wsu:Created>
  </wsse:UsernameToken>
</wsse:Security>`, username, digest, nonceB64, created)
}

func soapEnvelope(header, body string) string {
	// Use SOAP 1.1 envelope namespace.  Dahua firmware (and most IP cameras)
	// only accept SOAP 1.1 — sending the SOAP 1.2 namespace causes an
	// immediate connection close (EOF) without any HTTP response.
	return `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"
            xmlns:tds="http://www.onvif.org/ver10/device/wsdl"
            xmlns:trt="http://www.onvif.org/ver10/media/wsdl"
            xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"
            xmlns:timg="http://www.onvif.org/ver20/imaging/wsdl"
            xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Header>` + header + `</s:Header>
  <s:Body>` + body + `</s:Body>
</s:Envelope>`
}

// soapCall sends a SOAP request and returns the raw response body.
//
// Auth strategy (Dahua / CP-Plus compatible):
//  1. WS-Security PasswordDigest (clock-synced) + HTTP Basic
//  2. If SOAP auth fault → retry with HTTP Digest only (no WS-Security)
//  3. If HTTP 401 → retry with HTTP Digest
func (c *Client) soapCall(endpoint, action, body string) ([]byte, error) {
	// ── Attempt 1: WS-Security + HTTP Basic ──────────────────────────────────
	wsseHdr := wsseHeader(c.Username, c.password, c.timeOffset)
	payload := soapEnvelope(wsseHdr, body)
	data, resp, err := c.doSOAPRequest(endpoint, action, payload, true)
	if err != nil {
		return nil, err
	}

	// HTTP 401 → retry with HTTP Digest (no change to SOAP body).
	if resp != nil && resp.StatusCode == 401 {
		digestHdr := c.buildHTTPDigest(resp.Header.Get("WWW-Authenticate"), "POST", endpoint)
		if digestHdr == "" {
			return nil, fmt.Errorf("HTTP 401: camera rejected credentials (no Digest challenge)")
		}
		data, _, err = c.doSOAPRequestWithAuth(endpoint, action, payload, digestHdr)
		if err != nil {
			return nil, err
		}
		return data, nil
	}

	// Check for SOAP auth fault → retry with HTTP Digest only (empty SOAP header).
	if isSOAPAuthFault(data) {
		plainPayload := soapEnvelope("", body)

		// Try HTTP Basic first.
		data2, resp2, err2 := c.doSOAPRequest(endpoint, action, plainPayload, true)
		if err2 != nil {
			return nil, fmt.Errorf("WS-Security rejected; HTTP Basic also failed: %w", err2)
		}
		if resp2 != nil && resp2.StatusCode == 401 {
			digestHdr := c.buildHTTPDigest(resp2.Header.Get("WWW-Authenticate"), "POST", endpoint)
			if digestHdr == "" {
				// Return the original WS-Security error — more descriptive.
				return nil, fmt.Errorf("HTTP %d SOAP fault: %s", resp.StatusCode, soapFault(data))
			}
			data2, _, err2 = c.doSOAPRequestWithAuth(endpoint, action, plainPayload, digestHdr)
			if err2 != nil {
				return nil, err2
			}
		}
		if data2 != nil {
			if fault := soapFault(data2); fault != "" {
				return nil, fmt.Errorf("SOAP fault: %s", fault)
			}
			return data2, nil
		}
	}

	return data, nil
}

// doSOAPRequest sends a SOAP POST and returns (body, resp, error).
// If withBasic is true, HTTP Basic auth is included.
// On success (2xx, no SOAP fault) error is nil and resp is nil.
// On HTTP error or SOAP fault, resp is returned for caller inspection.
func (c *Client) doSOAPRequest(endpoint, action, payload string, withBasic bool) ([]byte, *http.Response, error) {
	req, err := http.NewRequest("POST", endpoint, bytes.NewBufferString(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s"`, action))
	if withBasic {
		req.SetBasicAuth(c.Username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	// Return the response object for 401 so caller can parse Digest challenge.
	if resp.StatusCode == 401 {
		return nil, resp, nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode >= 400 {
		// Return the data so caller can inspect SOAP faults.
		return data, resp, nil
	}
	if fault := soapFault(data); fault != "" {
		return data, resp, nil
	}
	return data, nil, nil
}

// doSOAPRequestWithAuth sends a SOAP POST with an explicit Authorization header.
func (c *Client) doSOAPRequestWithAuth(endpoint, action, payload, authHeader string) ([]byte, *http.Response, error) {
	req, err := http.NewRequest("POST", endpoint, bytes.NewBufferString(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s"`, action))
	req.Header.Set("Authorization", authHeader)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		if fault := soapFault(data); fault != "" {
			return nil, nil, fmt.Errorf("HTTP %d SOAP fault: %s", resp.StatusCode, fault)
		}
		return nil, nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 1000))
	}
	if fault := soapFault(data); fault != "" {
		return nil, nil, fmt.Errorf("SOAP fault: %s", fault)
	}
	return data, nil, nil
}

// isSOAPAuthFault checks if the response data contains an authentication-related SOAP fault.
func isSOAPAuthFault(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	fault := soapFault(data)
	lower := strings.ToLower(fault)
	return strings.Contains(lower, "not authorized") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "invalid username") ||
		strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "sender")
}

// buildHTTPDigest parses a WWW-Authenticate Digest challenge and returns the
// Authorization header value, or "" if the challenge is not Digest.
func (c *Client) buildHTTPDigest(challenge, method, uri string) string {
	lower := strings.ToLower(challenge)
	if !strings.HasPrefix(lower, "digest") {
		return ""
	}
	realm := onvifDigestField(challenge, "realm")
	nonce := onvifDigestField(challenge, "nonce")
	if realm == "" || nonce == "" {
		return ""
	}
	// Extract just the path from the full URL for the Digest URI.
	digestURI := uri
	if u, err := url.Parse(uri); err == nil {
		digestURI = u.RequestURI()
	}
	ha1 := fmt.Sprintf("%x", md5.Sum([]byte(c.Username+":"+realm+":"+c.password)))
	ha2 := fmt.Sprintf("%x", md5.Sum([]byte(method+":"+digestURI)))
	response := fmt.Sprintf("%x", md5.Sum([]byte(ha1+":"+nonce+":"+ha2)))
	return fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		c.Username, realm, nonce, digestURI, response)
}

func onvifDigestField(header, name string) string {
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

// soapFault extracts a SOAP fault reason from a response, if present.
func soapFault(data []byte) string {
	var env struct {
		Body struct {
			Fault struct {
				Reason struct {
					Text string `xml:"Text"`
				} `xml:"Reason"`
				// SOAP 1.1 fallback
				FaultString string `xml:"faultstring"`
			} `xml:"Fault"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return ""
	}
	if env.Body.Fault.Reason.Text != "" {
		return env.Body.Fault.Reason.Text
	}
	return env.Body.Fault.FaultString
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// isDefinitiveError returns true when the error is a clear server-side
// rejection (auth failure, SOAP fault, bad request) rather than a routing /
// connectivity issue that might resolve on a different path.
func isDefinitiveError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Auth errors and explicit SOAP faults are definitive.
	if strings.Contains(msg, "HTTP 401") ||
		strings.Contains(msg, "HTTP 403") ||
		strings.Contains(msg, "SOAP fault") {
		return true
	}
	// A network-level error (EOF, connection refused, timeout) means the path
	// either doesn't exist or the server closed the connection — keep trying.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return false
	}
	// HTTP 404 / 405 → try the next path.
	if strings.Contains(msg, "HTTP 404") || strings.Contains(msg, "HTTP 405") {
		return false
	}
	// HTTP 4xx other than 401/403 — could be a routing 400; keep trying.
	if strings.Contains(msg, "HTTP 4") {
		return false
	}
	// HTTP 5xx — server error; keep trying other paths.
	if strings.Contains(msg, "HTTP 5") {
		return false
	}
	return true
}

// ── Client ───────────────────────────────────────────────────────────────────

// Client is an authenticated ONVIF client for one camera.
type Client struct {
	DeviceURL  string
	PTZURL     string
	MediaURL   string
	ImagingURL string
	Username   string
	password   string

	// timeOffset is the difference between the camera's clock and ours
	// (cameraTime - localTime).  Applied to WS-Security Created timestamps
	// so they are within the camera's acceptance window.
	timeOffset time.Duration

	// Resolved at probe time.
	ProfileToken string // token of first PTZ-capable media profile
	VSToken      string // video source token for that profile

	http *http.Client
}

// deviceServicePaths are tried in order during Probe until one answers.
var deviceServicePaths = []string{
	"/onvif/device_service",
	"/onvif/device_management",
	"/onvif/device",
	"/onvif/services",
	"/onvif/Device",
}

// Probe creates and validates an ONVIF client for the given camera.
// It performs GetSystemDateAndTime → GetCapabilities → GetProfiles to confirm PTZ support.
func Probe(ip string, port int, username, password string) (*Client, error) {
	var err error
	ip, port, err = netutil.NormalizeHostPort(ip, port)
	if err != nil {
		return nil, fmt.Errorf("invalid camera address: %w", err)
	}
	c := &Client{
		Username: username,
		password: password,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
	base := fmt.Sprintf("http://%s:%d", ip, port)

	// ── Clock sync ───────────────────────────────────────────────────────────
	// GetSystemDateAndTime is unauthenticated on most cameras.  We need the
	// camera's time to generate correct WS-Security PasswordDigest timestamps.
	// Try each device path until one answers.
	for _, path := range deviceServicePaths {
		c.DeviceURL = base + path
		if err := c.syncClock(); err == nil {
			break
		}
	}
	// If sync fails (camera doesn't support it, network issue, etc.)
	// we continue with zero offset — WS-Security may still work if clocks
	// are roughly aligned, and the HTTP Digest fallback covers the rest.

	// ── GetCapabilities ──────────────────────────────────────────────────────
	var lastErr error
	for _, path := range deviceServicePaths {
		c.DeviceURL = base + path
		lastErr = c.getCapabilities()
		if lastErr == nil {
			break
		}
		if isDefinitiveError(lastErr) {
			break
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("GetCapabilities: %w", lastErr)
	}
	if c.PTZURL == "" {
		return nil, fmt.Errorf("camera reports no PTZ capability")
	}
	if err := c.getProfiles(); err != nil {
		return nil, fmt.Errorf("GetProfiles: %w", err)
	}
	if c.ProfileToken == "" {
		return nil, fmt.Errorf("no PTZ-capable media profile found")
	}
	return c, nil
}

// syncClock calls GetSystemDateAndTime (unauthenticated) and computes the
// offset between the camera's UTC clock and ours.
func (c *Client) syncClock() error {
	body := `<tds:GetSystemDateAndTime/>`
	// Send WITHOUT WS-Security — this call is public on ONVIF-compliant cameras.
	payload := soapEnvelope("", body)
	req, err := http.NewRequest("POST", c.DeviceURL, bytes.NewBufferString(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"http://www.onvif.org/ver10/device/wsdl/GetSystemDateAndTime"`)

	localBefore := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var env struct {
		Body struct {
			Resp struct {
				Info struct {
					UTCDateTime struct {
						Date struct {
							Year  int `xml:"Year"`
							Month int `xml:"Month"`
							Day   int `xml:"Day"`
						} `xml:"Date"`
						Time struct {
							Hour   int `xml:"Hour"`
							Minute int `xml:"Minute"`
							Second int `xml:"Second"`
						} `xml:"Time"`
					} `xml:"UTCDateTime"`
				} `xml:"SystemDateAndTime"`
			} `xml:"GetSystemDateAndTimeResponse"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("parse time: %w", err)
	}

	d := env.Body.Resp.Info.UTCDateTime
	if d.Date.Year == 0 {
		return fmt.Errorf("camera returned empty date")
	}

	camTime := time.Date(
		d.Date.Year, time.Month(d.Date.Month), d.Date.Day,
		d.Time.Hour, d.Time.Minute, d.Time.Second,
		0, time.UTC,
	)
	localMid := localBefore.Add(time.Since(localBefore) / 2).UTC()
	c.timeOffset = camTime.Sub(localMid)

	if abs(c.timeOffset) > time.Second {
		fmt.Printf("[ptz] clock offset: camera is %s ahead of local time\n", c.timeOffset)
	}
	return nil
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ── Capability & profile discovery ──────────────────────────────────────────

func (c *Client) getCapabilities() error {
	body := `<tds:GetCapabilities><tds:Category>All</tds:Category></tds:GetCapabilities>`
	data, err := c.soapCall(c.DeviceURL, "http://www.onvif.org/ver10/device/wsdl/GetCapabilities", body)
	if err != nil {
		return err
	}

	var env struct {
		Body struct {
			Resp struct {
				Caps struct {
					PTZ struct {
						XAddr string `xml:"XAddr"`
					} `xml:"PTZ"`
					Media struct {
						XAddr string `xml:"XAddr"`
					} `xml:"Media"`
					Imaging struct {
						XAddr string `xml:"XAddr"`
					} `xml:"Imaging"`
				} `xml:"Capabilities"`
			} `xml:"GetCapabilitiesResponse"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("parse capabilities: %w", err)
	}
	c.PTZURL = strings.TrimSpace(env.Body.Resp.Caps.PTZ.XAddr)
	c.MediaURL = strings.TrimSpace(env.Body.Resp.Caps.Media.XAddr)
	c.ImagingURL = strings.TrimSpace(env.Body.Resp.Caps.Imaging.XAddr)
	return nil
}

func (c *Client) getProfiles() error {
	if c.MediaURL == "" {
		c.MediaURL = strings.Replace(c.DeviceURL, "device_service", "Media", 1)
	}
	body := `<trt:GetProfiles/>`
	data, err := c.soapCall(c.MediaURL, "http://www.onvif.org/ver10/media/wsdl/GetProfiles", body)
	if err != nil {
		return err
	}

	var env struct {
		Body struct {
			Resp struct {
				Profiles []struct {
					Token string `xml:"token,attr"`
					VSC   struct {
						SourceToken string `xml:"SourceToken"`
					} `xml:"VideoSourceConfiguration"`
					PTZCfg *struct {
						Token string `xml:"token,attr"`
					} `xml:"PTZConfiguration"`
				} `xml:"Profiles"`
			} `xml:"GetProfilesResponse"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("parse profiles: %w", err)
	}
	for _, p := range env.Body.Resp.Profiles {
		if p.PTZCfg != nil {
			c.ProfileToken = p.Token
			c.VSToken = p.VSC.SourceToken
			return nil
		}
	}
	return nil // not an error; caller checks ProfileToken
}

// ── PTZ operations ────────────────────────────────────────────────────────────

// ContinuousMove starts continuous pan/tilt/zoom movement.
// Values are in [-1, 1]; 0 means no movement on that axis.
func (c *Client) ContinuousMove(pan, tilt, zoom float64) error {
	body := fmt.Sprintf(`<tptz:ContinuousMove>
  <tptz:ProfileToken>%s</tptz:ProfileToken>
  <tptz:Velocity>
    <tt:PanTilt x="%.4f" y="%.4f"/>
    <tt:Zoom x="%.4f"/>
  </tptz:Velocity>
</tptz:ContinuousMove>`, xmlEscape(c.ProfileToken), pan, tilt, zoom)
	_, err := c.soapCall(c.PTZURL, "http://www.onvif.org/ver20/ptz/wsdl/ContinuousMove", body)
	return err
}

// Stop halts all pan/tilt/zoom movement.
func (c *Client) Stop() error {
	body := fmt.Sprintf(`<tptz:Stop>
  <tptz:ProfileToken>%s</tptz:ProfileToken>
  <tptz:PanTilt>true</tptz:PanTilt>
  <tptz:Zoom>true</tptz:Zoom>
</tptz:Stop>`, xmlEscape(c.ProfileToken))
	_, err := c.soapCall(c.PTZURL, "http://www.onvif.org/ver20/ptz/wsdl/Stop", body)
	return err
}

// ── Imaging / focus operations ────────────────────────────────────────────────

// FocusMove starts continuous focus movement.
// speed > 0 = focus far; speed < 0 = focus near.  Range [-1, 1].
func (c *Client) FocusMove(speed float64) error {
	if c.ImagingURL == "" {
		return fmt.Errorf("no Imaging service URL")
	}
	if c.VSToken == "" {
		return fmt.Errorf("no video source token")
	}
	body := fmt.Sprintf(`<timg:Move>
  <timg:VideoSourceToken>%s</timg:VideoSourceToken>
  <timg:Focus>
    <tt:Continuous><tt:Speed>%.4f</tt:Speed></tt:Continuous>
  </timg:Focus>
</timg:Move>`, xmlEscape(c.VSToken), speed)
	_, err := c.soapCall(c.ImagingURL, "http://www.onvif.org/ver20/imaging/wsdl/Move", body)
	return err
}

// FocusStop stops continuous focus movement.
func (c *Client) FocusStop() error {
	if c.ImagingURL == "" {
		return nil
	}
	body := fmt.Sprintf(`<timg:Stop>
  <timg:VideoSourceToken>%s</timg:VideoSourceToken>
</timg:Stop>`, xmlEscape(c.VSToken))
	_, err := c.soapCall(c.ImagingURL, "http://www.onvif.org/ver20/imaging/wsdl/Stop", body)
	return err
}

// SetFocusAuto switches between auto (true) and manual (false) focus mode.
func (c *Client) SetFocusAuto(auto bool) error {
	if c.ImagingURL == "" || c.VSToken == "" {
		return fmt.Errorf("imaging service not available")
	}
	mode := "AUTO"
	if !auto {
		mode = "MANUAL"
	}
	body := fmt.Sprintf(`<timg:SetImagingSettings>
  <timg:VideoSourceToken>%s</timg:VideoSourceToken>
  <timg:ImagingSettings>
    <tt:Focus><tt:AutoFocusMode>%s</tt:AutoFocusMode></tt:Focus>
  </timg:ImagingSettings>
  <timg:ForcePersistence>true</timg:ForcePersistence>
</timg:SetImagingSettings>`, xmlEscape(c.VSToken), xmlEscape(mode))
	_, err := c.soapCall(c.ImagingURL, "http://www.onvif.org/ver20/imaging/wsdl/SetImagingSettings", body)
	return err
}
