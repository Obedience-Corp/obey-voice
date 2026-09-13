package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
)

// LegacyWorkerValidator is an optional admission check for feature-enabled
// legacy workers. Injected test providers do not implement it and remain
// usable; concrete Ollama workers verify their endpoint before inference.
type LegacyWorkerValidator interface {
	ValidateLegacyWorker(context.Context) error
}

// ValidateLegacyWorker verifies that this Ollama model is served locally.
// Ollama cloud aliases can appear in an otherwise ordinary model catalog, so
// model metadata is authoritative rather than the host URL alone.
func (o *OllamaBrain) ValidateLegacyWorker(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("legacy Ollama validation: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(o.model))
	if strings.HasSuffix(name, ":cloud") || strings.Contains(name, ":cloud-") {
		return fmt.Errorf("ollama model %q is a remote or cloud alias", o.model)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	shown, err := o.client.Show(probeCtx, &api.ShowRequest{Model: o.model})
	if err != nil {
		return fmt.Errorf("cannot inspect legacy Ollama model %q: %w", o.model, err)
	}
	if shown == nil {
		return fmt.Errorf("cannot inspect legacy Ollama model %q: empty metadata", o.model)
	}
	if strings.TrimSpace(shown.RemoteHost) != "" || strings.TrimSpace(shown.RemoteModel) != "" {
		return fmt.Errorf("ollama model %q is a remote or cloud alias", o.model)
	}
	if strings.TrimSpace(shown.Details.Family) == "" && len(shown.ModelInfo) == 0 && len(shown.Tensors) == 0 {
		return fmt.Errorf("ollama model %q returned empty local metadata", o.model)
	}
	return nil
}
