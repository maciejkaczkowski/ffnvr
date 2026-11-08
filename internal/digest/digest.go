// Package digest implements HTTP Digest authentication (RFC 2617, qop=auth) with a
// fallback to Basic, for HTTP (Transport) and for RTSP (Auth.Header), as recorders
// (Dahua, Uniview) use it.
package digest

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Auth answers the challenges of one server.
type Auth struct {
	User, Pass string

	mu    sync.Mutex
	chal  map[string]string // realm, nonce, qop, opaque, algorithm
	basic bool
	nc    uint32
}

// New returns the authentication state for one user.
func New(user, pass string) *Auth { return &Auth{User: user, Pass: pass} }

// Ready reports whether a challenge is known (Header gives something).
func (a *Auth) Ready() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.basic || a.chal != nil
}

// Nonce: the nonce of the challenge being answered ("" before one).
func (a *Auth) Nonce() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chal["nonce"]
}

// Learn remembers the challenge of WWW-Authenticate headers; false: another try
// makes no sense.
func (a *Auth) Learn(values []string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, v := range values {
		scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
		if strings.EqualFold(scheme, "digest") {
			a.chal = ParseParams(rest)
			a.nc = 0
			a.basic = false
			return true
		}
	}
	for _, v := range values {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "basic") && !a.basic {
			a.basic = true
			return true
		}
	}
	return false
}

// Header is the Authorization value for a request ("" before a challenge).
func (a *Auth) Header(method, uri string) string { return a.HeaderOpts(method, uri, false) }

// HeaderOpts is Header; noQop answers in the older form of RFC 2069 (without qop, nc
// and cnonce) even when the challenge offers qop - some RTSP servers want that.
func (a *Auth) HeaderOpts(method, uri string, noQop bool) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.basic {
		r, _ := http.NewRequest("GET", "/", nil)
		r.SetBasicAuth(a.User, a.Pass)
		return r.Header.Get("Authorization")
	}
	if a.chal == nil {
		return ""
	}
	a.nc++
	nc := fmt.Sprintf("%08x", a.nc)
	cnonce := randHex(8)
	realm, nonce := a.chal["realm"], a.chal["nonce"]
	ha1 := MD5Hex(a.User + ":" + realm + ":" + a.Pass)
	ha2 := MD5Hex(method + ":" + uri)
	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s"`, a.User, realm, nonce, uri)
	if alg := a.chal["algorithm"]; alg != "" { // only when the challenge names it
		fmt.Fprintf(&b, `, algorithm=%s`, alg)
	}
	if qopHasAuth(a.chal["qop"]) && !noQop {
		resp := MD5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
		fmt.Fprintf(&b, `, qop=auth, nc=%s, cnonce="%s", response="%s"`, nc, cnonce, resp)
	} else {
		fmt.Fprintf(&b, `, response="%s"`, MD5Hex(ha1+":"+nonce+":"+ha2))
	}
	if op := a.chal["opaque"]; op != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, op)
	}
	return b.String()
}

// Transport authenticates HTTP requests.
type Transport struct {
	Auth *Auth
	Base http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	if h := t.Auth.Header(r.Method, r.URL.RequestURI()); h != "" {
		r.Header.Set("Authorization", h)
	}
	resp, err := t.Base.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	// 401: learn the (new) challenge and try again exactly once
	if !t.Auth.Learn(resp.Header.Values("WWW-Authenticate")) {
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	r2 := req.Clone(req.Context())
	if req.GetBody != nil { // the first try has read the body (PUT, POST)
		if r2.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	if h := t.Auth.Header(r2.Method, r2.URL.RequestURI()); h != "" {
		r2.Header.Set("Authorization", h)
	}
	return t.Base.RoundTrip(r2)
}

// ParseParams parses realm="x", nonce="y", qop="auth" (';' is accepted too).
func ParseParams(s string) map[string]string {
	m := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t,;")
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return m
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = s[eq+1:]
		var val string
		if strings.HasPrefix(s, `"`) {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				val, s = s[1:], ""
			} else {
				val, s = s[1:1+end], s[2+end:]
			}
		} else {
			end := strings.IndexAny(s, ",;")
			if end < 0 {
				val, s = s, ""
			} else {
				val, s = s[:end], s[end:]
			}
		}
		m[key] = strings.TrimSpace(val)
	}
}

func qopHasAuth(qop string) bool {
	for _, q := range strings.Split(qop, ",") {
		if strings.TrimSpace(q) == "auth" {
			return true
		}
	}
	return false
}

// MD5Hex is the lower case hex MD5 of s.
func MD5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
