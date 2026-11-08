package dvrip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// cmdPTZ: OPPTZControl, as XMeye's web client sends it (captured from an NBD6808T-PL):
//
//	{ "Name" : "OPPTZControl", "OPPTZControl" : { "Command" : "DirectionUp",
//	  "Parameter" : { "AUX" : { "Number" : 0, "Status" : "On" }, "Channel" : 1,
//	  "MenuOpts" : "Enter", "POINT" : { "bottom" : 0, "left" : 0, "right" : 0, "top" : 0 },
//	  "Pattern" : "SetBegin", "Preset" : 65535, "Step" : 5, "Tour" : 0 } }, "SessionID" : … }
//
// Preset 65535 starts the movement, -1 stops it; Step is the speed (1-8), Channel counts
// from 0. Commands: DirectionUp, DirectionDown, DirectionLeft, DirectionRight,
// DirectionLeftUp, DirectionRightUp, DirectionLeftDown, DirectionRightDown, ZoomTile
// (sic: zoom in), ZoomWide, FocusFar, FocusNear, IrisLarge, IrisSmall.
const cmdPTZ = 1400

// PTZ starts (start) or stops a command on a channel (from 0); step 1-8.
func (s *Session) PTZ(channel int, command string, start bool, step int) error {
	preset := 65535
	if !start {
		preset = -1
	}
	_, _, err := s.c.call(cmdPTZ, map[string]any{
		"Name": "OPPTZControl", "SessionID": s.c.sessionHex(),
		"OPPTZControl": map[string]any{
			"Command": command,
			"Parameter": map[string]any{
				"AUX": map[string]any{"Number": 0, "Status": "On"}, "Channel": channel, "MenuOpts": "Enter",
				"POINT":   map[string]any{"bottom": 0, "left": 0, "right": 0, "top": 0},
				"Pattern": "SetBegin", "Preset": preset, "Step": step, "Tour": 0,
			},
		},
	}, 10*time.Second)
	return err
}

// KeepAlive keeps the session open until ctx ends (the recorder drops a silent one
// after about 20 seconds).
func (s *Session) KeepAlive(ctx context.Context) { s.c.keepAlive(ctx) }

// cmdConfigSet writes a named configuration (the answer of cmdConfigGet, changed).
const cmdConfigSet = 1040

// ErrNoWhiteLight: the camera tells no white light (WhiteLight in Camera.ParamEx).
var ErrNoWhiteLight = errors.New("the camera has no white light (no WhiteLight in Camera.ParamEx)")

// WhiteLight switches the white light of a channel's camera (from 0), the way the
// iCSee application does it (github.com/voidnullvalue/Icsee-android, CameraLighting):
// it reads Camera.ParamEx, sets WhiteLight.WorkMode to "Open" or "Close" and writes
// the configuration back unchanged otherwise. Through a recorder the channel's own
// part is Camera.ParamEx.[n]; firmware without it gets the list of all channels.
func (s *Session) WhiteLight(channel int, on bool) error {
	mode := "Close"
	if on {
		mode = "Open"
	}
	name := fmt.Sprintf("Camera.ParamEx.[%d]", channel)
	value, err := s.config(name)
	idx := 0
	if err != nil { // the whole list, the channel's entry in it
		name, idx = "Camera.ParamEx", channel
		if value, err = s.config(name); err != nil {
			return err
		}
	}
	ch := value
	if list, ok := value.([]any); ok {
		if idx >= len(list) {
			return fmt.Errorf("%s: no channel %d (%d entries)", name, channel, len(list))
		}
		ch = list[idx]
	}
	m, _ := ch.(map[string]any)
	white, ok := m["WhiteLight"].(map[string]any)
	if !ok {
		return ErrNoWhiteLight
	}
	white["WorkMode"] = mode
	_, _, err = s.c.call(cmdConfigSet, map[string]any{"Name": name, name: value, "SessionID": s.c.sessionHex()}, 15*time.Second)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// config reads a named configuration keeping its numbers as they are (it is written
// back).
func (s *Session) config(name string) (any, error) {
	_, raw, err := s.c.get(cmdConfigGet, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	v, ok := m[name]
	if !ok || v == nil {
		return nil, fmt.Errorf("%s: not in the answer", name)
	}
	return v, nil
}
