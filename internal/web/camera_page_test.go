package web

import (
	"strings"
	"testing"
)

// The page of a camera of a local recorder: its latest recordings as the Archive's
// tiles, its latest events, both leading into the Archive.
func TestCameraPage(t *testing.T) {
	srv, st := setup(t)
	seedEvents(t, st.e.Opts.Root)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	_, page := get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	contains(t, "camera page", page,
		"<h2>Latest recordings</h2>", `<div class="thumbs">`, `<label class="thumb">`,
		`/archive/thumb?n=nvr01&amp;c=cam1&amp;f=`, `data-play="/archive/play?n=nvr01&amp;c=cam1&amp;f=`,
		`style="margin-left:auto">Archive</a>`, `href="/?c=cam1&amp;n=nvr01&amp;p=archive&amp;t=`, // the player at the file
		`>Open in the player</a>`, `>Search the archive</a>`, `view=tiles`,
		`<dialog id="player"`, `data-thumb="1"`,
		"<h2>Latest events</h2>", ">VideoLoss<", `▶ Play</a>`, // the player at the event
		`href="/?p=event_log&amp;camera=cam1&amp;nvr=nvr01"`)
	if strings.Contains(page, "Motion CH1") {
		t.Error("the events of other cameras are not shown")
	}

	// another camera: no recordings, no events
	_, page = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam3")
	contains(t, "camera without recordings", page, "No recordings of this camera yet.")
	if strings.Contains(page, ">VideoLoss<") || strings.Contains(page, `<dialog id="player"`) {
		t.Error("cam3 has no events nor files")
	}

	// the event log filtered by the camera keeps the filter
	_, page = get(t, c, srv.URL+"/?p=event_log&nvr=nvr01&camera=cam1")
	contains(t, "event log of a camera", page, `<input type="hidden" name="camera" value="cam1">`, "Camera: cam1", ">VideoLoss<")
	_, page = get(t, c, srv.URL+"/?p=event_log&nvr=nvr01&camera=cam3")
	if strings.Contains(page, `<span class="mono" style="font-size:12px">VideoLoss</span>`) || !strings.Contains(page, "No events") {
		t.Error("camera= filters the events")
	}

	// without archive rights: no recordings; without archive.play: the search instead of the player
	st.exec(t, `/user/policy add name=sys allow="system.*"`)
	st.exec(t, `/user set jan policy=sys`)
	_, page = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	if strings.Contains(page, "Latest recordings") || strings.Contains(page, "▶ Play</a>") {
		t.Error("without archive rights no recordings nor player links")
	}
	contains(t, "events without the archive", page, "<h2>Latest events</h2>", ">VideoLoss<")
	st.exec(t, `/user/policy set sys allow="system.*,archive"`)
	_, page = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	contains(t, "archive without play", page, "<h2>Latest recordings</h2>", `style="margin-left:auto">Archive</a>`, `search=1`)
	if strings.Contains(page, "data-play=") || strings.Contains(page, "Open in the player") || strings.Contains(page, `<dialog id="player"`) {
		t.Error("without archive.play: no player")
	}
}

// The information of a camera: its make, profile and PTZ control.
func TestCameraSettings(t *testing.T) {
	srv, st := setup(t)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	contains(t, "camera without settings", page, "<dt>PTZ control</dt><dd>no</dd>")
	notContains(t, "camera without settings", page, "<dt>Type</dt>", "<dt>Profile</dt>", "left and right swapped")

	st.exec(t, `/camera/profile add name=td type=tiandy username=Admin password=pw ptz=yes stream=rtsp://<host>/1/1`,
		`/camera set [find name=cam1] profile=td host=192.168.10.21 ptz-swap-horizontal=yes`)
	_, page = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	contains(t, "camera of a profile", page, "<dt>Type</dt><dd>tiandy (from the profile td)</dd>", "<dt>Profile</dt><dd>td</dd>",
		"<dt>PTZ control</dt><dd>yes (from the profile td)</dd>",
		"<dt>PTZ: left and right swapped</dt><dd>yes</dd>", "<dt>PTZ: up and down swapped</dt><dd>no</dd>")
	// the camera's own values win
	st.exec(t, `/camera set [find name=cam1] type=dahua ptz=no`)
	_, page = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	contains(t, "camera's own values", page, "<dt>Type</dt><dd>dahua</dd>", "<dt>PTZ control</dt><dd>no</dd>", "<dt>PTZ: left and right swapped</dt><dd>yes</dd>")
	if strings.Contains(page, "Archive →") {
		t.Error("the archive button has no arrow")
	}
}
