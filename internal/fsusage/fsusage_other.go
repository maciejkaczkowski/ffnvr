//go:build !linux

package fsusage

import "errors"

// Of is supported on Linux only.
func Of(string) (Usage, error) { return Usage{}, errors.New("not supported on this system") }
