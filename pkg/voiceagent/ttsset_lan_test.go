package voiceagent

import (
	"net"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// unreachableSpeechURL is a loopback address nothing listens on: a bound-then-
// closed port, so the dial fails immediately instead of waiting on a firewall.
func unreachableSpeechURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr
}

func lanSetConfig(t *testing.T) *config.Config {
	return &config.Config{
		TTSProvider:         "qwen3-tts",
		TTSFallbackProvider: "kokoro",
		TTSPlace:            config.TTSPlaceLAN,
		TTSWorkerURL:        unreachableSpeechURL(t),
		TTSWorkerToken:      "s3cret",
		TTSLanTimeoutMS:     200,
	}
}

// A speech worker that is not there must never stop a conversation (D7) unless
// the operator asked for that explicitly.
func TestNewTTSSetUnreachableSpeechWorker(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*config.Config)
		wantErr  bool
		wantWarn string
	}{
		{
			name:    "strict refuses to enter talk",
			mutate:  func(cfg *config.Config) { cfg.TTSLanStrict = true },
			wantErr: true,
		},
		{
			name:    "fallback disabled refuses to enter talk",
			mutate:  func(cfg *config.Config) { cfg.TTSFallbackProvider = "disabled" },
			wantErr: true,
		},
		{
			name:     "default falls back to kokoro on this computer",
			mutate:   func(*config.Config) {},
			wantWarn: "unreachable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := lanSetConfig(t)
			tc.mutate(cfg)

			set, err := NewTTSSet(cfg)
			if tc.wantErr {
				if err == nil {
					set.Close()
					t.Fatal("expected NewTTSSet to refuse the conversation")
				}
				return
			}
			if err != nil {
				// Only a missing Kokoro install may skip here. A refusal that
				// came from config would otherwise hide a real regression.
				if strings.Contains(err.Error(), "tts_place") || strings.Contains(err.Error(), "tts_worker") {
					t.Fatalf("config was refused, not the connection: %v", err)
				}
				t.Skipf("kokoro not installable in this environment: %v", err)
			}
			defer set.Close()

			if _, isKokoro := set.Primary.(*tts.KokoroTTS); !isKokoro {
				t.Fatalf("primary=%T, want the local Kokoro provider", set.Primary)
			}
			if set.Fallback != nil {
				t.Fatal("kokoro is already the primary; it must not also be attached as the fallback")
			}
			if set.FallbackWarning == nil || !strings.Contains(set.FallbackWarning.Error(), tc.wantWarn) {
				t.Fatalf("warning=%v, want one naming the speech worker", set.FallbackWarning)
			}
		})
	}
}

// A missing token is a configuration error too: without it the refusal would
// look like an unreachable worker and silently become Kokoro.
func TestNewTTSSetLanWithoutATokenIsAnError(t *testing.T) {
	cfg := lanSetConfig(t)
	cfg.TTSWorkerToken = ""

	set, err := NewTTSSet(cfg)
	if err == nil {
		set.Close()
		t.Fatal("lan without a token must fail configuration, not fall back")
	}
	if !strings.Contains(err.Error(), "tts_worker_token") {
		t.Fatalf("err=%v want the token remediation", err)
	}
}

// The clone-voice degrade must reach the operator.
func TestNewTTSSetSurfacesTheCloneVoiceNotice(t *testing.T) {
	cfg := lanSetConfig(t)
	cfg.QwenTTSVoice = "Veronica"

	set, err := NewTTSSet(cfg)
	if err != nil {
		t.Skipf("kokoro not installable in this environment: %v", err)
	}
	defer set.Close()
	if set.VoiceNotice == nil || !strings.Contains(set.VoiceNotice.Error(), "clone voice") {
		t.Fatalf("notice=%v, want the clone-voice warning", set.VoiceNotice)
	}

	preset := lanSetConfig(t)
	preset.QwenTTSVoice = "Vivian"
	presetSet, err := NewTTSSet(preset)
	if err != nil {
		t.Skipf("kokoro not installable in this environment: %v", err)
	}
	defer presetSet.Close()
	if presetSet.VoiceNotice != nil {
		t.Fatalf("a preset voice must not warn: %v", presetSet.VoiceNotice)
	}

	local := lanSetConfig(t)
	local.TTSPlace = config.TTSPlaceLocal
	local.QwenTTSVoice = "Veronica"
	if notice := speechVoiceNotice(local); notice != nil {
		t.Fatalf("a clone on this computer must not warn: %v", notice)
	}
}

// A broken URL is a configuration error, not a reachability problem: it must
// not be papered over with Kokoro.
func TestNewTTSSetInvalidSpeechURLIsAnError(t *testing.T) {
	cfg := lanSetConfig(t)
	cfg.TTSWorkerURL = "http://192.168.1.1:7263"

	set, err := NewTTSSet(cfg)
	if err == nil {
		set.Close()
		t.Fatal("plaintext off-loopback must fail configuration, not fall back")
	}
}

// Place is topology: with Kokoro selected it changes nothing.
func TestNewTTSSetKokoroIgnoresPlace(t *testing.T) {
	cfg := lanSetConfig(t)
	cfg.TTSProvider = "kokoro"

	set, err := NewTTSSet(cfg)
	if err != nil {
		t.Skipf("kokoro not installable in this environment: %v", err)
	}
	defer set.Close()
	if set.FallbackWarning != nil {
		t.Fatalf("kokoro primary must not warn about a speech worker: %v", set.FallbackWarning)
	}
}
