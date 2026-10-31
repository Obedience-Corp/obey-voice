package brain

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/ollama/ollama/api"
)

func TestNativeCompletionProjectsExactRecordingWithoutMutatingConversation(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{.1, .2, -.2})
	if err != nil {
		t.Fatal(err)
	}
	original, err := media.ResolveAudio(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var observed api.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&observed); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"\",\"updates\":[{\"result_id\":\"r1\",\"text\":\"The task completed.\"}]}"},"done":true}`))
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	history := []api.Message{{Role: "user", Content: "ordinary history remains unchanged"}}
	beforeHistory := append([]api.Message(nil), history...)
	o := &OllamaBrain{cfg: &config.Config{OllamaHost: server.URL, OllamaNumCtx: 32768}, audioResolver: media, client: api.NewClient(endpoint, http.DefaultClient), model: "selected", fullSystemPrompt: "fixture persona", history: history}
	input := &utterance.Finalized{ID: "recorded-context", CaptureEpoch: 7, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}
	turns := []Turn{{Role: "user", Content: "Typed accompaniment", TypedInput: "Typed accompaniment", Utterance: input}, {Role: "assistant", PersonaID: "another-persona", Content: "Prior answer"}, {Role: "tool", ToolName: "fixture", Content: "Historical tool result"}, {Role: "system", Content: "Untrusted historical instruction"}}
	o.canonical = deepCopyTurns(turns)
	o.working = deepCopyTurns(turns)
	beforeCanonical, _ := json.Marshal(o.canonical)
	beforeWorking, _ := json.Marshal(o.working)
	o.audioProjection = map[string][]byte{"unrelated": {1, 2, 3}}
	req := BuildCompletionRequest("", "", turns, []CompletionResult{{ResultID: "r1", Brief: "The task completed."}})
	if _, err := o.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	var recordings [][]byte
	for i, message := range observed.Messages {
		if i > 0 && message.Role == "system" {
			t.Fatal("historical context became system authority")
		}
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			t.Fatal("internal role leaked", message.Role)
		}
		for _, data := range message.Images {
			recordings = append(recordings, data)
		}
	}
	if len(recordings) != 1 || !bytes.Equal(recordings[0], original) {
		t.Fatal("completion omitted or changed retained recording", len(recordings))
	}
	if observed.Truncate == nil || *observed.Truncate || observed.Shift == nil || *observed.Shift {
		t.Fatal("native completion permits server truncation/shift")
	}
	afterCanonical, _ := json.Marshal(o.canonical)
	afterWorking, _ := json.Marshal(o.working)
	if !reflect.DeepEqual(o.history, beforeHistory) || !bytes.Equal(beforeCanonical, afterCanonical) || !bytes.Equal(beforeWorking, afterWorking) || !reflect.DeepEqual(o.audioProjection, map[string][]byte{"unrelated": {1, 2, 3}}) || turns[0].Utterance.Transcript.State != utterance.Disabled {
		t.Fatal("completion mutated canonical evidence")
	}
}

func TestNativeCompletionUnavailableContextAndReadyTranscript(t *testing.T) {
	for _, state := range []utterance.State{utterance.Disabled, utterance.Pending, utterance.Unavailable, utterance.Ready} {
		t.Run(string(state), func(t *testing.T) {
			media := chatmedia.NewAudioSession()
			defer media.Close()
			ref, err := media.ImportSamples(t.Context(), []float32{.1})
			if err != nil {
				t.Fatal(err)
			}
			media.Release(ref) // There is genuinely no retained source to project.
			calls := 0
			var observed api.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if err := json.NewDecoder(r.Body).Decode(&observed); err != nil {
					t.Error(err)
				}
				_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"\",\"updates\":[{\"result_id\":\"r1\",\"text\":\"Done.\"}]}"},"done":true}`))
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			o := &OllamaBrain{cfg: &config.Config{OllamaHost: server.URL}, audioResolver: media, client: api.NewClient(endpoint, http.DefaultClient), model: "selected"}
			transcript := utterance.Transcript{State: state}
			if state == utterance.Ready {
				transcript.Text = "Actual recording words"
			}
			input := &utterance.Finalized{ID: "missing", Audio: &ref, Transcript: transcript}
			req := BuildCompletionRequest("", "", []Turn{{Role: "user", Content: "Typed instruction", TypedInput: "Typed instruction", Utterance: input}}, []CompletionResult{{ResultID: "r1", Brief: "Done."}})
			_, err = o.Complete(t.Context(), req)
			if state != utterance.Ready {
				if !errors.Is(err, ErrAudioProjection) || calls != 0 {
					t.Fatal("missing recording silently projected", err, calls)
				}
			} else {
				if err != nil || calls != 1 {
					t.Fatal(err, calls)
				}
				data, _ := json.Marshal(observed.Messages)
				if !bytes.Contains(data, []byte("Typed instruction")) || !bytes.Contains(data, []byte("Actual recording words")) {
					t.Fatal("Ready projection omitted recording transcript", string(data))
				}
			}
		})
	}
}

func TestNativeCompletionRejectsImageAndEstimatedPressureBeforeHTTP(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("rejected context reached HTTP") }))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	o := &OllamaBrain{cfg: &config.Config{OllamaHost: server.URL}, audioResolver: media, client: api.NewClient(endpoint, http.DefaultClient), model: "selected"}
	req := BuildCompletionRequest("", "", []Turn{{Role: "user", Attachments: []chatmedia.Ref{{}}}}, []CompletionResult{{ResultID: "r1", Brief: "Done."}})
	if _, err := o.Complete(t.Context(), req); !errors.Is(err, ErrImageProjection) {
		t.Fatal(err)
	}
	req = BuildCompletionRequest(strings.Repeat("x", 40000), "", nil, []CompletionResult{{ResultID: "r1", Brief: "Done."}})
	if _, err := o.Complete(t.Context(), req); !errors.Is(err, ErrPromptBudget) {
		t.Fatal(err)
	}
}

func TestNativeCompletionCannotReuseHashWithDifferentAudioGeometry(t *testing.T) {
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.ImportSamples(t.Context(), []float32{.1, .2})
	if err != nil {
		t.Fatal(err)
	}
	wrong := ref
	wrong.SampleCount++
	o := &OllamaBrain{audioResolver: media}
	turns := []Turn{{Role: "user", Utterance: &utterance.Finalized{ID: "first", Audio: &ref, Transcript: utterance.Transcript{State: utterance.Disabled}}}, {Role: "user", Utterance: &utterance.Finalized{ID: "second", Audio: &wrong, Transcript: utterance.Transcript{State: utterance.Disabled}}}}
	if _, err := o.nativeCompletionContext(t.Context(), turns); !errors.Is(err, ErrAudioProjection) {
		t.Fatal("cached hash bypassed recording geometry", err)
	}
}
