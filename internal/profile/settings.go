package profile

// Settings: the user's own preferences of the panel (settings.json).
//
//	{"version": 1, "start": "live", "zoom_hq": false}
//
// start: the module the panel opens with after signing in and at / - "live",
// "archive" or "system"; "" (or one the user may no longer use): the panel's choice.
// zoom_hq: the live view switches a tile zoomed over the grid to HQ (when the camera
// has it); false: the zoomed tile keeps the quality it had. Not there: true.
type Settings struct {
	Version int    `json:"version"`
	Start   string `json:"start,omitempty"`
	ZoomHQ  *bool  `json:"zoom_hq,omitempty"`
}

// ZoomToHQ: whether a zoomed live tile switches to HQ (the default: yes).
func (st Settings) ZoomToHQ() bool { return st.ZoomHQ == nil || *st.ZoomHQ }

// SettingsFile is the name of the file in a profile.
const SettingsFile = "settings"

// SettingsVersion is the shape written now.
const SettingsVersion = 1

// StartPages: the modules a user may open the panel with, in the order shown.
var StartPages = []string{"live", "archive", "system"}

// Settings of a user; none yet: the zero value.
func (s *Store) Settings(user string) (Settings, error) {
	var st Settings
	_, err := s.Read(user, SettingsFile, &st)
	return st, err
}

// SetStart keeps the user's start module ("": the panel's choice).
func (s *Store) SetStart(user, start string) error {
	var st Settings
	return s.Update(user, SettingsFile, &st, func() error {
		st.Version, st.Start = SettingsVersion, start
		return nil
	})
}

// SetZoomHQ keeps whether a zoomed live tile switches to HQ.
func (s *Store) SetZoomHQ(user string, on bool) error {
	var st Settings
	return s.Update(user, SettingsFile, &st, func() error {
		st.Version, st.ZoomHQ = SettingsVersion, &on
		return nil
	})
}
