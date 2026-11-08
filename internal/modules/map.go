package modules

import (
	"fmt"
	"math"
	"strconv"

	"ffnvr/internal/config"
)

// mapModule: where cameras stand on the map of the live view (the users' saved views
// are in their profiles, see package profile):
//
//	/camera/position
//	add nvr=nvr01 camera=cam22 lat=49.4 long=17.0 direction=90 angle=104 range=50
//
// The references to recorders and cameras are names, not strict references: a
// camera that disappears from a recorder leaves its entries behind, ignored, rather
// than blocking the change.
type mapModule struct{}

func (mapModule) Name() string { return "map" }

func float(it *config.Item, key string, min, max float64, required bool) error {
	v := it.Val(key)
	if v == "" {
		if required {
			return fmt.Errorf("%s is required", key)
		}
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f < min || f > max {
		return fmt.Errorf("%s %q: a number from %g to %g", key, v, min, max)
	}
	it.Set(key, strconv.FormatFloat(f, 'f', -1, 64))
	return nil
}

func (mapModule) Menus() []*config.Menu {
	return []*config.Menu{
		{
			Path: "/camera/position", Kind: config.Multi,
			Help: "where cameras stand on the map",
			Fields: []config.Field{
				{Name: "nvr", Ref: "/nvr:name", Required: true},
				{Name: "camera", Required: true},
				{Name: "lat", Required: true},
				{Name: "long", Required: true},
				{Name: "direction", Help: "where it looks, degrees from north (0..359)"},
				{Name: "angle", Help: "field of view in degrees (default 90)"},
				{Name: "range", Help: "how far it sees, in meters (default 30)"},
			},
			Validate: func(s *config.Store, it *config.Item) error {
				if err := checkName("camera", it.Val("camera")); err != nil {
					return err
				}
				for _, f := range []struct {
					k        string
					min, max float64
					req      bool
				}{{"lat", -90, 90, true}, {"long", -180, 180, true}, {"direction", 0, 360, false}, {"angle", 1, 360, false}, {"range", 1, 5000, false}} {
					if err := float(it, f.k, f.min, f.max, f.req); err != nil {
						return err
					}
				}
				for _, o := range s.Items("/camera/position") {
					if o != it && o.Val("nvr") == it.Val("nvr") && o.Val("camera") == it.Val("camera") {
						return fmt.Errorf("camera %s of %s has a position already", it.Val("camera"), it.Val("nvr"))
					}
				}
				return nil
			},
		},
	}
}

// Position of a camera on the map.
type Position struct {
	Nvr, Camera                  string
	Lat, Long                    float64
	Direction, Angle, RangeMeter float64
}

// Positions of cameras on the map.
func Positions(s *config.Store) []Position {
	var out []Position
	for _, it := range s.Items("/camera/position") {
		p := Position{Nvr: it.Val("nvr"), Camera: it.Val("camera"), Angle: 90, RangeMeter: 30}
		p.Lat, _ = strconv.ParseFloat(it.Val("lat"), 64)
		p.Long, _ = strconv.ParseFloat(it.Val("long"), 64)
		p.Direction, _ = strconv.ParseFloat(it.Val("direction"), 64)
		if v, err := strconv.ParseFloat(it.Val("angle"), 64); err == nil {
			p.Angle = v
		}
		if v, err := strconv.ParseFloat(it.Val("range"), 64); err == nil {
			p.RangeMeter = v
		}
		out = append(out, p)
	}
	return out
}
