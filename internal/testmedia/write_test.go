package testmedia

import (
	"os"
	"path/filepath"
	"testing"
)

// Opt-in shared asset preparation for the native-window harness.
func TestWriteFixtures(t *testing.T) {
	dir := os.Getenv("OBEY_VOICE_MEDIA_FIXTURE_DIR")
	if dir == "" {
		t.Skip("fixture export not requested")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"Screenshot with spaces.png": PNG(), "animation.gif": GIF()} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
