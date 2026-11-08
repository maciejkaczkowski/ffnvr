package dvrip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	cmdMachine      = 1450 // OPMachine: Reboot
	cmdConfigExport = 1542 // the configuration as a zip
)

// Reboot restarts the recorder (OPMachine Action=Reboot).
func (c *Client) Reboot(ctx context.Context) error {
	cn, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return err
	}
	defer cn.Close()
	_, _, err = cn.call(cmdMachine, map[string]any{"Name": "OPMachine", "OPMachine": map[string]any{"Action": "Reboot"},
		"SessionID": cn.sessionHex()}, 15*time.Second)
	return err
}

// Backup exports the configuration: message 1542, answered with a zip (in one or more
// messages 1543).
func (c *Client) Backup(ctx context.Context) (io.ReadCloser, error) {
	cn, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return nil, err
	}
	defer cn.Close()
	if err := cn.send(cmdConfigExport, map[string]any{"Name": "", "SessionID": cn.sessionHex()}); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	deadline := time.Now().Add(30 * time.Second)
	for {
		wait := 15 * time.Second
		if buf.Len() > 0 {
			wait = 2 * time.Second // more parts follow quickly, if any
		}
		cn.c.SetReadDeadline(time.Now().Add(wait))
		id, payload, err := cn.recv()
		var ne net.Error
		if err != nil {
			if errors.As(err, &ne) && ne.Timeout() && buf.Len() > 0 {
				break
			}
			return nil, fmt.Errorf("configuration export: %w", err)
		}
		if id != cmdConfigExport+1 {
			continue
		}
		if buf.Len() == 0 && len(payload) > 0 && payload[0] == '{' {
			return nil, fmt.Errorf("configuration export refused: %s", trimJSON(payload))
		}
		buf.Write(payload)
		if time.Now().After(deadline) {
			break
		}
		if bytes.HasPrefix(buf.Bytes(), []byte("PK")) && bytes.Contains(buf.Bytes()[max(0, buf.Len()-1024):], []byte("PK\x05\x06")) {
			break // the end of the zip
		}
	}
	return io.NopCloser(&buf), nil
}
