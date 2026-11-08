// Package hikvision is a client of the ISAPI of Hikvision cameras (HTTP, Digest
// authentication, XML) - for now the event stream (events.go). ISAPI is documented by
// Hikvision itself ("ISAPI Core Protocol", Event/notification/alertStream); the handling
// of the stream follows github.com/toxuin/alarmserver (servers/hikvision) and what is
// known of the firmware from it and from Home Assistant's pyhik.
package hikvision

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ffnvr/internal/digest"
)

// Client talks to one camera.
type Client struct {
	base *url.URL
	http *http.Client
}

// NewClient accepts "192.168.1.2", "192.168.1.2:8080" or "https://cam.lan"; a bare
// address with port 443 is spoken to over HTTPS. The certificate is not checked (the
// cameras' own are self-signed).
func NewClient(addr, user, pass string) (*Client, error) {
	if !strings.Contains(addr, "://") {
		scheme := "http"
		if _, port, err := net.SplitHostPort(addr); err == nil && port == "443" {
			scheme = "https"
		}
		addr = scheme + "://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid address %q", addr)
	}
	u.Path, u.RawPath, u.RawQuery, u.User = "", "", "", nil
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	}
	// no global timeout: the event stream stays open; callers use contexts
	return &Client{base: u, http: &http.Client{Transport: &digest.Transport{Auth: digest.New(user, pass), Base: tr}}}, nil
}

// Host is the host name of the camera (without port).
func (c *Client) Host() string { return c.base.Hostname() }

func (c *Client) url(path string) string {
	u := *c.base
	u.Path = path
	return u.String()
}
