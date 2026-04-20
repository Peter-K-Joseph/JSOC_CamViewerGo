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
	ID      int            `json:"id"`
	Error   *rpcError      `json:"error"`
	Result  any            `json:"result"`
	Params  map[string]any `json:"params"`
	Session int            `json:"session"`
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
		ID:      2,
		Session: 0,
		Params: map[string]any{
			"userName":   username,
			"password":   "",
			"clientType": "Web3.0",
			"loginType":  "Direct",
		},
	}

	resp1, err := doRPC(client, base+"/RPC2_Login", req1)
	if err != nil {
		return nil, fmt.Errorf("rpc2 challenge: %w", err)
	}

	params := resp1.Params
	if params == nil {
		params = resultMap(resp1.Result)
	}

	realm, _ := params["realm"].(string)
	random, _ := params["random"].(string)
	sessionID := resp1.Session
	if sessionID == 0 {
		sessionID = intParam(params, "session")
	}
	if sessionID == 0 {
		sessionID = intParam(params, "sessionID")
	}

	if realm == "" || random == "" {
		return nil, fmt.Errorf("rpc2: no challenge params in response")
	}

	// Dahua RPC2 login: MD5(username:realm:password), then
	// MD5(username:random:firstHash). Some firmware rejects the alternate
	// MD5(firstHash:random:firstHash) form.
	hash1 := md5Hex(username + ":" + realm + ":" + password)
	loginHash := md5Hex(username + ":" + random + ":" + hash1)

	// Stage 2: authenticate
	req2 := rpcRequest{
		Method:  "global.login",
		ID:      3,
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

	resp2, err := doRPC(client, base+"/RPC2_Login", req2)
	if err != nil {
		return nil, fmt.Errorf("rpc2 login: %w", err)
	}
	if resp2.Error != nil && resp2.Error.Code != 0 {
		return nil, fmt.Errorf("rpc2 auth failed: %s (code %d)", resp2.Error.Message, resp2.Error.Code)
	}
	if ok, present := resultBool(resp2.Result); present && !ok {
		return nil, fmt.Errorf("rpc2 auth failed")
	}
	if resp2.Session != 0 {
		sessionID = resp2.Session
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

func doRPC(client *http.Client, endpoint string, req rpcRequest) (*rpcResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
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

func intParam(params map[string]any, key string) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func resultMap(result any) map[string]any {
	if m, ok := result.(map[string]any); ok {
		return m
	}
	return nil
}

func resultBool(result any) (bool, bool) {
	if b, ok := result.(bool); ok {
		return b, true
	}
	return false, false
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%X", h[:])
}
