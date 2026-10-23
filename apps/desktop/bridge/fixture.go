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
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		panic("desktop fixture requires an existing config directory")
	}
	config.SetConfigDirForTest(fixtureScope{}, dir)
}
