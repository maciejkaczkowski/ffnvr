//go:build !linux

package shell

import (
	"errors"
	"os"
)

func makeRaw(*os.File) (func(), error) {
	return nil, errors.New("raw terminal mode is only supported on Linux")
}

func termWidth(*os.File) int { return 80 }
