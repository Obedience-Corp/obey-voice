package discovery

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/model"
)

// Inspect metadata only: no model loading or generation. The catalog's order
// survives concurrent probes, and unknown/failed metadata never grants tools.
func ollamaWorkerModels(ctx context.Context, client *api.Client, names []string) []string {
	eligible := make([]bool, len(names))
	jobs := make(chan int, len(names))
	for i := range names {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(4, len(names)) {
		wg.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				shown, err := client.Show(ctx, &api.ShowRequest{Model: names[i]})
				eligible[i] = err == nil && shown != nil &&
					slices.Contains(shown.Capabilities, model.CapabilityCompletion) &&
					slices.Contains(shown.Capabilities, model.CapabilityTools)
			}
		})
	}
	wg.Wait()
	out := make([]string, 0, len(names))
	for i, name := range names {
		if eligible[i] {
			out = append(out, strings.TrimSuffix(name, ":latest"))
		}
	}
	return out
}
