package tiandy

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// PTZ over the web interface's ISAPI (captured from a Tiandy recorder's web interface;
// channel from 1, Digest authentication):
//
//	PUT /ISAPI/PTZCtrl/channels/4/continuous        <PTZData><pan>-50</pan><tilt>50</tilt><zoom/></PTZData>
//	PUT /ISAPI/System/Video/inputs/channels/4/focus <FocusData><focus>-100</focus></FocusData>
//	PUT /ISAPI/System/Video/inputs/channels/4/iris  <IrisData><iris>100</iris></IrisData>
//	PUT /ISAPI/PTZCtrl/channels/4/auxcontrols/0     <PTZAux><id>0</id><type>WIPER</type><status>on</status></PTZAux>
//	PUT /CGI/PTZCtrl/channels/4/manuallaser         <LaserData><LaserEnable>1</LaserEnable></LaserData>
//
// Zero stops a movement (pan, tilt, zoom, focus). The web interface sends no stop for
// the iris: every press changes it by a step. The answer is a ResponseStatus with
// statusCode 1 when done.

func (c *Client) put(ctx context.Context, path, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.webURL(path), bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	resp, err := c.web().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s: HTTP 401: wrong username or password", path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	var st struct {
		Code   int    `xml:"statusCode"`
		String string `xml:"statusString"`
		Sub    string `xml:"subStatusCode"`
	}
	if xml.Unmarshal(raw, &st) == nil && st.Code != 0 && st.Code != 1 {
		return fmt.Errorf("%s: refused: %d %s %s", path, st.Code, st.String, st.Sub)
	}
	return nil
}

func axis(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

// PTZContinuous moves a camera: pan (right +), tilt (up +) and zoom (in +) from -100 to
// 100, 0 stops; nil leaves the axis alone (sent empty, as the web interface does).
func (c *Client) PTZContinuous(ctx context.Context, channel int, pan, tilt, zoom *int) error {
	tag := func(name string, v *int) string {
		if v == nil {
			return "<" + name + "/>"
		}
		return "<" + name + ">" + axis(v) + "</" + name + ">"
	}
	return c.put(ctx, fmt.Sprintf("/ISAPI/PTZCtrl/channels/%d/continuous", channel),
		"<PTZData>"+tag("pan", pan)+tag("tilt", tilt)+tag("zoom", zoom)+"</PTZData>")
}

// PTZFocus: far +, near -, 0 stops.
func (c *Client) PTZFocus(ctx context.Context, channel, v int) error {
	return c.put(ctx, fmt.Sprintf("/ISAPI/System/Video/inputs/channels/%d/focus", channel),
		"<FocusData><focus>"+strconv.Itoa(v)+"</focus></FocusData>")
}

// PTZIris: open +, close - (a step per call).
func (c *Client) PTZIris(ctx context.Context, channel, v int) error {
	return c.put(ctx, fmt.Sprintf("/ISAPI/System/Video/inputs/channels/%d/iris", channel),
		"<IrisData><iris>"+strconv.Itoa(v)+"</iris></IrisData>")
}

// PTZAux switches an auxiliary: WIPER, LIGHT, POWER, DEFOG (as /xml/PTZ/PTZAux.xml names them).
func (c *Client) PTZAux(ctx context.Context, channel int, typ string, on bool) error {
	st := "off"
	if on {
		st = "on"
	}
	return c.put(ctx, fmt.Sprintf("/ISAPI/PTZCtrl/channels/%d/auxcontrols/0", channel),
		"<PTZAux><id>0</id><type>"+typ+"</type><status>"+st+"</status></PTZAux>")
}

// PTZLaser switches the camera's laser (infrared) illuminator.
func (c *Client) PTZLaser(ctx context.Context, channel int, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return c.put(ctx, fmt.Sprintf("/CGI/PTZCtrl/channels/%d/manuallaser", channel),
		`<?xml version="1.0" encoding="UTF-8"?><LaserData><LaserEnable>`+v+`</LaserEnable></LaserData>`)
}
