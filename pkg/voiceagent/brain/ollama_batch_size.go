package brain

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// slidingWindowBatch is the prompt batch sent for a model with sliding-window
// attention when ollama_num_batch was left at its default.
//
// llama.cpp cannot resume such a model from an arbitrary prefix: it keeps only
// the attention window plus about two batches of positions, so a prompt that
// differs from the previous request more than two batches before its end is
// re-read from token zero. Every voice turn rewrites more than that — the
// per-turn reminders on the newest user message, the route instruction, the
// reply itself. Measured on gemma-4 12b (window 1024) with a 3k-token prompt:
// at batch 128 a 330-token changed tail re-read all 3243 tokens; at batch 512
// tails of 330, 600 and 1000 tokens re-read 339, 700 and 1232.
const slidingWindowBatch = 512

// slidingWindowProbeTimeout bounds the one model lookup per model and host.
const slidingWindowProbeTimeout = 2 * time.Second

// slidingWindowModels caches the lookup for the process: the answer is a
// property of the model file, and a second answer mid-session would change the
// runner options and make Ollama reload the model.
var slidingWindowModels sync.Map

// promptBatch is the num_batch every request for model must carry. Requests
// that disagree on it make Ollama reload the runner, so each Ollama request
// path asks here instead of reading the config value.
func promptBatch(client *api.Client, model string, cfg *config.Config) int {
	if cfg == nil || cfg.OllamaNumBatch <= 0 {
		return 0
	}
	batch := cfg.OllamaNumBatch
	if cfg.OllamaNumBatchExplicit || batch >= slidingWindowBatch || client == nil {
		return batch
	}
	if !modelHasSlidingWindow(client, cfg.OllamaHost, model) {
		return batch
	}
	if cfg.OllamaNumCtx > 0 && cfg.OllamaNumCtx < slidingWindowBatch {
		return max(batch, cfg.OllamaNumCtx)
	}
	return slidingWindowBatch
}

func modelHasSlidingWindow(client *api.Client, host, model string) bool {
	key := host + "\x00" + model
	if known, ok := slidingWindowModels.Load(key); ok {
		return known.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), slidingWindowProbeTimeout)
	defer cancel()
	sliding := false
	if shown, err := client.Show(ctx, &api.ShowRequest{Model: model}); err == nil {
		sliding = modelInfoHasSlidingWindow(shown.ModelInfo)
	}
	known, _ := slidingWindowModels.LoadOrStore(key, sliding)
	return known.(bool)
}

// modelInfoHasSlidingWindow reads the GGUF metadata Ollama reports, where the
// key is <architecture>.attention.sliding_window and the value is the window.
func modelInfoHasSlidingWindow(info map[string]any) bool {
	for key, value := range info {
		if !strings.HasSuffix(key, ".attention.sliding_window") {
			continue
		}
		if window, ok := value.(float64); ok && window > 0 {
			return true
		}
	}
	return false
}
