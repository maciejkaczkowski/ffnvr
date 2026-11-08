package dahua

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrNoRPCPTZ: the firmware gives no PTZ instance over JSON-RPC (use PTZCGI).
var ErrNoRPCPTZ = errors.New("no PTZ over JSON-RPC")

// PTZ of the cameras behind the recorder, as the web interface does it (captured from
// an NVR's web interface): a JSON-RPC instance per channel (ptz.factory.instance,
// channel from 0), then ptz.start and ptz.stop with a code - Up, Down, Left, Right,
// LeftUp, RightUp, LeftDown, RightDown, ZoomTele, ZoomWide, FocusNear, FocusFar,
// IrisLarge, IrisSmall - and arg1/arg2 the speeds (1-8):
//
//	{"method":"ptz.start","params":{"code":"Up","arg1":5,"arg2":5,"arg3":0,"arg4":0},
//	 "object":80680944,"session":"…"}
//
// The CGI (ptz.cgi?action=start&channel=1&code=Up&arg1=5&arg2=5&arg3=0, channel from 1)
// does the same for firmware without the RPC.

// PTZ is a JSON-RPC session controlling cameras; Close it.
type PTZ struct {
	rpc     *RPC
	objects map[int]any // by channel (from 1)
}

// LoginPTZ opens a session for PTZ commands.
func (c *Client) LoginPTZ(ctx context.Context) (*PTZ, error) {
	rpc, err := c.LoginRPC(ctx)
	if err != nil {
		return nil, err
	}
	return &PTZ{rpc: rpc, objects: map[int]any{}}, nil
}

// object returns the PTZ instance of a channel (from 1).
func (p *PTZ) object(ctx context.Context, channel int) (any, error) {
	if o, ok := p.objects[channel]; ok {
		return o, nil
	}
	p.rpc.id++
	resp, raw, err := p.rpc.c.post(ctx, "/RPC2", rpcRequest{Method: "ptz.factory.instance",
		Params: map[string]any{"channel": channel - 1}, ID: p.rpc.id, Session: p.rpc.session})
	if err != nil {
		return nil, fmt.Errorf("ptz.factory.instance: %w", err)
	}
	var obj json.Number
	if err := json.Unmarshal(resp.Result, &obj); err != nil || obj == "" || obj == "0" {
		msg := fmt.Sprintf("%.120s", raw)
		if resp.Error != nil {
			msg = fmt.Sprintf("%s (%d)", resp.Error.Message, resp.Error.Code)
		}
		return nil, fmt.Errorf("ptz.factory.instance channel %d: %w: %w: %s", channel, ErrNoRPCPTZ, ErrDevice, msg)
	}
	n, err := obj.Int64()
	if err != nil {
		return nil, fmt.Errorf("ptz.factory.instance: bad object %s", obj)
	}
	p.objects[channel] = n
	return n, nil
}

// Do starts (start) or stops a movement of a channel (from 1); speed 1-8.
func (p *PTZ) Do(ctx context.Context, channel int, code string, start bool, speed int) error {
	obj, err := p.object(ctx, channel)
	if err != nil {
		return err
	}
	method := "ptz.stop"
	if start {
		method = "ptz.start"
	}
	p.rpc.id++
	resp, raw, err := p.rpc.c.post(ctx, "/RPC2", rpcRequest{Method: method,
		Params: map[string]any{"code": code, "arg1": speed, "arg2": speed, "arg3": 0, "arg4": 0},
		ID:     p.rpc.id, Session: p.rpc.session, Object: obj})
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, code, err)
	}
	if string(resp.Result) != "true" {
		msg := fmt.Sprintf("%.120s", raw)
		if resp.Error != nil {
			msg = fmt.Sprintf("%s (%d)", resp.Error.Message, resp.Error.Code)
		}
		return fmt.Errorf("%s %s: %w: %s", method, code, ErrDevice, msg)
	}
	return nil
}

// Close releases the instances and logs out.
func (p *PTZ) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	for _, obj := range p.objects {
		p.rpc.id++
		p.rpc.c.post(ctx, "/RPC2", rpcRequest{Method: "ptz.destroy", ID: p.rpc.id, Session: p.rpc.session, Object: obj})
	}
	p.objects = map[int]any{}
	p.rpc.Close()
}

// PTZCGI is Do over the CGI (Digest authentication, no session).
func (c *Client) PTZCGI(ctx context.Context, channel int, code string, start bool, speed int) error {
	action := "stop"
	if start {
		action = "start"
	}
	_, err := c.cgi(ctx, "ptz.cgi", param{"action", action}, param{"channel", strconv.Itoa(channel)},
		param{"code", code}, param{"arg1", strconv.Itoa(speed)}, param{"arg2", strconv.Itoa(speed)}, param{"arg3", "0"})
	return err
}
