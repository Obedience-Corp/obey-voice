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
		{name: "tls off box", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://192.168.1.1:7263"}},
		{name: "named tls host", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "https://studio:7263"}},
		{name: "plaintext loopback ipv4", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://127.0.0.1:7263"}},
		{name: "plaintext loopback ipv6", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://[::1]:7263"}},
		{name: "plaintext localhost", cfg: Config{TTSPlace: TTSPlaceLAN, TTSWorkerURL: "http://localhost:7263"}},
		{name: "mixed case place", cfg: Config{TTSPlace: "LAN", TTSWorkerURL: "https://studio:7263"}},
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
