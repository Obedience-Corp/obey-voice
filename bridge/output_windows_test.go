//go:build windows

package main

import "testing"

func TestProtocolOutputFailsClosed(t *testing.T) {
	writer, err := protocolOutput()
	if err == nil || writer != nil {
		t.Fatal("Windows bridge must reject startup before capture")
	}
}
