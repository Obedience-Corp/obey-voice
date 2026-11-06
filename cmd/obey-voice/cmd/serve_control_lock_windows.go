//go:build !integration && windows

package cmd

import (
	"errors"
	"os"
)

func lockServeControl(string) (*os.File, error) {
	return nil, errors.New("serve lifecycle control is not supported on Windows")
}
