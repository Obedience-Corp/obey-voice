package tui

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const realClipboardMsg = "test reached the real system clipboard; inject a clipboardBackend fake"

var (
	realClipboardHit  atomic.Bool
	realClipboardMu   sync.Mutex
	realClipboardWhat []string
)

// recordRealClipboard remembers the attempt before panicking, because a
// bubbletea command goroutine recovers panics and would otherwise hide it.
func recordRealClipboard(call string) {
	realClipboardMu.Lock()
	realClipboardWhat = append(realClipboardWhat, call+"\n"+string(debug.Stack()))
	realClipboardMu.Unlock()
	realClipboardHit.Store(true)
	panic(realClipboardMsg)
}

func TestMain(m *testing.M) {
	systemClipboardRead = func() (string, error) { recordRealClipboard("ReadAll"); return "", nil }
	systemClipboardWrite = func(string) error { recordRealClipboard("WriteAll"); return nil }
	// The real scan starts whatever provider CLIs are installed, the claude CLI
	// among them. A test that needs it opts in with useRealDiscovery.
	discoverProviders = func(*config.Config) []discovery.ProviderInfo { return nil }
	code := m.Run()
	if realClipboardHit.Load() {
		realClipboardMu.Lock()
		fmt.Fprintf(os.Stderr, "FAIL: %s; the stacks below name the offending test, so read them for the *_test.go frame\n", realClipboardMsg)
		for _, c := range realClipboardWhat {
			fmt.Fprintln(os.Stderr, c)
		}
		realClipboardMu.Unlock()
		code = 1
	}
	os.Exit(code)
}

// useRealDiscovery runs the real startup scan for the rest of the test. Put
// stand-in CLIs on PATH first.
func useRealDiscovery(t *testing.T) {
	t.Helper()
	stub := discoverProviders
	discoverProviders = discovery.DiscoverProviders
	t.Cleanup(func() { discoverProviders = stub })
}
