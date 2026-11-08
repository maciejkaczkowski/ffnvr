package dahua

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// SessionID of a JSON-RPC session (the web interface's cookie WebClientHttpSessionID).
func (r *RPC) SessionID() string {
	switch s := r.session.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatInt(int64(s), 10)
	}
	return fmt.Sprint(r.session)
}

// Reboot restarts the recorder (JSON-RPC magicBox.reboot, as the web interface does;
// the CGI magicBox.cgi?action=reboot on older firmware).
func (c *Client) Reboot(ctx context.Context) error {
	if rpc, err := c.LoginRPC(ctx); err == nil {
		_, err := rpc.Call(ctx, "magicBox.reboot", nil, nil)
		if err == nil {
			return nil // no logout: the recorder is going down
		}
		rpc.Close()
	}
	_, err := c.cgi(ctx, "magicBox.cgi", param{"action", "reboot"})
	return err
}

// Backup exports the whole configuration (configFileExport.backup?action=All, the
// file the web interface downloads; encrypted by the recorder).
func (c *Client) Backup(ctx context.Context) (io.ReadCloser, error) {
	q := "action=All&" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	// the web interface's way: the JSON-RPC session as a cookie
	if rpc, err := c.LoginRPC(ctx); err == nil {
		u := *c.base
		u.Path, u.RawQuery = "/cgi-bin/configFileExport.backup", q
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		req.AddCookie(&http.Cookie{Name: "WebClientHttpSessionID", Value: rpc.SessionID()})
		resp, err := c.plain.RoundTrip(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return readCloser{resp.Body, closeAll{resp.Body, rpc}}, nil
		}
		if err == nil {
			resp.Body.Close()
		}
		rpc.Close()
	}
	// older firmware: Digest
	resp, err := c.get(ctx, "/cgi-bin/configFileExport.backup", q)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("configFileExport.backup: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

type closeAll struct {
	body io.Closer
	rpc  *RPC
}

func (c closeAll) Close() error { c.rpc.Close(); return c.body.Close() }
