package config

import (
	"strings"
	"testing"
)

func TestValidateTTSPlace(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "unknown place",
			cfg:  Config{TTSPlace: "cloud"},
			want: "unsupported tts_place",
		},
		{
			name: "lan without a url",
			cfg:  Config{TTSPlace: TTSPlaceLAN},
			want: "requires tts_worker_url",
		},
		{
			name: "plaintext off box",
			cfg:  Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://192.168.1.1:7263"},
			want: "plaintext",
		},
		{
			name: "credentials in the url",
			cfg:  Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://user:secret@studio:7263"},
			want: "must not carry credentials",
		},
		{
			name: "unknown scheme",
			cfg:  Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "tcp://studio:7263"},
			want: "https",
		},
		{
			name: "no host",
			cfg:  Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://"},
			want: "no host",
		},
		{name: "empty place is local", cfg: Config{}},
		{name: "explicit local ignores the url", cfg: Config{TTSPlace: TTSPlaceLocal, TTSWorkerURL: "nonsense"}},
		{
			name: "lan without a token",
			cfg:  Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263"},
			want: "requires tts_worker_token",
		},
		{name: "tls off box", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://192.168.1.1:7263", TTSWorkerToken: "t"}},
		{name: "named tls host", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"}},
		{name: "plaintext loopback ipv4", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://127.0.0.1:7263", TTSWorkerToken: "t"}},
		{name: "plaintext loopback ipv6", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://[::1]:7263", TTSWorkerToken: "t"}},
		{name: "plaintext localhost", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://localhost:7263", TTSWorkerToken: "t"}},
		{name: "mixed case place", cfg: Config{TTSPlace: "LAN", TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTTSPlace(&tc.cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateTTSPlace: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
		})
	}
}

// The token may come from the environment instead of the file.
func TestValidateTTSPlaceAcceptsAnEnvironmentToken(t *testing.T) {
	cfg := Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263"}
	if err := ValidateTTSPlace(&cfg); err == nil {
		t.Fatal("expected a missing token to fail")
	}
	t.Setenv(SpeechTokenEnv, "from-env")
	if err := ValidateTTSPlace(&cfg); err != nil {
		t.Fatalf("ValidateTTSPlace with an environment token: %v", err)
	}
}

// A clone voice cannot travel: the degrade is deliberate, and loud.
func TestSpeechVoiceNotice(t *testing.T) {
	lan := func(voice string) *Config {
		return &Config{
			TTSProvider: "qwen3-tts", TTSPlace: TTSPlaceLAN,
			TTSWorkerURL: "https://studio:7263", TTSWorkerToken: "t",
			QwenTTSVoice: voice,
		}
	}
	for _, tc := range []struct {
		name string
		cfg  *Config
		want string
	}{
		{name: "shipped clone by name", cfg: lan("Veronica"), want: "Veronica"},
		{name: "shipped clone by id", cfg: lan("veronicadesk"), want: "VeronicaDesk"},
		{name: "empty resolves to the shipped default", cfg: lan(""), want: "Veronica"},
		{name: "default keyword", cfg: lan("default"), want: "Veronica"},
		{name: "model preset", cfg: lan("Vivian")},
		{name: "local clone is fine", cfg: &Config{TTSProvider: "qwen3-tts", QwenTTSVoice: "Veronica"}},
		{name: "kokoro ignores place", cfg: &Config{TTSProvider: "kokoro", TTSPlace: TTSPlaceLAN}},
		{name: "nil config", cfg: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SpeechVoiceNotice(tc.cfg)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("notice = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "clone voice") {
				t.Fatalf("notice = %q, want it to name %q", got, tc.want)
			}
		})
	}
}

func TestValidateQwenTTSConfigCarriesThePlaceRule(t *testing.T) {
	if err := ValidateQwenTTSConfig(&Config{TTSPlace: TTSPlaceLAN}); err == nil {
		t.Fatal("expected lan without a url to fail Qwen validation")
	}
	// A Kokoro install sets none of the new keys and must stay valid.
	if err := ValidateQwenTTSConfig(&Config{TTSProvider: "kokoro"}); err != nil {
		t.Fatalf("kokoro defaults must not require the LAN keys: %v", err)
	}
}

func TestTTSPlaceDefaultsAreLocal(t *testing.T) {
	loadIntoEmptyInstall(t)

	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.TTSPlace != TTSPlaceLocal {
		t.Fatalf("tts_place=%q want %q", cfg.TTSPlace, TTSPlaceLocal)
	}
	if cfg.TTSWorkerURL != "" || cfg.TTSWorkerToken != "" || cfg.TTSLanStrict {
		t.Fatalf("LAN keys default to %+v, want empty", cfg)
	}
	if cfg.TTSLanTimeoutMS != DefaultTTSLanTimeoutMS {
		t.Fatalf("tts_lan_timeout_ms=%d want %d", cfg.TTSLanTimeoutMS, DefaultTTSLanTimeoutMS)
	}
	if got := TTSLanTimeoutMSOf(&Config{}); got != DefaultTTSLanTimeoutMS {
		t.Fatalf("TTSLanTimeoutMSOf(zero)=%d want %d", got, DefaultTTSLanTimeoutMS)
	}
}

func TestTTSPlaceSchemaRoundTrip(t *testing.T) {
	path := newInstall(t, "tts_provider: kokoro\n")

	spec, ok := SpecFor("tts_place")
	if !ok {
		t.Fatal("tts_place has no KeySpec")
	}
	if spec.Type != TypeEnum || spec.Default != TTSPlaceLocal {
		t.Fatalf("spec=%+v", spec)
	}
	if strings.Join(spec.Enum, ",") != TTSPlaceLocal+","+TTSPlaceLAN {
		t.Fatalf("enum=%v", spec.Enum)
	}
	if !spec.Editable || spec.PersonaOverridable {
		t.Fatalf("place is an editable host setting, not a persona field: %+v", spec)
	}

	if _, err := SetKeyFile("tts_place", "cloud"); err == nil {
		t.Fatal("expected an off-enum place to be refused")
	}
	mustSet(t, "tts_place", TTSPlaceLAN)
	if got := readConfig(t, path); !strings.Contains(got, "tts_place: lan") {
		t.Fatalf("config after set:\n%s", got)
	}
	if got := Get("tts_place"); got != TTSPlaceLAN {
		t.Fatalf("in-process value=%v", got)
	}

	if _, err := UnsetKeyFile("tts_place"); err != nil {
		t.Fatalf("UnsetKeyFile: %v", err)
	}
	if got := Get("tts_place"); got != TTSPlaceLocal {
		t.Fatalf("unset left tts_place=%v, want the local default", got)
	}
}

// The token's help text promises the environment variable works. This is the
// binding that makes that true — and the schema reports the same binding the
// loader honors, so `config schema` cannot advertise one that does nothing.
func TestTTSWorkerTokenReadsTheEnvironment(t *testing.T) {
	t.Setenv("OBEY_SPEECH_TOKEN", "from-env")
	loadIntoEmptyInstall(t)

	if got := Get("tts_worker_token"); got != "from-env" {
		t.Fatalf("tts_worker_token = %v, want the environment value", got)
	}
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if cfg.TTSWorkerToken != "from-env" {
		t.Fatalf("cfg.TTSWorkerToken = %q, want the environment value", cfg.TTSWorkerToken)
	}
	value := Values()["tts_worker_token"]
	if value.Source != SourceEnv {
		t.Fatalf("source = %q, want %q", value.Source, SourceEnv)
	}
	// Secret: reported as set, never rendered — not even from the environment.
	if value.Value != "" {
		t.Fatalf("config get rendered the token: %v", value.Value)
	}
	spec, ok := SpecFor("tts_worker_token")
	if !ok || spec.Env != "OBEY_SPEECH_TOKEN" {
		t.Fatalf("spec=%+v, want the schema to report the binding", spec)
	}
}

func TestTTSWorkerTokenIsSecret(t *testing.T) {
	loadIntoEmptyInstall(t)

	spec, ok := SpecFor("tts_worker_token")
	if !ok {
		t.Fatal("tts_worker_token has no KeySpec")
	}
	if !spec.Secret {
		t.Fatal("the speech token must be marked secret so config get never prints it")
	}
	if got := Values()["tts_worker_token"].Value; got != "" {
		t.Fatalf("config get rendered the token: %v", got)
	}
}
