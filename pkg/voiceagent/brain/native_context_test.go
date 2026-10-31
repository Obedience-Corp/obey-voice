package brain

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/ollama/ollama/api"
)

func TestNativeContextPressureRefusesBeforeHTTPWithoutTrimmingEvidence(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), make([]float32, 16000))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	b := repetitionBrain(t, func(http.ResponseWriter, *http.Request) { calls++ })
	b.SetAudioResolver(media)
	b.cfg.OllamaNumCtx = 256
	prior := []Turn{{Role: "user", Content: strings.Repeat("prior evidence ", 50)}, {Role: "assistant", Content: "prior answer"}}
	b.LoadHistory(prior)
	input := &utterance.Finalized{ID: "new-audio", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
	if _, err := b.ThinkFull(t.Context(), "", StreamOptions{Utterance: input}); !errors.Is(err, ErrPromptBudget) {
		t.Fatal("missing pressure rejection", err)
	}
	if calls != 0 {
		t.Fatal("oversized native context reached HTTP")
	}
	history := b.History()
	if len(history) < 2 || history[0].Content != prior[0].Content || history[1].Content != prior[1].Content {
		t.Fatal("pressure silently removed prior evidence", history)
	}
	if b.effectiveNumCtx() != 256 {
		t.Fatal("native pressure silently expanded context")
	}
}

func TestNativeContextIncludesAudioGeometryAndClampsRequestedWindow(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), make([]float32, 16000))
	if err != nil {
		t.Fatal(err)
	}
	data, err := media.ResolveAudio(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	b := repetitionBrain(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected HTTP") })
	b.SetAudioResolver(media)
	b.cfg.OllamaNumCtx = 256
	if err := b.messagesBudgetError([]api.Message{{Role: "user", Images: []api.ImageData{data}}}, nil); !errors.Is(err, ErrPromptBudget) {
		t.Fatal("audio omitted from admission", err)
	}
	b.cfg.OllamaNumCtx = 128000
	if b.effectiveNumCtx() != nativeContextCeiling {
		t.Fatal("native requested unsupported window")
	}
	if err := b.messagesBudgetError([]api.Message{{Role: "user", Images: []api.ImageData{data}}}, nil); err != nil {
		t.Fatal(err)
	}
}
