package tiandy

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/digest"
)

// Tiandy recorders also answer ISAPI over HTTP (Digest authentication): the disks, the
// channels with their cameras' addresses and the clock come from there - the records
// after the native login tell the disks unreliably (twice, or not at all).

// DefaultHTTPPort of the web interface.
const DefaultHTTPPort = 80

func (c *Client) web() *http.Client {
	if c.httpc == nil {
		tr := &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 20 * time.Second}
		c.httpc = &http.Client{Transport: &digest.Transport{Auth: digest.New(c.user, c.pass), Base: tr}, Timeout: 30 * time.Second}
	}
	return c.httpc
}

func (c *Client) webURL(path string) string {
	port := c.HTTPPort
	if port == 0 {
		port = DefaultHTTPPort
	}
	return "http://" + net.JoinHostPort(c.Host(), strconv.Itoa(port)) + path
}

// ISAPI fetches a path and decodes its XML into v (nil: only the raw answer).
func (c *Client) ISAPI(ctx context.Context, path string, v any) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.webURL(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.web().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return raw, fmt.Errorf("%s: HTTP 401: wrong username or password", path)
	}
	if resp.StatusCode != http.StatusOK {
		return raw, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	if v != nil {
		if err := xml.Unmarshal(raw, v); err != nil {
			return raw, fmt.Errorf("%s: %w", path, err)
		}
	}
	return raw, nil
}

// HDD of the recorder (sizes in MB).
type HDD struct {
	ID        string `xml:"id"`
	Name      string `xml:"hddName"`
	Type      string `xml:"hddType"`
	Capacity  int64  `xml:"capacity"`
	UsedSpace int64  `xml:"usedSpace"`
	FreeSpace int64  `xml:"freeSpace"`
	Status    string `xml:"status"`
	Property  string `xml:"property"`
}

// Working reports whether the disk records normally. Tiandy firmware (TC-R3120,
// DVRS_V13.13.20) reports "formating" for a disk that records and is full - as captured
// from production - so that counts as working when the disk holds data.
func (h HDD) Working() bool {
	switch strings.ToLower(h.Status) {
	case "ok", "normal", "idle":
		return true
	case "formating", "formatting":
		return h.Capacity > 0 && h.UsedSpace > 0
	}
	return false
}

// Failed reports whether the status means a broken or missing disk.
func (h HDD) Failed() bool {
	switch strings.ToLower(h.Status) {
	case "error", "smartfailed", "offline", "mismatch", "notexist", "abnormal":
		return true
	}
	return false
}

// HDDs: /ISAPI/ContentMgmt/Storage/hdd/.
func (c *Client) HDDs(ctx context.Context) ([]HDD, error) {
	var list struct {
		Hdds []HDD `xml:"hdd"`
	}
	_, err := c.ISAPI(ctx, "/ISAPI/ContentMgmt/Storage/hdd/", &list)
	return list.Hdds, err
}

// ProxyChannel is the state of a channel and its camera.
type ProxyChannel struct {
	ID     string `xml:"id"`
	Online bool   `xml:"online"`
	Source struct {
		ChannelName string `xml:"channelName"`
		IPAddress   string `xml:"ipAddress"`
		Mac         string `xml:"Mac"`
		DevModel    string `xml:"DevModel"`
		Barcode     string `xml:"Barcode"` // the camera's serial number
		AdminPort   string `xml:"adminPortNo"`
	} `xml:"sourceInputPortDescriptor"`
}

// ChannelStatus: /ISAPI/ContentMgmt/InputProxy/channels/status.
func (c *Client) ChannelStatus(ctx context.Context) ([]ProxyChannel, error) {
	var list struct {
		Channels []ProxyChannel `xml:"InputProxyChannelStatus"`
	}
	_, err := c.ISAPI(ctx, "/ISAPI/ContentMgmt/InputProxy/channels/status", &list)
	return list.Channels, err
}

// Time: /ISAPI/System/time (localTime).
func (c *Client) Time(ctx context.Context) (time.Time, error) {
	var t struct {
		LocalTime string `xml:"localTime"`
	}
	if _, err := c.ISAPI(ctx, "/ISAPI/System/time", &t); err != nil {
		return time.Time{}, err
	}
	v := strings.TrimSpace(t.LocalTime)
	if tm, err := time.Parse(time.RFC3339, v); err == nil {
		return tm, nil
	}
	return time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSuffix(v, "Z"), time.Local)
}
