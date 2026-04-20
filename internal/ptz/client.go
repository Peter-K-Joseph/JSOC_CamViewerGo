// Package ptz provides a minimal hand-rolled ONVIF SOAP client for PTZ and
// Imaging (focus) control.  No external dependencies — only stdlib.
package ptz

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // ONVIF WS-Security mandates SHA1
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jsoc/camviewer/internal/netutil"
)

// ── SOAP / WS-Security helpers ───────────────────────────────────────────────

func wsseHeader(username, password string) string {
	nonce := make([]byte, 16)
	rand.Read(nonce) //nolint:errcheck
	nonceB64 := base64.StdEncoding.EncodeToString(nonce)
	created := time.Now().UTC().Format(time.RFC3339)

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
	return `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
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
func (c *Client) soapCall(url, action, body string) ([]byte, error) {
	payload := soapEnvelope(wsseHeader(c.Username, c.password), body)
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", fmt.Sprintf(`application/soap+xml; charset=utf-8; action="%s"`, action))
	req.Header.Set("SOAPAction", action)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if fault := soapFault(data); fault != "" {
			return nil, fmt.Errorf("HTTP %d SOAP fault: %s", resp.StatusCode, fault)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 1000))
	}
	if fault := soapFault(data); fault != "" {
		return nil, fmt.Errorf("SOAP fault: %s", fault)
	}
	return data, nil
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

// ── Client ───────────────────────────────────────────────────────────────────

// Client is an authenticated ONVIF client for one camera.
type Client struct {
	DeviceURL  string
	PTZURL     string
	MediaURL   string
	ImagingURL string
	Username   string
	password   string

	// Resolved at probe time.
	ProfileToken string // token of first PTZ-capable media profile
	VSToken      string // video source token for that profile

	http *http.Client
}

// Probe creates and validates an ONVIF client for the given camera.
// It performs GetCapabilities → GetProfiles to confirm PTZ support.
func Probe(ip string, port int, username, password string) (*Client, error) {
	var err error
	ip, port, err = netutil.NormalizeHostPort(ip, port)
	if err != nil {
		return nil, fmt.Errorf("invalid camera address: %w", err)
	}
	c := &Client{
		DeviceURL: fmt.Sprintf("http://%s:%d/onvif/device_service", ip, port),
		Username:  username,
		password:  password,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
	if err := c.getCapabilities(); err != nil {
		return nil, fmt.Errorf("GetCapabilities: %w", err)
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
</tptz:ContinuousMove>`, c.ProfileToken, pan, tilt, zoom)
	_, err := c.soapCall(c.PTZURL, "http://www.onvif.org/ver20/ptz/wsdl/ContinuousMove", body)
	return err
}

// Stop halts all pan/tilt/zoom movement.
func (c *Client) Stop() error {
	body := fmt.Sprintf(`<tptz:Stop>
  <tptz:ProfileToken>%s</tptz:ProfileToken>
  <tptz:PanTilt>true</tptz:PanTilt>
  <tptz:Zoom>true</tptz:Zoom>
</tptz:Stop>`, c.ProfileToken)
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
</timg:Move>`, c.VSToken, speed)
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
</timg:Stop>`, c.VSToken)
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
</timg:SetImagingSettings>`, c.VSToken, mode)
	_, err := c.soapCall(c.ImagingURL, "http://www.onvif.org/ver20/imaging/wsdl/SetImagingSettings", body)
	return err
}
