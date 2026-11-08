package dahua

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
)

func TestUpgrade(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	fake.UpgradeStep = 20 * time.Millisecond
	old := keepAliveEvery
	keepAliveEvery = 30 * time.Millisecond
	defer func() { keepAliveEvery = old }()

	c, _ := NewClient(fake.Addr(), dahuatest.User, dahuatest.Pass, false)
	fw := bytes.Repeat([]byte("SS\x01\x01firmware"), 5000)
	var mu sync.Mutex
	var states []string
	var pcts []int
	err := c.Upgrade(context.Background(), bytes.NewReader(fw), `C:\fw\DH_NVR"2X.bin`, int64(len(fw)), func(s UpgradeState) {
		mu.Lock()
		defer mu.Unlock()
		if len(states) == 0 || states[len(states)-1] != s.State {
			states = append(states, s.State)
		}
		pcts = append(pcts, s.Percent())
	})
	if err != nil {
		t.Fatal(err)
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if !bytes.Equal(fake.Upgraded, fw) || fake.UpgradeName != "DH_NVR2X.bin" {
		t.Errorf("received %d bytes as %q", len(fake.Upgraded), fake.UpgradeName)
	}
	got := strings.Join(states, ",")
	if got != ",Preparing,Downloading,Upgrading,Succeeded" {
		t.Errorf("states %s", got)
	}
	for i := 1; i < len(pcts); i++ {
		if pcts[i] < pcts[i-1] && pcts[i] != 0 {
			t.Errorf("progress goes back: %v", pcts)
			break
		}
	}
	if pcts[len(pcts)-1] != 100 {
		t.Errorf("progress ends at %d", pcts[len(pcts)-1])
	}
	if fake.KeepAlives == 0 {
		t.Error("the session was not kept alive")
	}
	if strings.Contains(strings.Join(fake.Requests, " "), "global.logout") {
		t.Error("no logout after a success: the recorder restarts")
	}
}

func TestUpgradeRefused(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	fake.UpgradeFail = "FeatureAPIUnmatch"
	c, _ := NewClient(fake.Addr(), dahuatest.User, dahuatest.Pass, false)
	err := c.Upgrade(context.Background(), strings.NewReader("x"), "a.bin", 1, nil)
	if !errors.Is(err, ErrUpgrade) || !strings.Contains(err.Error(), "not meant for this model") {
		t.Errorf("%v", err)
	}
	// without notifications: the answer alone decides
	fake.NoNotify = true
	err = c.Upgrade(context.Background(), strings.NewReader("x"), "a.bin", 1, nil)
	if !errors.Is(err, ErrUpgrade) || !strings.Contains(err.Error(), "upgrade failed") {
		t.Errorf("%v", err)
	}
	fake.UpgradeFail = ""
	if err := c.Upgrade(context.Background(), strings.NewReader("x"), "a.bin", 1, nil); err != nil {
		t.Errorf("without notifications: %v", err)
	}
	// a wrong password
	bad, _ := NewClient(fake.Addr(), dahuatest.User, "wrong", false)
	if err := bad.Upgrade(context.Background(), strings.NewReader("x"), "a.bin", 1, nil); err == nil || !strings.Contains(err.Error(), "wrong username or password") {
		t.Errorf("%v", err)
	}
	if err := c.Upgrade(context.Background(), strings.NewReader(""), "a.bin", 0, nil); err == nil {
		t.Error("an empty file")
	}
}

func TestUpgradePercent(t *testing.T) {
	for _, c := range []struct {
		s    UpgradeState
		want int
	}{
		{UpgradeState{Sent: 50, Total: 100}, 25},
		{UpgradeState{State: "Preparing", Sent: 100, Total: 100}, 50},
		{UpgradeState{State: "Downloading", Progress: 40}, 20},
		{UpgradeState{State: "Upgrading", Progress: 40}, 70},
		{UpgradeState{State: "Upgrading", Progress: 140}, 100},
		{UpgradeState{State: "Succeeded"}, 100},
	} {
		if got := c.s.Percent(); got != c.want {
			t.Errorf("%+v: %d, want %d", c.s, got, c.want)
		}
	}
}
