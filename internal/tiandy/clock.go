package tiandy

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// The clock, as the web interface sets it (captured on a TC-R3105, the NTP switch
// confirmed on four recorders):
//
//	GET/PUT /ISAPI/System/time <Time><timeMode>manual</timeMode>
//	    <localTime>2026-11-05T09:57:55</localTime> (empty: the clock stays)
//	    <timeZone>CST+1:00:00DST01:00:00,M2.0.0/02:00:00,M9.0.0/03:00:00</timeZone></Time>
//	GET/PUT /CGI/System/Network/NTP <NTP><NTPServer/><PortNO/><Inerval/> (minutes)</NTP>
//	GET/PUT /CGI/System/CommonEnable/channels/1/Type/45076
//	    <CommonEnable><type>45076</type><enable>true|false</enable><value>0</value></CommonEnable>
//
// The NTP switch is the CommonEnable of type 45076; timeMode stays manual either way
// (it is not the switch, unlike Hikvision's ISAPI).

// NTP are the time server settings of a recorder.
type NTP struct {
	Enabled  bool
	Server   string
	Port     int
	Interval int // minutes
}

// Clock is what a recorder tells about its time.
type Clock struct {
	Time     time.Time
	Zone     string
	NTP      NTP
	NTPKnown bool
}

type isapiTime struct {
	XMLName   xml.Name `xml:"Time"`
	TimeMode  string   `xml:"timeMode"`
	LocalTime string   `xml:"localTime"`
	TimeZone  string   `xml:"timeZone"`
}

// ntpSwitch: the CommonEnable that switches NTP.
const ntpSwitch = "/CGI/System/CommonEnable/channels/1/Type/45076"

type commonEnable struct {
	XMLName xml.Name `xml:"CommonEnable"`
	Type    int      `xml:"type"`
	Enable  bool     `xml:"enable"`
	Value   int      `xml:"value"`
}

type cgiNTP struct {
	XMLName  xml.Name `xml:"NTP"`
	Server   string   `xml:"NTPServer"`
	Port     int      `xml:"PortNO"`
	Interval int      `xml:"Inerval"` // sic
}

const timeLayoutISAPI = "2006-01-02T15:04:05"

func parseLocal(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if tm, err := time.Parse(time.RFC3339, v); err == nil {
		return tm, nil
	}
	return time.ParseInLocation(timeLayoutISAPI, strings.TrimSuffix(v, "Z"), time.Local)
}

// ClockInfo reads the clock, the time zone and the NTP settings.
func (c *Client) ClockInfo(ctx context.Context) (Clock, error) {
	var t isapiTime
	if _, err := c.ISAPI(ctx, "/ISAPI/System/time", &t); err != nil {
		return Clock{}, err
	}
	tm, err := parseLocal(t.LocalTime)
	if err != nil {
		return Clock{}, fmt.Errorf("/ISAPI/System/time: %w", err)
	}
	out := Clock{Time: tm, Zone: t.TimeZone}
	var sw commonEnable
	if _, err := c.ISAPI(ctx, ntpSwitch, &sw); err == nil {
		out.NTP.Enabled, out.NTPKnown = sw.Enable, true
	}
	var n cgiNTP
	if _, err := c.ISAPI(ctx, "/CGI/System/Network/NTP", &n); err == nil {
		out.NTP.Server, out.NTP.Port, out.NTP.Interval = strings.TrimSpace(n.Server), n.Port, n.Interval
	} else {
		out.NTPKnown = false
	}
	return out, nil
}

// putXML sends a document with put (see ptz.go).
func (c *Client) putXML(ctx context.Context, path string, doc any) error {
	b, err := xml.Marshal(doc)
	if err != nil {
		return err
	}
	return c.put(ctx, path, xml.Header+string(b))
}

// SetTime sets the recorder's clock to the wall time of t (in time.Local); the time
// zone and NTP stay (with NTP on, the recorder may correct the clock later).
func (c *Client) SetTime(ctx context.Context, t time.Time) error {
	var cur isapiTime
	if _, err := c.ISAPI(ctx, "/ISAPI/System/time", &cur); err != nil {
		return err
	}
	return c.putXML(ctx, "/ISAPI/System/time", isapiTime{TimeMode: "manual", LocalTime: t.In(time.Local).Format(timeLayoutISAPI), TimeZone: cur.TimeZone})
}

// SetNTP changes the time server, then the NTP switch (CommonEnable 45076), as the web
// interface does; the clock and the time zone stay.
func (c *Client) SetNTP(ctx context.Context, n NTP) error {
	port, interval := n.Port, n.Interval
	var old cgiNTP
	if _, err := c.ISAPI(ctx, "/CGI/System/Network/NTP", &old); err == nil {
		if port <= 0 {
			port = old.Port
		}
		if interval <= 0 {
			interval = old.Interval
		}
	}
	if port <= 0 {
		port = 123
	}
	if interval <= 0 {
		interval = 60
	}
	if err := c.putXML(ctx, "/CGI/System/Network/NTP", cgiNTP{Server: n.Server, Port: port, Interval: interval}); err != nil {
		return err
	}
	return c.putXML(ctx, ntpSwitch, commonEnable{Type: 45076, Enable: n.Enabled})
}
