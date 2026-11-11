package brain

import (
	"strings"
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
