package config

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

func TestQwenTTSStreamOfNormalizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *Config
		want string
	}{
		{name: "nil config", cfg: nil, want: QwenStreamAuto},
		{name: "unset predates the key", cfg: &Config{}, want: QwenStreamAuto},
		{name: "whitespace and case", cfg: &Config{QwenTTSStream: "  OFF "}, want: QwenStreamOff},
		{name: "explicit on", cfg: &Config{QwenTTSStream: "on"}, want: QwenStreamOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QwenTTSStreamOf(tc.cfg); got != tc.want {
				t.Fatalf("QwenTTSStreamOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateQwenTTSStreamRejectsAnUnknownMode(t *testing.T) {
	if err := ValidateQwenTTSStream(&Config{QwenTTSStream: "always"}); err == nil {
		t.Fatal("qwen_tts_stream=always was accepted")
	} else if !strings.Contains(err.Error(), "qwen_tts_stream") {
		t.Fatalf("error %q does not name the setting to fix", err)
	}
	for _, mode := range []string{"", QwenStreamAuto, QwenStreamOn, QwenStreamOff} {
		if err := ValidateQwenTTSStream(&Config{QwenTTSStream: mode}); err != nil {
			t.Fatalf("ValidateQwenTTSStream(%q): %v", mode, err)
		}
	}
}

// The eligibility table is the whole safety argument for slice 5c, so it is
// asserted case by case rather than inferred from a live worker.
//
// The measurement it encodes (qwen3-tts-native
// docs/latency/stage_b_streaming_2026-08-27.json): CUDA streams at RTF 0.55
// with zero underruns over 15 runs; Metal streams at RTF 1.66 — worse than its
// own 1.40 whole-utterance baseline — with 120 underruns over the same 15.
func TestQwenStreamEligible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		remote  bool
		backend string
		capable bool
		want    bool
	}{
		{
			name: "auto lan is the measured CUDA offload target",
			mode: QwenStreamAuto, remote: true, capable: true, want: true,
		},
		{
			name: "auto local cuda passed the gate with 2x headroom",
			mode: QwenStreamAuto, backend: "cuda", capable: true, want: true,
		},
		{
			name: "auto local cuda hint is case insensitive",
			mode: QwenStreamAuto, backend: "CUDA", capable: true, want: true,
		},
		{
			name: "auto local metal is the measured failure",
			mode: QwenStreamAuto, backend: "metal", capable: true, want: false,
		},
		{
			name: "auto local cpu is unmeasured and fails closed",
			mode: QwenStreamAuto, backend: "cpu", capable: true, want: false,
		},
		{
			name: "auto local with no manifest to ask fails closed",
			mode: QwenStreamAuto, backend: "", capable: true, want: false,
		},
		{
			name: "on overrides a failing backend for an operator who asked",
			mode: QwenStreamOn, backend: "metal", capable: true, want: true,
		},
		{
			name: "off overrides the passing one",
			mode: QwenStreamOff, remote: true, capable: true, want: false,
		},
		{
			name: "an incapable worker cannot stream however it is configured",
			mode: QwenStreamOn, remote: true, backend: "cuda", capable: false, want: false,
		},
		{
			name: "an incapable local worker either",
			mode: QwenStreamAuto, backend: "cuda", capable: false, want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QwenStreamEligible(tc.mode, tc.remote, tc.backend, tc.capable); got != tc.want {
				t.Fatalf("QwenStreamEligible(%q, remote=%v, %q, %v) = %v, want %v",
					tc.mode, tc.remote, tc.backend, tc.capable, got, tc.want)
			}
		})
	}
}

func TestQwenTTSStreamSchemaRoundTrip(t *testing.T) {
	spec, ok := SpecFor("qwen_tts_stream")
	if !ok {
		t.Fatal("qwen_tts_stream has no KeySpec")
	}
	if spec.Type != TypeEnum || spec.Default != QwenStreamAuto {
		t.Fatalf("spec type=%v default=%v", spec.Type, spec.Default)
	}
	if got := strings.Join(spec.Enum, ","); got != QwenStreamAuto+","+QwenStreamOn+","+QwenStreamOff {
		t.Fatalf("enum=%q", got)
	}
	// A real default means "" is not a value the key can hold, so a front end
	// must not offer an empty choice for it.
	if spec.AllowsEmpty {
		t.Error("qwen_tts_stream reports allows_empty for a key with a real default")
	}
}

// The Qwen config validator is where an unusable place is refused, and the
// streaming mode has to be refused in the same place or a typo reaches the
// provider as silent auto.
func TestValidateQwenTTSConfigRejectsAnUnknownStreamMode(t *testing.T) {
	cfg := &Config{QwenTTSBinary: "qwen3-tts-worker", QwenTTSModel: "/models", QwenTTSStream: "sometimes"}
	if err := ValidateQwenTTSConfig(cfg); err == nil {
		t.Fatal("ValidateQwenTTSConfig accepted qwen_tts_stream=sometimes")
	}
}

