package brain

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/ollama/ollama/api"
)

func TestNativeAudioWireAndCanonicalTranscriptRemainDistinct(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1, 0.2, -0.2})
	if err != nil {
		t.Fatal(err)
	}
	wav, err := media.ResolveAudio(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "u1", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Ready, Text: "hello assistant", Provider: "ollama", Model: "lfm3-audio:2b"}}
	calls := 0
	brain := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		found := false
		for _, message := range req.Messages {
			if message.Role == "user" && len(message.Images) > 0 {
				found = true
				if !bytes.Equal(message.Images[0], wav) {
					t.Error("model audio changed")
				}
				if message.Content != "" {
					t.Errorf("control transcript became competing input: %q", message.Content)
				}
			}
		}
		if !found {
			t.Error("WAV missing from actual request")
		}
		calls++
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Hello."}, Done: true})
	})
	brain.ClearHistory()
	brain.SetAudioResolver(media)
	if _, err := brain.ThinkFull(t.Context(), "", StreamOptions{Utterance: input}); err != nil {
		t.Fatal(err)
	}
	history := brain.History()
	if history[0].Content != "hello assistant" || history[0].Utterance == nil {
		t.Fatal(history)
	}
	history[0].Utterance.Audio.ID = "changed"
	if brain.History()[0].Utterance.Audio.ID != ref.ID {
		t.Fatal("canonical audio metadata aliases caller")
	}
	if err := brain.ImportHistory(brain.History()); err != nil {
		t.Fatal(err)
	}
	if _, err := brain.ThinkFull(t.Context(), "Continue", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestUnavailableAudioOnlyHistoryStopsBeforeDispatch(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "u2", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
	calls := 0
	brain := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Hello."}, Done: true})
	})
	brain.ClearHistory()
	brain.SetAudioResolver(media)
	if _, err := brain.ThinkFull(t.Context(), "", StreamOptions{Utterance: input}); err != nil {
		t.Fatal(err)
	}
	media.Release(ref)
	if _, err := brain.ThinkFull(t.Context(), "Continue", StreamOptions{}); !errors.Is(err, ErrAudioProjection) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("dispatched with missing audio", calls)
	}
}

func TestReadyTranscriptProjectsWhenAudioIsUnavailable(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{0.1})
	if err != nil {
		t.Fatal(err)
	}
	input := &utterance.Finalized{ID: "u3", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Ready, Text: "actual recognized words"}}
	calls := 0
	brain := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		found := false
		for _, m := range req.Messages {
			if m.Role == "user" && m.Content == "actual recognized words" && len(m.Images) == 0 {
				found = true
			}
		}
		if !found {
			t.Error("ready transcript absent from Standard projection")
		}
		calls++
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Okay."}, Done: true})
	})
	brain.ClearHistory()
	brain.LoadHistory([]Turn{{Role: "user", Content: input.Transcript.Text, Utterance: input}})
	if _, err := brain.ThinkFull(t.Context(), "Continue", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
