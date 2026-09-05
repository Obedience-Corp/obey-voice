package brain

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// NewOllamaBatchLight builds an Ollama batch adapter from config alone: no
// persona prompt, no skills catalog, no embedding call, no conversation
// state. A transform that runs inside the STT stage must not pay for a
// conversation it will never have. It keeps the two checks that turn a
// misconfiguration into a clear message: the server answers, and the model
// is pulled.
func NewOllamaBatchLight(cfg *config.Config) (BatchProvider, error) {
	model := strings.TrimSpace(cfg.OllamaModel)
	if model == "" {
		return nil, fmt.Errorf("ollama_model not configured — run: obey-voice config ollama_model <model>")
	}
	base, err := url.Parse(cfg.OllamaHost)
	if err != nil {
		return nil, fmt.Errorf("invalid ollama_host %q: %w", cfg.OllamaHost, err)
	}
	probe := api.NewClient(base, &http.Client{Timeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := probe.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to ollama at %s: %w", cfg.OllamaHost, err)
	}
	if !ollamaListsModel(models, model) {
		return nil, fmt.Errorf("model %q not found in ollama — run: ollama pull %s", model, model)
	}

	var keepAlive *api.Duration
	if raw := strings.TrimSpace(cfg.OllamaKeepAlive); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			keepAlive = &api.Duration{Duration: d}
		} else if raw == "-1" {
			keepAlive = &api.Duration{Duration: -1}
		}
	}
	// A correction is not a creative task: temperature 0 makes two runs over
	// the same line agree, and leaves the model less room to invent. It is a
	// sampling option, so it never makes Ollama reload the runner.
	options := map[string]any{"temperature": 0}
	if cfg.OllamaNumCtx > 0 {
		options["num_ctx"] = cfg.OllamaNumCtx
	}
	if cfg.OllamaNumBatch > 0 {
		options["num_batch"] = cfg.OllamaNumBatch
	}
	return &ollamaBatch{
		client:    api.NewClient(base, http.DefaultClient),
		model:     model,
		options:   options,
		keepAlive: keepAlive,
		think:     resolveOllamaThink(cfg.OllamaThink, model),
	}, nil
}

// Warmup implements Warmer: it asks Ollama to load the model without
// generating. A chat request with no messages returns as soon as the model is
// resident. Best effort: a failure surfaces on the first real call instead.
func (o *ollamaBatch) Warmup(ctx context.Context) {
	stream := false
	req := &api.ChatRequest{Model: o.model, Stream: &stream, Options: o.options, KeepAlive: o.keepAlive}
	_ = o.client.Chat(ctx, req, func(api.ChatResponse) error { return nil })
}

func ollamaListsModel(list *api.ListResponse, want string) bool {
	if list == nil {
		return false
	}
	for _, m := range list.Models {
		if m.Name == want || strings.TrimSuffix(m.Name, ":latest") == want {
			return true
		}
	}
	return false
}
