package tiandy

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *Client) webDo(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.webURL(path), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.web().Do(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP 401: wrong username or password", path)
	}
	return resp, err
}

// Reboot restarts the recorder (PUT /ISAPI/System/reboot).
func (c *Client) Reboot(ctx context.Context) error {
	resp, err := c.webDo(ctx, http.MethodPut, "/ISAPI/System/reboot", "application/xml; charset=utf-8", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var st struct {
		StatusCode   int    `xml:"statusCode"`
		StatusString string `xml:"statusString"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("System/reboot: HTTP %d", resp.StatusCode)
	}
	if xml.Unmarshal(raw, &st) == nil && st.StatusCode != 1 && st.StatusString != "" {
		return fmt.Errorf("System/reboot: %s", st.StatusString)
	}
	return nil
}

// Backup exports the configuration (.box), as the web interface does: a token from
// /CGI/UploadCheck, then a POST of it to /CGI/System/S/configData/export/<time>.box.
func (c *Client) Backup(ctx context.Context) (io.ReadCloser, error) {
	if resp, err := c.webDo(ctx, http.MethodPut, "/CGI/UploadCheck", "application/xml; charset=utf-8", nil); err == nil {
		resp.Body.Close()
	}
	resp, err := c.webDo(ctx, http.MethodGet, "/CGI/UploadCheck", "", nil)
	if err != nil {
		return nil, err
	}
	var check struct {
		Token string `xml:"token"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if xml.Unmarshal(raw, &check) != nil || check.Token == "" {
		return nil, fmt.Errorf("UploadCheck: no token (HTTP %d)", resp.StatusCode)
	}
	path := "/CGI/System/S/configData/export/" + time.Now().Format("20060102150405") + ".box"
	resp, err = c.webDo(ctx, http.MethodPost, path, "application/x-www-form-urlencoded", strings.NewReader("csrf_token="+check.Token))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || strings.Contains(resp.Header.Get("Content-Type"), "xml") {
		resp.Body.Close()
		return nil, fmt.Errorf("configuration export: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}
