package modules

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"ffnvr/internal/config"
)

// storageModule: /storage - places where recordings are kept.
//
//	/storage
//	add name="disk1" path="/srv/disk1"
type storageModule struct{}

func (storageModule) Name() string { return "storage" }

func (storageModule) Menus() []*config.Menu {
	return []*config.Menu{{
		Path:    "/storage",
		Kind:    config.Multi,
		NameKey: "name",
		Help:    "places for recordings",
		Fields: []config.Field{
			{Name: "name", Required: true, Help: "name used by /nvr storage=..."},
			{Name: "path", Required: true, Unique: true, Help: "absolute directory, e.g. /srv/disk1"},
			{Name: "types", Type: config.TList, Enum: StorageTypes, Help: "what else it is for: archive (the Archive page), backup (configuration backups)"},
			{Name: "disabled", Type: config.TBool},
			{Name: "comment"},
		},
		Validate: validateStorage,
	}}
}

func validateStorage(_ *config.Store, it *config.Item) error {
	name, path := it.Val("name"), it.Val("path")
	if err := checkName("name", name); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path %q must be absolute (start with /)", path)
	}
	// the path ends up in ffmpeg arguments (with -strftime, so no %) and in unit files
	for _, r := range path {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_-.", r)) {
			return fmt.Errorf("path %q: only letters, digits and / _ - . are allowed", path)
		}
	}
	path = filepath.Clean(path)
	if path == "/" {
		return fmt.Errorf("path / is not allowed, use a directory")
	}
	it.Set("path", path) // normalised before the store checks that it is unique
	for _, t := range splitList(it.Val("types")) {
		if !slices.Contains(StorageTypes, t) {
			return fmt.Errorf("types: %q is not one of %s", t, strings.Join(StorageTypes, ", "))
		}
	}
	return nil
}

// StorageTypes: what a storage can be used for besides recording.
var StorageTypes = []string{"archive", "backup"}

// StoragesOf lists the enabled storages of a type (archive, backup).
func StoragesOf(s *config.Store, typ string) []string {
	var out []string
	for _, st := range s.Items("/storage") {
		if st.Val("disabled") != "yes" && slices.Contains(splitList(st.Val("types")), typ) {
			out = append(out, st.Val("name"))
		}
	}
	return out
}
