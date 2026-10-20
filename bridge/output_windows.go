//go:build windows

package main

import (
	"errors"
	"io"
)

// Rebinding os.Stdout cannot isolate native CRT/cgo writes on Windows.
// Reject bridge startup before opening capture until those handles can be
// redirected together. Ordinary CLI commands are unaffected.
func protocolOutput() (io.Writer, error) {
	return nil, errors.New("desktop bridge is not supported on Windows: native stdout isolation is unavailable")
}
