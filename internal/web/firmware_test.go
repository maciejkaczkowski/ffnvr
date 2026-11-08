package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
)

// postFirmware sends the update form as the browser does: csrf, nvr, then the file.
func postFirmware(t *testing.T, c *http.Client, base, csrf, nvr, file string, data []byte) (int, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	mw.WriteField("csrf", csrf)
	mw.WriteField("nvr", nvr)
	fw, _ := mw.CreateFormFile("file", file)
	fw.Write(data)
	mw.Close()
	resp, err := c.Post(base+"/tools/firmware", mw.FormDataContentType(), &b)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestFirmwareUpgrade(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	srv, st := setup(t)
	st.exec(t, `/nvr set nvr02 host="`+fake.Addr()+`" password=secret`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// reached from the list and from the recorder's page (Dahua only)
	_, list := get(t, c, srv.URL+"/?p=firmware")
	contains(t, "firmware list", list, `<th style="text-align:right">Action</th>`, `href="/?p=fwup&amp;n=nvr02">Update</a>`)
	notContains(t, "firmware list", list, `n=nvr01">Update`)
	_, nvr := get(t, c, srv.URL+"/?p=nvr&n=nvr02")
	contains(t, "recorder page", nvr, `href="/?p=fwup&amp;n=nvr02">Update firmware</a>`)
	_, local := get(t, c, srv.URL+"/?p=nvr&n=nvr01")
	notContains(t, "local recorder", local, "?p=fwup")

	_, page := get(t, c, srv.URL+"/?p=fwup&n=nvr02")
	contains(t, "update page", page, "<h1>Firmware update: nvr02</h1>", `action="/tools/firmware" enctype="multipart/form-data"`,
		`data-confirm="Update the firmware of nvr02?"`, `accept=".bin"`, "4.005.0000000.5.R", "DHI-NVR2108-S3",
		`<a href="/?p=firmware" aria-current="page">`, `<dialog id="fwconfirm"`, "XMLHttpRequest")
	// the form's order: the file last
	if i, j := strings.Index(page, `name="nvr" value="nvr02"`), strings.Index(page, `type="file"`); i < 0 || j < i {
		t.Error("csrf and nvr must come before the file")
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]
	_, page = get(t, c, srv.URL+"/?p=fwup&n=nvr01")
	contains(t, "local recorder", page, "Dahua recorders only")
	notContains(t, "local recorder", page, `action="/tools/firmware"`)
	if _, page := get(t, c, srv.URL+"/?p=fwup&n=nope"); !strings.Contains(page, "<h1>Firmware</h1>") {
		t.Error("an unknown recorder leads to the list")
	}

	// refusals
	if code, _ := postFirmware(t, c, srv.URL, "forged", "nvr02", "a.bin", []byte("x")); code != 403 {
		t.Errorf("forged token: %d", code)
	}
	_, page = postFirmware(t, c, srv.URL, csrf, "nvr02", "notes.txt", []byte("x"))
	contains(t, "wrong file", page, "Choose a firmware file (.bin).")
	_, page = postFirmware(t, c, srv.URL, csrf, "nvr02", "empty.bin", nil)
	contains(t, "empty file", page, "Choose a firmware file (.bin).")
	_, page = postFirmware(t, c, srv.URL, csrf, "nvr01", "a.bin", []byte("x"))
	contains(t, "local recorder", page, "Dahua recorders only")
	old := maxFirmware
	maxFirmware = 4
	_, page = postFirmware(t, c, srv.URL, csrf, "nvr02", "big.bin", []byte("12345"))
	maxFirmware = old
	contains(t, "too big", page, "The file is larger than")
	if fake.Upgraded != nil {
		t.Fatal("nothing should have reached the recorder")
	}

	// the update: in the background, one at a time
	fake.Mu.Lock()
	fake.UpgradeStep = 100 * time.Millisecond
	fake.Mu.Unlock()
	data := bytes.Repeat([]byte("DHFW"), 10000)
	code, page := postFirmware(t, c, srv.URL, csrf, "nvr02", `C:\fw\DH_NVR2X.bin`, data)
	if code != 200 {
		t.Fatalf("%d %s", code, page)
	}
	contains(t, "running", page, "<h1>Firmware update: nvr02</h1>", "Update in progress", `id="fwrunning"`, "DH_NVR2X.bin", "by jan")
	notContains(t, "running", page, `id="fwform"`)
	_, again := postFirmware(t, c, srv.URL, csrf, "nvr02", "a.bin", []byte("x"))
	contains(t, "busy", again, "nvr02: an update is running already.")
	for i := 0; i < 100 && !strings.Contains(page, `<span class="badge ok">Updated</span>`); i++ {
		time.Sleep(50 * time.Millisecond)
		_, page = get(t, c, srv.URL+"/?p=fwup&n=nvr02")
	}
	contains(t, "done", page, `<span class="badge ok">Updated</span>`, "Installed, the recorder restarts", "100%", `id="fwform"`)
	fake.Mu.Lock()
	if !bytes.Equal(fake.Upgraded, data) || fake.UpgradeName != "DH_NVR2X.bin" {
		t.Errorf("the recorder got %d bytes as %q", len(fake.Upgraded), fake.UpgradeName)
	}
	fake.UpgradeStep = 0
	fake.UpgradeFail = "FeatureAPIUnmatch"
	fake.Mu.Unlock()
	_, nvr = get(t, c, srv.URL+"/?p=nvr&n=nvr02")
	contains(t, "recorder page after", nvr, `<span class="badge ok">Updated</span>`)

	// a file of another model
	postFirmware(t, c, srv.URL, csrf, "nvr02", "other.bin", []byte("x"))
	for i := 0; i < 100 && !strings.Contains(page, `<span class="badge err">`); i++ {
		time.Sleep(50 * time.Millisecond)
		_, page = get(t, c, srv.URL+"/?p=fwup&n=nvr02")
	}
	contains(t, "failed", page, `<span class="badge err">Failed</span>`, "not meant for this model")

	// the system log
	_, log := get(t, c, srv.URL+"/?p=event_log&nvr=nvr02")
	contains(t, "system log", log, "Firmware update", "firmware update started: DH_NVR2X.bin", "firmware installed: DH_NVR2X.bin", "firmware update failed: other.bin")
}
