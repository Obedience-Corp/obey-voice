package brain

import (
	"context"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type recordingWorker struct {
	lastOpts StreamOptions
	reply    string
}

func (r *recordingWorker) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	r.lastOpts = opts
	return (&fakeStreamBrain{chunks: []string{r.reply}}).ThinkStream(ctx, input, opts)
}

func (r *recordingWorker) ThinkFull(ctx context.Context, input string, opts StreamOptions) (string, error) {
	r.lastOpts = opts
	return r.reply, nil
}

func (r *recordingWorker) ClearHistory()      {}
func (r *recordingWorker) History() []Turn    { return nil }
func (r *recordingWorker) LoadHistory([]Turn) {}

type fakeStreamBrain struct {
	chunks []string
}

func (f *fakeStreamBrain) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	out := make(chan string, len(f.chunks))
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		for _, chunk := range f.chunks {
			select {
			case <-ctx.Done():
				done <- StreamResult{Err: ctx.Err()}
				return
			case out <- chunk:
			}
		}
		done <- StreamResult{}
	}()
	return &Stream{Chunks: out, Done: done}, nil
}

func (f *fakeStreamBrain) ThinkFull(context.Context, string, StreamOptions) (string, error) {
	if len(f.chunks) == 0 {
		return "", nil
	}
	return f.chunks[0], nil
}

func (f *fakeStreamBrain) ClearHistory()      {}
func (f *fakeStreamBrain) History() []Turn    { return nil }
func (f *fakeStreamBrain) LoadHistory([]Turn) {}

func TestWorkerStreamOptions(t *testing.T) {
	opts := WorkerStreamOptions(true)
	if !opts.ToolsEnabled {
		t.Fatal("ToolsEnabled = false, want true")
	}
	if !opts.OmitTurnInstruction {
		t.Fatal("OmitTurnInstruction = false, want true")
	}
	if opts.RouterToolsPolicy != RouterToolsLegacy {
		t.Fatalf("RouterToolsPolicy = %q, want legacy", opts.RouterToolsPolicy)
	}
}

func TestNewWorkerProviderRefusesTwoLocalOllama(t *testing.T) {
	_, err := NewWorkerProvider(&config.Config{
		BrainProvider:       "ollama",
		OllamaHost:          "http://localhost:11434",
		VoiceWorkerProvider: "ollama",
	})
	if err == nil {
		t.Fatal("expected error for two local Ollama workers")
	}
	if !strings.Contains(err.Error(), "second local model") {
		t.Fatalf("err = %q, want second local model", err)
	}
}

func TestWorkerConfigDefaultsToGrok(t *testing.T) {
	cfg := &config.Config{}
	got := config.WorkerBrainConfig(cfg)
	if got.BrainProvider != "grok" {
		t.Fatalf("worker provider = %q, want grok", got.BrainProvider)
	}
}

func TestWorkerConfigModelOverride(t *testing.T) {
	cfg := &config.Config{
		VoiceWorkerProvider:   "ollama",
		VoiceWorkerModel:      "qwen3:30b",
		VoiceWorkerOllamaHost: "http://studio:11434",
	}
	got := config.WorkerBrainConfig(cfg)
	if got.OllamaModel != "qwen3:30b" {
		t.Fatalf("OllamaModel = %q", got.OllamaModel)
	}
	if got.OllamaHost != "http://studio:11434" {
		t.Fatalf("OllamaHost = %q", got.OllamaHost)
	}
}

func TestRecordingWorkerHonorsStreamOptions(t *testing.T) {
	w := &recordingWorker{reply: "done"}
	_, err := w.ThinkFull(context.Background(), "task", WorkerStreamOptions(true))
	if err != nil {
		t.Fatalf("ThinkFull: %v", err)
	}
	if !w.lastOpts.OmitTurnInstruction || !w.lastOpts.ToolsEnabled {
		t.Fatalf("opts = %+v, want tools on and omit turn instruction", w.lastOpts)
	}
}
