package brain

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

func TestPinnedContextIdentityDoesNotApplyToChangedRuntimeOrModel(t *testing.T) {
	good := NativeContextIdentity{"0.40.12", "19ff9eb6e621de5643e27d7a3d64e3fe62f9cd22d1471c9a34c01fb69b8e4f74"}
	if !good.pinnedLFM3() {
		t.Fatal("pinned identity refused")
	}
	for _, changed := range []NativeContextIdentity{{}, {"0.40.13", good.Digest}, {good.RuntimeVersion, strings.Repeat("0", 64)}} {
		if changed.pinnedLFM3() {
			t.Fatal("unknown encoder inherited bound", changed)
		}
	}
}

func TestPinnedAudioContextGeometryMatchesObservedRuntimeBoundaries(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	base, err := pinnedLFM3ContextBound([]api.Message{{Role: "user"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ samples, tokens int }{{1, 1}, {400, 1}, {401, 1}, {1520, 1}, {1521, 2}, {16000, 13}, {480000, 375}} {
		ref, err := media.ImportSamples(t.Context(), make([]float32, tc.samples))
		if err != nil {
			t.Fatal(err)
		}
		data, err := media.ResolveAudio(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pinnedLFM3ContextBound([]api.Message{{Role: "user", Images: []api.ImageData{data}}}, nil)
		if err != nil || got-base != int64(tc.tokens+3) {
			t.Fatalf("samples%d: bound%d base%d error%v", tc.samples, got, base, err)
		}
		media.Release(ref)
	}
}

func TestPinnedToolMarkupPressureRejectsBeforeHTTPWithoutHistoryLoss(t *testing.T) {
	b := repetitionBrain(t, func(http.ResponseWriter, *http.Request) { t.Fatal("over-budget request reached HTTP") })
	media := chatmedia.NewAudioSession()
	defer media.Close()
	b.SetAudioResolver(media)
	b.nativeContextIdentity = NativeContextIdentity{"0.40.12", "19ff9eb6e621de5643e27d7a3d64e3fe62f9cd22d1471c9a34c01fb69b8e4f74"}
	b.cfg.OllamaNumCtx = 4096
	args := api.NewToolCallFunctionArguments()
	for i := 0; i < 200; i++ {
		args.Set(fmt.Sprint(i), "")
	}
	messages := []api.Message{{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "example", Arguments: args}}}}}
	if err := b.messagesBudgetError(messages, nil); !errors.Is(err, ErrPromptBudget) {
		t.Fatal("parameter wrappers omitted", err)
	}
	prior := []Turn{{Role: "user", Content: strings.Repeat("retained evidence ", 400)}}
	b.LoadHistory(prior)
	if _, err := b.ThinkFull(t.Context(), "Keep all prior context.", StreamOptions{}); !errors.Is(err, ErrPromptBudget) {
		t.Fatal(err)
	}
	if got := b.History(); len(got) < 1 || got[0].Content != prior[0].Content {
		t.Fatal("context was trimmed", got)
	}
	b.cfg.OllamaNumCtx = 128000
	if b.effectiveNumCtx() != nativeContextCeiling {
		t.Fatal("profile expanded32K cap")
	}
}
