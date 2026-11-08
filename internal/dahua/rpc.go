package dahua

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The JSON-RPC interface (/RPC2) of the web interface. Newer firmware answers some
// questions only there - e.g. the connection state of the cameras. It needs its own
// login (/RPC2_Login): a challenge with realm and random, answered with
// MD5(user:random:MD5(user:realm:password)), both in upper case hex.

type rpcRequest struct {
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      int    `json:"id"`
	Session any    `json:"session,omitempty"`
	Object  any    `json:"object,omitempty"` // an instance (ptz.factory.instance)
}

type rpcResponse struct {
	Result  json.RawMessage `json:"result"`
	Params  json.RawMessage `json:"params"`
	Session any             `json:"session"`
	Error   *struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *rpcResponse) ok() bool {
	return string(r.Result) == "true" || (len(r.Result) > 0 && r.Result[0] == '{')
}

// RPC is a logged in JSON-RPC session.
type RPC struct {
	c       *Client
	session any
	id      int
}

func (c *Client) post(ctx context.Context, path string, req rpcRequest) (*rpcResponse, []byte, error) {
	body, _ := json.Marshal(req)
	u := *c.base
	u.Path = path
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if s, ok := req.Session.(string); ok && s != "" {
		hr.AddCookie(&http.Cookie{Name: "DWebClientSessionID", Value: s})
	}
	// the RPC interface has its own login: no Digest
	resp, err := c.plain.RoundTrip(hr)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, raw, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	var r rpcResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, raw, fmt.Errorf("%s: not JSON: %.80q", path, raw)
	}
	return &r, raw, nil
}

func md5upper(s string) string {
	h := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

// LoginRPC opens a JSON-RPC session; Close it when done.
func (c *Client) LoginRPC(ctx context.Context) (*RPC, error) {
	user, pass := c.user, c.pass
	params := map[string]any{"userName": user, "password": "", "clientType": "Web3.0", "loginType": "Direct"}
	r, _, err := c.post(ctx, "/RPC2_Login", rpcRequest{Method: "global.login", Params: params, ID: 1})
	if err != nil {
		return nil, fmt.Errorf("RPC2 login: %w", err)
	}
	var ch struct {
		Realm      string `json:"realm"`
		Random     string `json:"random"`
		Encryption string `json:"encryption"`
	}
	if err := json.Unmarshal(r.Params, &ch); err != nil || ch.Random == "" {
		return nil, errors.New("RPC2 login: no challenge in the answer")
	}
	if ch.Encryption != "" && ch.Encryption != "Default" {
		return nil, fmt.Errorf("RPC2 login: unsupported encryption %q", ch.Encryption)
	}
	params["password"] = md5upper(user + ":" + ch.Random + ":" + md5upper(user+":"+ch.Realm+":"+pass))
	params["authorityType"], params["passwordType"] = "Default", "Default"
	r2, _, err := c.post(ctx, "/RPC2_Login", rpcRequest{Method: "global.login", Params: params, ID: 2, Session: r.Session})
	if err != nil {
		return nil, fmt.Errorf("RPC2 login: %w", err)
	}
	if !r2.ok() {
		msg := "refused"
		if r2.Error != nil {
			msg = r2.Error.Message
		}
		return nil, fmt.Errorf("RPC2 login: wrong username or password (%s)", msg)
	}
	s := r2.Session
	if s == nil {
		s = r.Session
	}
	return &RPC{c: c, session: s, id: 2}, nil
}

// Call runs a method and decodes its params into out (may be nil); raw is the answer.
func (r *RPC) Call(ctx context.Context, method string, params, out any) (raw []byte, err error) {
	r.id++
	resp, raw, err := r.c.post(ctx, "/RPC2", rpcRequest{Method: method, Params: params, ID: r.id, Session: r.session})
	if err != nil {
		return raw, fmt.Errorf("%s: %w", method, err)
	}
	if !resp.ok() {
		msg := "refused"
		if resp.Error != nil {
			msg = fmt.Sprintf("%s (%d)", resp.Error.Message, resp.Error.Code)
		}
		return raw, fmt.Errorf("%s: %w: %s", method, ErrDevice, msg)
	}
	if out != nil && len(resp.Params) > 0 {
		if err := json.Unmarshal(resp.Params, out); err != nil {
			return raw, fmt.Errorf("%s: %w", method, err)
		}
	}
	return raw, nil
}

// Close logs out.
func (r *RPC) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	r.Call(ctx, "global.logout", nil, nil)
}
