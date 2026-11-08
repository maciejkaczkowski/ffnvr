package tiandy

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// The search of recordings, as the recorder's web client does it (ISAPI, captured from a
// TC-R3120 with firmware DVRS_V13.13.20):
//
//	POST /ISAPI/ContentMgmt/record/tracks/<channel>/type/1/dailyDistribution
//	     <trackDailyParam><year>2026</year><monthOfYear>10</monthOfYear></trackDailyParam>
//	  -> <trackDailyDistribution><dayList><day><dayOfMonth>6</dayOfMonth><record>true</record>…
//	POST /ISAPI/ContentMgmt/dailySearch
//	     <CDailySearchDescription><day>2026-10-20</day>…<chanNo>0</chanNo>…
//	  -> <CDailySearchResult><numOfMatches>1393</numOfMatches><matchList><matchElement>
//	     <chanNo>0</chanNo><type>Alarm</type><timeSpan><startTime>2026-10-20T00:00:00Z</startTime>…
//
// The track counts channels from 1, chanNo from 0. A day is answered whole, its spans
// cut at midnight, by type (Alarm, Timer…) and not in order, all of them although the
// client asks for 300 (some hundred kB for a day of motion recording). The times are of
// the recorder's wall clock with a "Z" that is not UTC.
//
// A day of much motion is answered 400 "Device Error", every time (a TC-R3105, three
// times the same day of the same channel, the other days fine): the largest answer that
// came was 299 642 bytes, as if the recorder had room for 300 KiB. Its own web client
// shows nothing then. ErrRefused tells it; a search of one type of recording (Timer,
// Alarm) is about half of it. Searches of a recorder go one at a time all the same, and
// a refused one is tried once more (it may be busy).

// ErrNoSearch: the recorder's web interface has no search (HTTP 404).
var ErrNoSearch = errors.New("the recorder's web interface offers no search of recordings")

// ErrRefused: the recorder answered the search "Device Error" (a day too big to tell).
var ErrRefused = errors.New("the recorder refused the search (Device Error)")

// Record is a recorded span of a channel.
type Record struct {
	Start, End time.Time
	Type       string // Alarm, Timer…
}

// one search of a recorder at a time
var searching = struct {
	sync.Mutex
	m map[string]chan struct{}
}{m: map[string]chan struct{}{}}

func (c *Client) searchTurn(ctx context.Context) (func(), error) {
	searching.Lock()
	ch := searching.m[c.addr]
	if ch == nil {
		ch = make(chan struct{}, 1)
		searching.m[c.addr] = ch
	}
	searching.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// searchRetry: how long to wait before the tries after a refusal
var searchRetry = []time.Duration{time.Second}

// post sends an ISAPI request of the search and decodes its answer into v.
func (c *Client) post(ctx context.Context, path, body string, v any) error {
	release, err := c.searchTurn(ctx)
	if err != nil {
		return err
	}
	defer release()
	for try := 0; ; try++ {
		raw, code, err := c.postOnce(ctx, path, body)
		if err != nil {
			return err
		}
		if code == http.StatusOK {
			if err := xml.Unmarshal(raw, v); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			return nil
		}
		var st struct {
			StatusCode   int    `xml:"statusCode"`
			StatusString string `xml:"statusString"`
		}
		xml.Unmarshal(raw, &st)
		switch {
		case code == http.StatusNotFound:
			return ErrNoSearch
		case code == http.StatusBadRequest && st.StatusCode == 3 && try < len(searchRetry): // busy?
			select {
			case <-time.After(searchRetry[try]):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		case code == http.StatusBadRequest && st.StatusCode == 3:
			return fmt.Errorf("%s: %w", path, ErrRefused)
		case st.StatusString != "":
			return fmt.Errorf("%s: HTTP %d: %s", path, code, st.StatusString)
		}
		return fmt.Errorf("%s: HTTP %d", path, code)
	}
}

func (c *Client) postOnce(ctx context.Context, path, body string) ([]byte, int, error) {
	resp, err := c.webDo(ctx, http.MethodPost, path, "application/xml; charset=utf-8", strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return raw, resp.StatusCode, err
}

// Days: the days of a month (1…31) on which a channel (from 1) recorded.
func (c *Client) Days(ctx context.Context, channel int, month time.Time) ([]int, error) {
	var res struct {
		Days []struct {
			Day    int    `xml:"dayOfMonth"`
			Record string `xml:"record"`
		} `xml:"dayList>day"`
	}
	body := fmt.Sprintf("<trackDailyParam>\n    <year>%d</year>\n    <monthOfYear>%d</monthOfYear>\n</trackDailyParam>\n", month.Year(), int(month.Month()))
	if err := c.post(ctx, fmt.Sprintf("/ISAPI/ContentMgmt/record/tracks/%d/type/1/dailyDistribution", channel), body, &res); err != nil {
		return nil, err
	}
	var out []int
	for _, d := range res.Days {
		if strings.TrimSpace(d.Record) == "true" && d.Day >= 1 && d.Day <= 31 {
			out = append(out, d.Day)
		}
	}
	sort.Ints(out)
	return out, nil
}

// searchPage: as many as the recorder's client asks for (the recorder gives all anyway)
const searchPage = 300

// Search: what a channel (from 1) recorded on a day, in order, as the recorder tells it
// (cut at midnight, a span of a type after the other).
func (c *Client) Search(ctx context.Context, channel int, day time.Time) ([]Record, error) {
	return c.SearchType(ctx, channel, day, "ALL")
}

// SearchType: Search of one type of recording (recType: ALL, Timer, Alarm…).
func (c *Client) SearchType(ctx context.Context, channel int, day time.Time, recType string) ([]Record, error) {
	var out []Record
	for pos := 1; ; {
		var res struct {
			Num  int `xml:"numOfMatches"`
			List []struct {
				Type  string `xml:"type"`
				Start string `xml:"timeSpan>startTime"`
				End   string `xml:"timeSpan>endTime"`
			} `xml:"matchList>matchElement"`
		}
		body := fmt.Sprintf("<CDailySearchDescription>\n\t<day>%s</day>\n\t<contentTypeList> \n\t\t<contentType>video</contentType>\n\t</contentTypeList> \n"+
			"\t<RecTypeList> \n\t\t<recType>%s</recType>\n\t</RecTypeList>\n\t<chanNo>%d</chanNo>\n\t<streamType>main</streamType>\n"+
			"\t<maxResults>%d</maxResults>\n\t<searchResultPostion>%d</searchResultPostion>\n</CDailySearchDescription>",
			day.Format("2006-01-02"), recType, channel-1, searchPage, pos)
		if err := c.post(ctx, "/ISAPI/ContentMgmt/dailySearch", body, &res); err != nil {
			return nil, err
		}
		for _, m := range res.List {
			a, e1 := wallTime(m.Start)
			b, e2 := wallTime(m.End)
			if e1 != nil || e2 != nil || b.Before(a) {
				continue
			}
			out = append(out, Record{Start: a, End: b, Type: strings.TrimSpace(m.Type)})
		}
		pos += len(res.List)
		// all of them came (the recorder ignores maxResults), or nothing more: done
		if len(res.List) == 0 || pos > res.Num || pos > 100*searchPage {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// wallTime reads a time of the recorder: its wall clock, whatever zone it claims.
func wallTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if len(v) > 19 {
		v = v[:19] // "Z" or an offset: not to be believed
	}
	return time.ParseInLocation("2006-01-02T15:04:05", v, time.Local)
}
