// Package modules lists the feature modules of ffnvr.
package modules

import (
	"fmt"

	"ffnvr/internal/module"
)

// All returns the modules in load order: commands of a script are applied menu by menu
// in this order (and in registration order of the menus inside one module), so a menu
// must come after the menus it refers to (/nvr storage=... after /storage).
func All() []module.Module {
	// users come before the recorders whose valid-users name them
	return []module.Module{storageModule{}, userModule{}, &nvrModule{}, cameraModule{}, &downloadModule{}, serviceModule{}, systemModule{}, &toolsModule{}, liveModule{}, mapModule{}, &eventModule{}}
}

// checkName accepts names that are safe in file paths and systemd unit names:
// letters, digits, "_", "-" and ".", not starting with "-" or ".".
func checkName(field, v string) error {
	if v == "" {
		return nil
	}
	for i, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' ||
			(i > 0 && (r == '-' || r == '.'))
		if !ok {
			return fmt.Errorf("%s %q: only letters, digits, _ - . are allowed (not - or . first)", field, v)
		}
	}
	return nil
}
