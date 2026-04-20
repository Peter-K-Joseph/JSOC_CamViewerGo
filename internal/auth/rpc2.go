package auth

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/jsoc/camviewer/internal/netutil"
)

// Session holds the authenticated Dahua RPC2 session.
type Session struct {
	Host      string
	Port      int
	Username  string
	Password  string
	SessionID int
	Cookies   []*http.Cookie
}

type rpcRequest struct {
	Method  string         `json:"method"`
	ID      int            `json:"id"`
	Session int            `json:"session"`
	Params  map[string]any `json:"params"`
}

type rpcResponse struct {
	ID     int            `json:"id"`
	Error  *rpcError      `json:"error"`
	Result map[string]any `json:"result"`
	Params map[string]any `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Login performs the two-stage Dahua RPC2 MD5 challenge-response authentication.
func Login(host string, port int, username, password string) (*Session, error) {
	var err error
	host, port, err = netutil.NormalizeHostPort(host, port)
	if err != nil {
		return nil, fmt.Errorf("invalid camera address: %w", err)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 10 * time.Second,
		Jar:     jar,
	}
	base := fmt.Sprintf("http://%s:%d", host, port)

	// Stage 1: get challenge
	req1 := rpcRequest{
		Method:  "global.login",
		ID:      1,
		Session: 0,
		Params: map[string]any{
			"userName":      username,
			"password":      "",
			"clientType":    "Web3.0",
			"ipAddr":        "0.0.0.0",
			"userLoginType": "Direct",
			"authorityType": "Default",
			"passwordType":  "Default",
		},
	}

	resp1, err := doRPC(client, base, req1)
	if err != nil {
		return nil, fmt.Errorf("rpc2 challenge: %w", err)
	}

	params := resp1.Params
	if params == nil {
		params = resp1.Result
	}

	realm, _ := params["realm"].(string)
	random, _ := params["random"].(string)
	sessionIDf, _ := params["sessionID"].(float64)
	sessionID := int(sessionIDf)

	if realm == "" || random == "" {
		return nil, fmt.Errorf("rpc2: no challenge params in response")
	}

	// Compute login hash: MD5(MD5(user:realm:pass):random:MD5(user:realm:pass))
	// Dahua uses: MD5(username:realm:password) -> hex uppercase
	// then MD5(hash1:random:hash1) -> hex uppercase — but actual formula is:
	// loginHash = MD5(UPPERCASE(hash1) + ":" + random + ":" + UPPERCASE(hash1))
	hash1 := md5Hex(username + ":" + realm + ":" + password)
	loginHash := md5Hex(hash1 + ":" + random + ":" + hash1)

	// Stage 2: authenticate
	req2 := rpcRequest{
		Method:  "global.login",
		ID:      2,
		Session: sessionID,
		Params: map[string]any{
			"userName":      username,
			"password":      loginHash,
			"clientType":    "Web3.0",
			"ipAddr":        "0.0.0.0",
			"userLoginType": "Direct",
			"authorityType": "Default",
			"passwordType":  "Default",
		},
	}

	resp2, err := doRPC(client, base, req2)
	if err != nil {
		return nil, fmt.Errorf("rpc2 login: %w", err)
	}
	if resp2.Error != nil && resp2.Error.Code != 0 {
		return nil, fmt.Errorf("rpc2 auth failed: %s (code %d)", resp2.Error.Message, resp2.Error.Code)
	}

	// Collect cookies for WebSocket auth
	u, _ := url.Parse(base)
	cookies := jar.Cookies(u)

	return &Session{
		Host:      host,
		Port:      port,
		Username:  username,
		Password:  password,
		SessionID: sessionID,
		Cookies:   cookies,
	}, nil
}

func doRPC(client *http.Client, base string, req rpcRequest) (*rpcResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(base+"/RPC2", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse rpc response: %w", err)
	}
	return &rpcResp, nil
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%X", h[:])
}
