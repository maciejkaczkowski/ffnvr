package uniview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func (c *Client) request(ctx context.Context, method, pathQuery string) (*http.Response, error) {
	path, query, _ := strings.Cut(pathQuery, "?")
	u := *c.base
	u.Path, u.RawQuery = path, query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// Reboot restarts the recorder (PUT /LAPI/V1.0/System/Reboot).
func (c *Client) Reboot(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodPut, "/LAPI/V1.0/System/Reboot")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("System/Reboot: HTTP %d", resp.StatusCode)
	}
	var env envelope
	if json.Unmarshal(raw, &env) == nil && env.Response.ResponseCode != 0 {
		return fmt.Errorf("System/Reboot: %w: %d %s", ErrDevice, env.Response.ResponseCode, env.Response.ResponseString)
	}
	return nil
}

// Backup exports the configuration (Config.xml). Newer recorders give it at once
// (System/Configuration); older ones tell where to fetch it (System/ConfigurationInfo
// -> URL).
func (c *Client) Backup(ctx context.Context) (io.ReadCloser, error) {
	file := func(resp *http.Response) (io.ReadCloser, bool) {
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if resp.StatusCode == http.StatusOK && (strings.Contains(ct, "octet") || resp.Header.Get("Content-Disposition") != "") {
			return resp.Body, true
		}
		resp.Body.Close()
		return nil, false
	}
	if resp, err := c.request(ctx, http.MethodGet, "/LAPI/V1.0/System/Configuration"); err == nil {
		if body, ok := file(resp); ok {
			return body, nil
		}
	}
	var info struct {
		URL string `json:"URL"`
	}
	if _, err := c.Get(ctx, "/LAPI/V1.0/System/ConfigurationInfo", nil, &info); err != nil {
		return nil, fmt.Errorf("configuration export: %w", err)
	}
	if info.URL == "" {
		return nil, fmt.Errorf("configuration export: no URL in the answer")
	}
	resp, err := c.request(ctx, http.MethodGet, info.URL)
	if err != nil {
		return nil, err
	}
	if body, ok := file(resp); ok {
		return body, nil
	}
	return nil, fmt.Errorf("configuration export: %s: HTTP %d", info.URL, resp.StatusCode)
}
