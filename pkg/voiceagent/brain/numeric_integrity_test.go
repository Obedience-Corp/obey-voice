package brain

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/ollama/ollama/api"
)

func TestExplicitlyStoppedNumericAnswerIsCompleteWithoutWeakeningOtherSignals(t *testing.T) {
	for _, text := range []string{"15", "-12.5", "+4", ".5", "1e3", "35%"} {
		got := evaluateStreamIntegrity(IntegrityInput{DoneReason: "stop", Speakable: text, GenTokens: 2, NumCtx: 8192})
		if got.Truncated || got.MidSentence {
			t.Errorf("complete numeric answer %q was treated as partial", text)
		}
	}
	for _, test := range []IntegrityInput{
		{DoneReason: "length", Speakable: "15", GenTokens: 2, NumCtx: 8192},
		{DoneReason: "", Speakable: "15", GenTokens: 2, NumCtx: 8192},
		{DoneReason: "stop", Speakable: "15", GenTokens: 2, NumCtx: 8192, PromptTokens: 8190},
		{DoneReason: "stop", Speakable: "15 blocks and", GenTokens: 2, NumCtx: 8192},
		{DoneReason: "stop", Speakable: "1e", GenTokens: 2, NumCtx: 8192},
		{DoneReason: "stop", Speakable: "15\n15", GenTokens: 2, NumCtx: 8192},
	} {
		if got := evaluateStreamIntegrity(test); !got.Truncated {
			t.Errorf("partial or pressured answer was accepted: %+v", test)
		}
	}
}

func TestNativeStoppedNumericStreamDoesNotRetryOrContaminateHistory(t *testing.T) {
	var requests atomic.Int32
	b := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "15"}, Done: true, DoneReason: "stop", Metrics: api.Metrics{EvalCount: 2, PromptEvalCount: 100}})
	})
	b.ClearHistory()
	media := chatmedia.NewAudioSession()
	defer media.Close()
	b.SetAudioResolver(media)
	b.cfg.ContinuityIntegrityEnabled = true
	b.cfg.ContinuityRecoveryMax = 2
	stream, err := b.ThinkStream(t.Context(), "What is seven plus eight? Reply with only the number.", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := drainStream(t, stream)
	result := <-stream.Done
	if result.Err != nil || result.Integrity.Truncated || result.Recovered || text != "15" || requests.Load() != 1 {
		t.Fatalf("numeric answer retried/recovered: text=%q requests=%d result=%+v", text, requests.Load(), result)
	}
	history := b.History()
	if len(history) != 2 || history[1].Content != "15" {
		t.Fatal("numeric response contaminated canonical history", history)
	}
}
