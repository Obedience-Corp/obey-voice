package qwen

import (
	tts "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"testing"
)

// A latency artifact that names an engine but not the computer it ran on
// cannot be compared with one recorded on the other topology, which is the
// comparison the fleet design exists to make (E9).
func TestQwenReportsItsSpeechPlace(t *testing.T) {
	local := newQwen3TTS("", "", 0, nil)
	local.native = true
	if got := local.SpeechPlace(); got != tts.PlaceLocal {
		t.Errorf("local SpeechPlace() = %q, want %q", got, tts.PlaceLocal)
	}
	if local.WorkerStreaming() {
		t.Error("a provider with no live session must not claim its worker streams")
	}
	if got := tts.SpeechPlaceOf(local); got != tts.PlaceLocal {
		t.Errorf("SpeechPlaceOf() = %q, want %q", got, tts.PlaceLocal)
	}

	srv := startFakeSpeechServer(t, &fakeSpeechServer{token: "s3cret"})
	cfg := lanConfig(srv.url(), "s3cret")
	remote, err := newRemoteQwen3TTS(cfg)
	if err != nil {
		t.Fatalf("newRemoteQwen3TTS: %v", err)
	}
	defer remote.Delete()

	if got := remote.SpeechPlace(); got != tts.PlaceLAN {
		t.Errorf("remote SpeechPlace() = %q, want %q", got, tts.PlaceLAN)
	}
	if got := tts.SpeechPlaceOf(remote); got != tts.PlaceLAN {
		t.Errorf("SpeechPlaceOf() = %q, want %q", got, tts.PlaceLAN)
	}
	// Stage A: the ready frame says streaming=false, so first audio cannot
	// arrive before the whole utterance is generated. Recording that beside
	// the timings is what makes them explicable.
	if remote.WorkerStreaming() || tts.WorkerStreamingOf(remote) {
		t.Error("stage A ready must not read as a streaming worker")
	}
}

// The tier is part of the engine's identity, and over LAN the local
// qwen_tts_model_tier describes a worker this process never started.
func TestQwenModelTierPrefersTheWorkersOwnTier(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{
		token: "s3cret",
		ready: `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le","streaming":true,"tier":"1.7b"}`,
	})
	cfg := lanConfig(srv.url(), "s3cret")
	// Deliberately disagrees with the worker: the local key must lose.
	cfg.QwenTTSModelTier = "0.6b"

	remote, err := newRemoteQwen3TTS(cfg)
	if err != nil {
		t.Fatalf("newRemoteQwen3TTS: %v", err)
	}
	defer remote.Delete()

	if got := remote.ModelTier(); got != "1.7b" {
		t.Errorf("ModelTier() = %q, want the worker's own 1.7b, not the local 0.6b key", got)
	}
	if !remote.WorkerStreaming() {
		t.Error("WorkerStreaming() = false for a ready frame that advertised streaming")
	}
}

// A worker that reports no tier leaves it empty rather than substituting a
// local key that describes a different model.
func TestQwenModelTierIsEmptyWhenTheWorkerNamesNoTier(t *testing.T) {
	srv := startFakeSpeechServer(t, &fakeSpeechServer{
		token: "s3cret",
		ready: `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le"}`,
	})
	cfg := lanConfig(srv.url(), "s3cret")
	cfg.QwenTTSModelTier = "0.6b"

	remote, err := newRemoteQwen3TTS(cfg)
	if err != nil {
		t.Fatalf("newRemoteQwen3TTS: %v", err)
	}
	defer remote.Delete()

	if got := remote.ModelTier(); got != "" {
		t.Errorf("ModelTier() = %q, want empty: the worker never said which model it loaded", got)
	}
}

// A local provider's tier still comes from config: there is no worker to ask,
// and the process that spawned the child chose the GGUF itself.
func TestQwenLocalModelTierComesFromConfig(t *testing.T) {
	local := newQwen3TTS("", "", 0, nil)
	local.native = true
	local.modelTier = "1.7b"
	if got := local.ModelTier(); got != "1.7b" {
		t.Errorf("local ModelTier() = %q, want 1.7b", got)
	}
}
