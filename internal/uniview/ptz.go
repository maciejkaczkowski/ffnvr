package uniview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// PTZ commands of LAPI (PUT /LAPI/V1.0/Channels/<channel>/PTZ/PTZCtrl, channel from 1),
// as the web interface sends them (captured from a BCS Point NVR): {"PTZCmd":1026,
// "Para1":0,"Para2":5} starts tilting up, {"PTZCmd":1025,"Para1":5,"Para2":5} stops it.
// A command's stop is the command minus one; Para1 is the pan speed, Para2 the tilt
// speed (1-9). The auxiliaries are switched on and off without parameters.
const (
	PTZIrisClose = 0x0102
	PTZIrisOpen  = 0x0104
	PTZFocusNear = 0x0202
	PTZFocusFar  = 0x0204
	PTZZoomTele  = 0x0302
	PTZZoomWide  = 0x0304
	PTZUp        = 0x0402
	PTZDown      = 0x0404
	PTZRight     = 0x0502
	PTZLeft      = 0x0504
	PTZLeftUp    = 0x0702
	PTZLeftDown  = 0x0704
	PTZRightUp   = 0x0802
	PTZRightDown = 0x0804
	PTZAllStop   = 0x0901
	PTZWiperOn   = 0x0A01 // the brush; every auxiliary: off = on + 1
	PTZLightOn   = 0x0B01
	PTZHeaterOn  = 0x0C01
	PTZDeicingOn = 0x1201 // snow removal, de-icing
)

// PTZ sends one command to a channel (from 1); params nil: none (stop all, auxiliaries).
func (c *Client) PTZ(ctx context.Context, channel, cmd int, para1, para2 *int) error {
	body := map[string]any{"PTZCmd": cmd}
	if para1 != nil {
		body["Para1"] = *para1
	}
	if para2 != nil {
		body["Para2"] = *para2
	}
	b, _ := json.Marshal(body)
	path := fmt.Sprintf("/LAPI/V1.0/Channels/%d/PTZ/PTZCtrl", channel)
	u := *c.base
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("PTZCtrl: HTTP 401: wrong username or password")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PTZCtrl: HTTP %d", resp.StatusCode)
	}
	var env envelope
	if json.Unmarshal(raw, &env) == nil && env.Response.ResponseCode != 0 {
		return fmt.Errorf("PTZCtrl %#04x: %w: %d %s", cmd, ErrDevice, env.Response.ResponseCode, env.Response.ResponseString)
	}
	return nil
}