// The doctor row is how an operator learns why a reply speaks on the first
// chunk or on the finished sentence. It must answer without starting a worker.
func TestDiagnoseQwenStreaming(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        *Config
		wantDetail string
		wantSev    Severity
		wantNone   bool
	}{
		{
			name:     "kokoro has no chunks to play",
			cfg:      &Config{TTSProvider: "kokoro"},
			wantNone: true,
		},
		{
			name:       "lan is the measured offload target",
			cfg:        &Config{TTSProvider: "qwen3-tts", TTSPlace: TTSPlaceLAN},
			wantDetail: "chunks play as they arrive",
			wantSev:    SeverityOK,
		},
		{
			name:       "local with no package to ask stays whole-sentence",
			cfg:        &Config{TTSProvider: "qwen3-tts"},
			wantDetail: "whole sentences",
			wantSev:    SeverityOK,
		},
		{
			name:       "explicit on says so",
			cfg:        &Config{TTSProvider: "qwen3-tts", QwenTTSStream: QwenStreamOn},
			wantDetail: "set explicitly",
			wantSev:    SeverityOK,
		},
		{
			name:       "an unusable mode is an error, not a silent auto",
			cfg:        &Config{TTSProvider: "qwen3-tts", QwenTTSStream: "maybe"},
			wantDetail: "unsupported qwen_tts_stream",
			wantSev:    SeverityError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := DiagnoseQwenStreaming(tc.cfg, t.TempDir())
			if tc.wantNone {
				if len(diags) != 0 {
					t.Fatalf("got %d rows for a provider with no streaming to report", len(diags))
				}
				return
			}
			if len(diags) != 1 {
				t.Fatalf("got %d rows, want 1", len(diags))
			}
			if diags[0].Name != "qwen3-tts-streaming" {
				t.Fatalf("row name = %q", diags[0].Name)
			}
			if diags[0].Severity != tc.wantSev {
				t.Fatalf("severity = %v, want %v", diags[0].Severity, tc.wantSev)
			}
			if !strings.Contains(diags[0].Detail, tc.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", diags[0].Detail, tc.wantDetail)
			}
		})
	}
}

// The eligibility seam takes a bool, not a place string. This is the test that
// would have caught the original cross-package coincidence: the production
// caller's notion of "remote" is tts.PlaceLAN, a constant provider.go keeps
// deliberately separate from config's, and comparing the two strings was only
// correct until someone renamed one. A bool argument cannot be renamed apart.
func TestQwenStreamEligibleTakesRemoteNotAPlaceString(t *testing.T) {
	fn := reflect.TypeOf(QwenStreamEligible)
	if got := fn.In(1).Kind(); got != reflect.Bool {
		t.Fatalf("QwenStreamEligible's second parameter is %v; a place string here can be silently "+
			"renamed out of agreement with the caller's own constant", got)
	}
	// And it must actually branch on it.
	if !QwenStreamEligible(QwenStreamAuto, true, "", true) {
		t.Error("remote=true did not stream under auto")
	}
	if QwenStreamEligible(QwenStreamAuto, false, "", true) {
		t.Error("remote=false streamed under auto with no CUDA package")
	}
}

// An install that predates backend_hint must not read as a missing install:
// the two need different fixes, and the wrong one sends the operator to
// reinstall something that is already there.
func TestDiagnoseQwenStreamingSeparatesAStaleManifestFromNoInstall(t *testing.T) {
	diags := DiagnoseQwenStreaming(&Config{TTSProvider: "qwen3-tts"}, t.TempDir())
	if len(diags) != 1 {
		t.Fatalf("got %d rows", len(diags))
	}
	if !strings.Contains(diags[0].Detail, "no installed package") {
		t.Fatalf("detail = %q, want it to name the missing install", diags[0].Detail)
	}
}

// The real thing writeFakeQwenNativeTar produces has no backend_hint — it is
// the shape every package installed before this field existed still has on
// disk. That must read as "stale manifest", not as "nothing installed".
func TestDiagnoseQwenStreamingNamesAStaleManifest(t *testing.T) {
	modelsDir := t.TempDir()
	cfg := &Config{
		TTSProvider:      "qwen3-tts",
		ModelsDir:        modelsDir,
		QwenTTSModelTier: "0.6b",
		QwenTTSNativeURL: writeFakeQwenNativeTar(t, t.TempDir(), "0.6b"),
	}
	if err := EnsureQwenTTSTier(context.Background(), cfg, "0.6b", nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if st := qwen.InspectNative(modelsDir, "0.6b"); !st.Installed || st.BackendHint != "" {
		t.Fatalf("fixture is not the pre-backend_hint shape: installed=%v hint=%q", st.Installed, st.BackendHint)
	}

	diags := DiagnoseQwenStreaming(cfg, modelsDir)
	if len(diags) != 1 {
		t.Fatalf("got %d rows", len(diags))
	}
	if !strings.Contains(diags[0].Detail, "predates the backend field") {
		t.Errorf("detail = %q, want it to name the stale manifest", diags[0].Detail)
	}
	if strings.Contains(diags[0].Detail, "no installed package") {
		t.Error("an installed package was reported as missing")
	}
	if !strings.Contains(diags[0].Remediation, "models ensure") {
		t.Errorf("remediation = %q, want it to point at a reinstall", diags[0].Remediation)
	}
	// Whole sentences either way: an unknown backend is not an eligible one.
	if !strings.Contains(diags[0].Detail, "whole sentences") {
		t.Errorf("detail = %q, want streaming off for an unknown backend", diags[0].Detail)
	}
}
