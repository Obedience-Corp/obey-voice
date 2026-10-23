//go:build !windows

package main

import (
	"golang.org/x/sys/unix"
	"io"
	"os"
)

// Native libraries occasionally write directly to fd 1. Preserve a private
// protocol fd and redirect fd 1 to diagnostics before loading any models.
func protocolOutput() (io.Writer, error) {
	fd, err := unix.Dup(1)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	if err = unix.Dup2(2, 1); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "protocol"), nil
}
