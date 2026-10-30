//go:build !integration

package cmd

import (
	"os"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// TestDemoTerminalHarness runs the real text-mode launcher under VHS with an
// explicit disposable install. It neither changes HOME nor reads the user's
// persona/config files. Ordinary test runs skip this interactive entry point.
func TestDemoTerminalHarness(t *testing.T) {
	root := os.Getenv("OBEY_VOICE_DEMO_ROOT")
	if root == "" {
		t.Skip("VHS-only disposable demo install")
	}
	config.SetConfigDirForTest(t, root)
	rootCmd.SetArgs([]string{"-t", "-n"})
	if err := rootCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}
