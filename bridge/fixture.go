//go:build desktopfixture

package main

// This build tag is used only by the native-window test harness. Fail closed
// unless given an absolute, existing temporary install; the regular companion
// never includes this file or changes the config root.
import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"os"
	"path/filepath"
)

type fixtureScope struct{}

func (fixtureScope) Helper()        {}
func (fixtureScope) Cleanup(func()) {}
func init() {
	dir := os.Getenv("OBEY_VOICE_TEST_CONFIG")
	if !filepath.IsAbs(dir) {
		panic("desktop fixture requires an absolute OBEY_VOICE_TEST_CONFIG")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		panic(err)
	}
	config.SetConfigDirForTest(fixtureScope{}, dir)
}
