//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type productFinalizedInput struct{ input *utterance.Finalized }

func (*productFinalizedInput) Available() bool { return true }
func (p *productFinalizedInput) Start(context.Context) (utterance.Session, error) {
	events := make(chan utterance.Event, 1)
	events <- utterance.Event{Kind: utterance.FinalizedEvent, Input: p.input.Clone()}
	close(events)
	return &productFinalizedSession{events: events}, nil
}

type productFinalizedSession struct{ events chan utterance.Event }

func (s *productFinalizedSession) Events() <-chan utterance.Event { return s.events }
func (*productFinalizedSession) Close() error                     { return nil }

// Inject only finalized software input through the public pipeline seam. The
// actual product builder, selector, ASR client, admission and work router remain
// installed. Capture geometry and selected speech have separate owning proofs.
func TestNativeProductSpokenStandardControlsAndIndependentWorker(t *testing.T) {
	dir := personaEnv(t)
	var transcript atomic.Value
	transcript.Store("Ready")
	var asr, foreground, worker atomic.Int32
	var mu sync.Mutex
	var images [][]byte
	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWorker) }) }
	defer release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"fixture"}`)
		case "/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"lfm3-audio:2b","digest":"%s"},{"name":"independent-worker"}]}`, strings.Repeat("a", 64))
		case "/api/show":
			fmt.Fprint(w, `{"details":{"family":"fixture"},"capabilities":["completion","audio","speech","tools"],"voices":["female"]}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"lfm3-audio:2b"}]}`)
		case "/v1/audio/voices":
			w.WriteHeader(404)
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0, 0, 1, 0})
		case "/v1/audio/transcriptions":
			asr.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"text": transcript.Load().(string)})
		case "/api/chat":
			var req struct {
				Model    string
				Stream   bool
				Messages []struct {
					Content string
					Images  [][]byte
				}
				Format json.RawMessage
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			answer := "15"
			if req.Model == "independent-worker" {
				if worker.Add(1) == 1 {
					close(workerStarted)
				}
				select {
				case <-releaseWorker:
				case <-r.Context().Done():
					return
				}
				answer = "Independent worker result."
			} else if req.Stream {
				foreground.Add(1)
				answer = "Native conversation answer."
				mu.Lock()
				for _, message := range req.Messages {
					images = append(images, message.Images...)
				}
				mu.Unlock()
			} else if len(req.Format) > 0 {
				t.Error("unexpected model routing classifier", string(req.Format))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "message": map[string]string{"role": "assistant", "content": answer}, "done": true, "done_reason": "stop", "prompt_eval_count": 64, "eval_count": 8})
		default:
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	data := fmt.Sprintf("active_persona: samantha\nbrain_provider: ollama\nollama_host: %s\nollama_model: independent-worker\nvoice_worker_provider: ollama\nvoice_worker_model: independent-worker\nvoice_worker_ollama_host: %s\nmodels_dir: %s\nwork_dir: %s\nvoice_work_enabled: true\nvoice_tools_enabled: false\nskills_enabled: false\nvad_enabled: false\n", server.URL, server.URL, filepath.Join(dir, "empty-models"), t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := persona.Load("samantha")
	if err != nil {
		t.Fatal(err)
	}
	p.Audio = &persona.Audio{Mode: persona.AudioNative, Endpoint: server.URL, Voice: "female", Controls: persona.AudioControlsStandard, Transcript: persona.AudioTranscriptAuto}
	p.Brain.Worker = persona.Worker{Provider: "ollama", Model: "independent-worker", OllamaHost: server.URL}
	writePersona(t, p)
	rt, err := BuildDesktopConversation(t.Context(), DesktopOptions{PersonaID: "samantha", TextOnly: true, Silent: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Cleanup()
	if rt.Pipeline.AdmitInput == nil || rt.Pipeline.BeforeCapture == nil || rt.Pipeline.Native.Controls != "standard" || asr.Load() != 1 {
		t.Fatal("required setup/admission missing", asr.Load())
	}
	input := &productFinalizedInput{}
	rt.Pipeline.Input = input
	epoch := uint64(1)
	run := func(text string) (string, error) {
		transcript.Store(text)
		ref, err := rt.Pipeline.Native.Media.ImportSamples(t.Context(), []float32{float32(epoch) / 100, .2, -.2})
		if err != nil {
			t.Fatal(err)
		}
		input.input = &utterance.Finalized{ID: fmt.Sprintf("spoken-%d", epoch), CaptureEpoch: epoch, Audio: &ref, Transcript: utterance.Transcript{State: utterance.Pending}}
		epoch++
		return rt.Pipeline.RunTurn(t.Context())
	}
	// Bare current-persona address is a real local selector response.
	beforeASR := asr.Load()
	beforeChat := foreground.Load()
	if _, err := run("Samantha"); err != nil {
		t.Fatal(err)
	}
	if asr.Load() != beforeASR+1 || foreground.Load() != beforeChat {
		t.Fatal("local address did not ASR once before local admission")
	}
	history := rt.Pipeline.Brain.History()
	if len(history) != 2 || history[1].Content != "Samantha here." || history[0].Utterance == nil || history[0].Utterance.Transcript.State != utterance.Ready {
		t.Fatal("local selector history lost actual ASR/input", history)
	}
	// Explicit unknown identities refuse before chat/worker dispatch.
	beforeChat = foreground.Load()
	if _, err := run("@no-such-persona help me"); err == nil {
		t.Fatal("unknown identity silently admitted")
	}
	if foreground.Load() != beforeChat || worker.Load() != 0 {
		t.Fatal("refused identity dispatched inference")
	}
	// Conversational admission still carries the original recording, even though
	// Standard controls already obtained an actual same-model transcript.
	if _, err := run("Samantha, answer this fixture question"); err != nil {
		t.Fatal(err)
	}
	if foreground.Load() != beforeChat+1 {
		t.Fatal("admitted spoken request did not reach selected native brain")
	}
	mu.Lock()
	hasAudio := len(images) > 0
	mu.Unlock()
	if !hasAudio {
		t.Fatal("required ASR replaced native recording on chat wire")
	}
	// Phrase routing dispatches the independent worker, then status inspects it.
	if _, err := run("search the web for fixture topic"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("spoken work command never dispatched independent worker")
	}
	beforeChat = foreground.Load()
	if _, err := run("status"); err != nil {
		t.Fatal(err)
	}
	if worker.Load() != 1 || foreground.Load() != beforeChat {
		t.Fatal("status changed worker or invoked foreground chat", worker.Load(), foreground.Load())
	}
	// Keep the fixture worker deliberately waiting; exercise the existing
	// spoken cancel command rather than inventing external search results.
	if _, err := run("cancel that"); err != nil {
		t.Fatal(err)
	}
	snapshot := rt.Pipeline.WorkSnapshot()
	if len(snapshot.Pending) == 0 {
		select {
		case <-rt.Pipeline.WorkCoordinator().Ready():
		case <-time.After(3 * time.Second):
			t.Fatal("spoken cancel did not publish terminal result")
		}
		snapshot = rt.Pipeline.WorkSnapshot()
	}
	if len(snapshot.Jobs) != 1 || len(snapshot.Pending) != 1 || snapshot.Pending[0].Outcome.Status != "cancelled" || snapshot.Jobs[0].Model != "independent-worker" {
		t.Fatal("spoken work cancellation lost canonical worker identity", snapshot)
	}
	release()
	after, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || string(after) != data {
		t.Fatal("spoken controls changed global/worker settings", err)
	}
	if rt.SessionCfg.VoiceWorkerModel != "independent-worker" {
		t.Fatal("Native mode replaced independent worker model")
	}
	rt.Persist()
	t.Logf("actual product selector/control path: required ASR=%d; native foreground=%d; independent worker=%d", asr.Load(), foreground.Load(), worker.Load())
}
