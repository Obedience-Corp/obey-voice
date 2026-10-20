//go:build windows

package main

import (
	"io"
	"os"
)

func protocolOutput() (io.Writer, error) { out := os.Stdout; os.Stdout = os.Stderr; return out, nil }
