package brain

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

type disableOutageRetryKey struct{}

// ProviderOutage retains the real provider failure for diagnostics and callers.
type ProviderOutage struct {
	Kind      string
	Err       error
	Delivered bool
}

func (e *ProviderOutage) Error() string { return fmt.Sprintf("Ollama %s: %v", e.Kind, e.Err) }
func (e *ProviderOutage) Unwrap() error { return e.Err }

func ollamaOutage(err error) *ProviderOutage {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	var status api.StatusError
	if errors.As(err, &status) {
		switch status.StatusCode {
		case 502, 503, 504, 429:
			return &ProviderOutage{Kind: "service unavailable", Err: err}
		case 500: // Known worker exits below; arbitrary generation failures are not retried.
		default:
			return nil
		}
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "runner exited unexpectedly") || strings.Contains(message, "runner process has terminated") {
		return &ProviderOutage{Kind: "model worker failed", Err: err}
	}
	var network *net.OpError
	if errors.As(err, &network) {
		return &ProviderOutage{Kind: "connection unavailable", Err: err}
	}
	return nil
}

func waitOutageRetry(ctx context.Context) error {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// A successful metadata request never establishes inference recovery. Only a
// completed generation for this provider/model can publish available.
func (o *OllamaBrain) reportProviderState(ctx context.Context, state string) {
	if ctx.Err() != nil {
		return
	}
	o.healthMu.Lock()
	previous := o.healthState
	if state == "available" && (previous == "" || previous == "available") {
		o.healthMu.Unlock()
		return
	}
	o.healthState = state
	message := "Ollama model unavailable; retrying once."
	if state == "unavailable" {
		message = "Ollama's model is unavailable. Check or restart Ollama on its host, then use /retry or send a new request."
	}
	if state == "available" {
		message = "Ollama model recovered."
	}
	endpoint := ""
	if o.cfg != nil {
		endpoint = strings.TrimRight(o.cfg.OllamaHost, "/")
	}
	status := events.NewProviderStatus("ollama", "brain", o.model, endpoint, state, message)
	o.healthMu.Unlock()
	e := events.Info{Message: message, ProviderStatus: status}
	o.emitContinuity(events.AttributeTrace(e, events.TraceFromContext(ctx)))
}
