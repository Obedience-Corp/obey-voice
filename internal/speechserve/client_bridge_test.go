package speechserve

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/qwen"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// The host's own LAN client against the real bridge and a real child worker.
// Everything below the handshake is the frozen child protocol, so this is the
// end-to-end contract: text in on TCP, the same PCM out that a local worker
// would have produced.
func hostConfig(addr string) *config.Config {
	return &config.Config{
		TTSProvider:     "qwen3-tts",
		TTSPlace:        config.TTSPlaceLAN,
		TTSWorkerURL:    "http://" + addr,
		TTSWorkerToken:  testToken,
		TTSLanTimeoutMS: 2000,
		QwenTTSTimeout:  30,
	}
}

func speakThrough(t *testing.T, addr, text string) int {
	t.Helper()
	provider, err := qwen.NewQwen3TTS(hostConfig(addr))
	if err != nil {
		t.Fatalf("NewQwen3TTS: %v", err)
	}
	// Delete closes the session, which sends shutdown — the line the bridge
	// must intercept.
	defer provider.Delete()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := provider.SynthesizeRequest(ctx, tts.SynthesisRequest{Text: text})
	if err != nil {
		t.Fatalf("SynthesizeRequest: %v", err)
	}
	var samples int
	for frame := range result.Stream.Frames() {
		samples += len(frame)
	}
	return samples
}

func TestHostClientSpeaksThroughTheBridge(t *testing.T) {
	bridge := startTestBridge(t, "0")

	if got := speakThrough(t, bridge.Addr(), "hello from the other computer"); got != 3 {
		t.Fatalf("samples=%d want 3", got)
	}
	saw := bridge.childSaw(t)
	if !strings.Contains(saw, `"synthesize"`) {
		t.Fatalf("the child never saw the request: %q", saw)
	}
	if strings.Contains(saw, "hello") && strings.Contains(saw, config.SpeechProtocol) {
		t.Fatalf("the hello frame reached the child: %q", saw)
	}

	// The provider above was deleted, which sent shutdown. If the bridge had
	// forwarded it, this second conversation would pay a cold model load — or
	// find nothing at all.
	if got := speakThrough(t, bridge.Addr(), "and again, still warm"); got != 3 {
		t.Fatalf("samples=%d want 3 from the same warm worker", got)
	}
	if strings.Contains(bridge.childSaw(t), `"shutdown"`) {
		t.Fatalf("a client shutdown reached the child: %q", bridge.childSaw(t))
	}
}

func TestHostClientRejectedWithoutTheToken(t *testing.T) {
	bridge := startTestBridge(t, "0")
	cfg := hostConfig(bridge.Addr())
	cfg.TTSWorkerToken = "not-the-token"

	_, err := qwen.NewQwen3TTS(cfg)
	if !tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) {
		t.Fatalf("err=%v want an unavailable provider error", err)
	}
	if !strings.Contains(err.Error(), msgUnauthorized) {
		t.Fatalf("err=%v want the refusal message", err)
	}
}

// The doctor probe closes without shutdown; the bridge must free the client
// slot anyway, or one probe would lock out the conversation host.
func TestDoctorProbeLeavesTheBridgeUsable(t *testing.T) {
	bridge := startTestBridge(t, "0")
	cfg := hostConfig(bridge.Addr())

	result, err := config.ProbeSpeechWorker(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ProbeSpeechWorker: %v", err)
	}
	if result.Tier != "0.6b" || result.Streaming {
		t.Fatalf("probe=%+v", result)
	}
	if strings.Contains(bridge.childSaw(t), `"shutdown"`) {
		t.Fatalf("the probe reached the child: %q", bridge.childSaw(t))
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		frame, err := tryHello(bridge.Addr())
		if err == nil && frame["type"] == "ready" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a probe locked the bridge (frame=%v err=%v)", frame, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
