package main

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"strings"
	"testing"
)

func TestLatencyWorkloadRejectsPaidWorkerBeforeAgency(t *testing.T) {
	// A bare Agency cannot dispatch. Reaching it would panic instead of passing
	// this guard, which specifically covers local live + paid worker settings.
	for _, provider := range []string{"claude", "grok", "stub"} {
		t.Run(provider, func(t *testing.T) {
			r := &liveRunner{cfg: &config.Config{BrainProvider: "ollama", VoiceWorkerProvider: provider}, agent: &voiceagent.Agent{Pipeline: &pipeline.Pipeline{Agency: &pipeline.Agency{}}}}
			if _, err := r.startLatencyWorker(t.Context(), "run workload"); err == nil || !strings.Contains(err.Error(), "local Ollama") {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}
