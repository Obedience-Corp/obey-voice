package tui

import (
	"os"
	"testing"
)

const realClipboardMsg = "test reached the real system clipboard; inject a clipboardBackend fake"

func TestMain(m *testing.M) {
	systemClipboardRead = func() (string, error) { panic(realClipboardMsg) }
	systemClipboardWrite = func(string) error { panic(realClipboardMsg) }
	os.Exit(m.Run())
}
