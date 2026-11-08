package web

import (
	"bytes"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// headers adds headers to every request: what a reverse proxy adds.
type headers struct {
	h  map[string]string
	rt http.RoundTripper
}

func (x headers) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range x.h {
		r.Header.Set(k, v)
	}
	return x.rt.RoundTrip(r)
}

func proxied(h map[string]string) *http.Client {
	c := client()
	c.Transport = headers{h, http.DefaultTransport}
	return c
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func setCookies(t *testing.T, c *http.Client, url string) []string {
	t.Helper()
	resp, err := c.Get(url + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Values("Set-Cookie")
}

// Behind an HTTPS reverse proxy the cookies are Secure, without any setting.
func TestSecureCookiesBehindProxy(t *testing.T) {
	srv, _ := setup(t)
	for _, c := range []struct {
		h      map[string]string
		secure bool
	}{
		{nil, false},
		{map[string]string{"X-Forwarded-Proto": "https"}, true},
		{map[string]string{"X-Forwarded-Proto": "HTTPS, http"}, true},
		{map[string]string{"X-Forwarded-Proto": "http"}, false},
	} {
		ck := setCookies(t, proxied(c.h), srv.URL)
		csrf := ""
		for _, v := range ck {
			if strings.HasPrefix(v, "ffnvr_csrf=") {
				csrf = v
			}
		}
		if csrf == "" || strings.Contains(csrf, "; Secure") != c.secure {
			t.Errorf("%v: %q", c.h, csrf)
		}
	}
	// the session's cookie too (the form's token taken over plain HTTP: a Secure cookie is
	// not sent back over it, as a browser would not)
	c := client()
	_, page := get(t, c, srv.URL+"/login")
	m := csrfRe.FindStringSubmatch(page)
	c.Transport = headers{map[string]string{"X-Forwarded-Proto": "https"}, http.DefaultTransport}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.PostForm(srv.URL+"/login", map[string][]string{"csrf": {m[1]}, "username": {"jan"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	found := false
	for _, v := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(v, cookieName+"=") {
			found = true
			if !strings.Contains(v, "; Secure") || !strings.Contains(v, "HttpOnly") {
				t.Errorf("session cookie: %q", v)
			}
		}
	}
	if resp.StatusCode != http.StatusSeeOther || !found {
		t.Errorf("%d: no session cookie", resp.StatusCode)
	}
}

// Behind a reverse proxy of proxy=, the browser's address is what the proxy says: one
// user's wrong passwords do not block the others; logs name the browser.
func TestClientAddressBehindProxy(t *testing.T) {
	logs := &syncBuf{}
	srv, _ := setupWith(t, func(o *Options) {
		o.Proxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")} // the test is the proxy
		o.Log = log.New(logs, "", 0)
	})
	a := proxied(map[string]string{"X-Forwarded-For": "203.0.113.5"})
	b := proxied(map[string]string{"X-Forwarded-For": "198.51.100.7"})
	for i := 0; i < maxFailures; i++ {
		signIn(t, a, srv.URL, "jan", "zle")
	}
	if code, page := signIn(t, a, srv.URL, "jan", "secret"); code != 429 || !strings.Contains(page, "Too many") {
		t.Errorf("a: blocked: %d", code)
	}
	if code, _ := signIn(t, b, srv.URL, "jan", "secret"); code != 200 {
		t.Errorf("b is someone else: %d", code)
	}
	if l := logs.String(); !strings.Contains(l, `failed sign in of "jan" from 203.0.113.5`) || !strings.Contains(l, "jan signed in from 198.51.100.7") {
		t.Errorf("logs:\n%s", l)
	}
	// written by the browser before the proxy added its own: the last one counts
	spoof := proxied(map[string]string{"X-Forwarded-For": "198.51.100.7, 203.0.113.5"})
	if code, _ := signIn(t, spoof, srv.URL, "jan", "secret"); code != 429 {
		t.Errorf("a pretending to be b: %d", code)
	}
}

// Without proxy= the header is anybody's: it is not believed.
func TestClientAddressNoProxy(t *testing.T) {
	srv, _ := setup(t)
	for i := 0; i < maxFailures; i++ {
		signIn(t, proxied(map[string]string{"X-Forwarded-For": "203.0.113.5"}), srv.URL, "jan", "zle")
	}
	if code, _ := signIn(t, proxied(map[string]string{"X-Forwarded-For": "198.51.100.9"}), srv.URL, "jan", "secret"); code != 429 {
		t.Errorf("a made-up X-Forwarded-For gets round the block: %d", code)
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{opt: Options{Proxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32"), netip.MustParsePrefix("10.1.0.0/16")}}}
	for _, c := range []struct{ peer, xff, want string }{
		{"10.0.0.5:4000", "203.0.113.5", "203.0.113.5"},
		{"10.0.0.5:4000", "1.2.3.4, 203.0.113.5", "203.0.113.5"},
		{"10.0.0.5:4000", "203.0.113.5, 10.1.2.3", "203.0.113.5"}, // two proxies
		{"10.0.0.5:4000", "", "10.0.0.5"},
		{"10.0.0.5:4000", "garbage", "10.0.0.5"},
		{"192.168.1.9:4000", "203.0.113.5", "192.168.1.9"}, // not a proxy
		{"[::ffff:10.0.0.5]:4000", "2001:db8::1", "2001:db8::1"},
	} {
		r, _ := http.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientIP(r).String(); got != c.want {
			t.Errorf("%s %q: %s, want %s", c.peer, c.xff, got, c.want)
		}
	}
	// the live view's WebSocket: a proxy that changes Host forwards the browser's
	r, _ := http.NewRequest("GET", "/live/ws", nil)
	r.RemoteAddr, r.Host = "10.0.0.5:4000", "10.0.0.20:8080"
	r.Header.Set("Origin", "https://nvr.example.com")
	if s.sameOrigin(r) {
		t.Error("another origin")
	}
	r.Header.Set("X-Forwarded-Host", "nvr.example.com")
	if !s.sameOrigin(r) {
		t.Error("the host the proxy forwards")
	}
	r.RemoteAddr = "192.168.1.9:4000" // not a proxy: its header means nothing
	if s.sameOrigin(r) {
		t.Error("X-Forwarded-Host from anyone")
	}
}
