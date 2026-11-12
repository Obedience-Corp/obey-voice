package brain

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
)

// parseOllamaKeepAlive reads ollama_keep_alive. Go durations cover "10m", and
// "-1" (keep loaded forever) is the one Ollama value they cannot express.
func parseOllamaKeepAlive(raw string) (*api.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if raw == "-1" {
		return &api.Duration{Duration: -1}, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return nil, err
	}
	return &api.Duration{Duration: d}, nil
}

// keepAliveWarned is process-global and never expires: a value fixed and then
// set to the same invalid value again in one process is not reported again.
var (
	keepAliveWarnOut io.Writer = os.Stderr
	keepAliveWarned  sync.Map
)

// keepAliveOrDefault parses ollama_keep_alive for every Ollama path. An invalid
// value yields nil (the server default) and is reported once per distinct value.
func keepAliveOrDefault(raw string) *api.Duration {
	d, err := parseOllamaKeepAlive(raw)
	if err == nil {
		return d
	}
	if _, seen := keepAliveWarned.LoadOrStore(raw, struct{}{}); !seen {
		fmt.Fprintf(keepAliveWarnOut, "obey-voice: invalid ollama_keep_alive %q (%v); using the server default\n", raw, err)
	}
	return nil
}
