package tui

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
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
