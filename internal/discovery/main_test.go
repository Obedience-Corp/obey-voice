package discovery

import (
	"os"
	"testing"
	"time"
)

// A loaded machine can take longer than the production cap just to start the
// stand-in CLI, so tests that expect an answer must not depend on that cap.
// The raise is package-wide and set before any test runs; no test here may
// call t.Parallel or change probeTimeout.
func TestMain(m *testing.M) {
	probeTimeout = time.Minute
	os.Exit(m.Run())
}

func TestProbeTimeoutDefaultIsThreeSeconds(t *testing.T) {
	if defaultProbeTimeout != 3*time.Second {
		t.Fatalf("production probe cap = %s, want 3s", defaultProbeTimeout)
	}
}
