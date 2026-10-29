package stt

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

type stubDecoder struct {
	text string
	err  error
}

func (s *stubDecoder) DecodeSegment(context.Context, []float32) (string, error) {
	return s.text, s.err
}

// recordingBuilders reports which branch the factory took without constructing
// a recognizer, so selection is testable with no model files on disk.
func recordingBuilders(chosen *string, decoder SegmentDecoder, cleanup func()) decoderBuilders {
	build := func(name string) func(*config.Config) (SegmentDecoder, func(), error) {
		return func(*config.Config) (SegmentDecoder, func(), error) {
			*chosen = name
			return decoder, cleanup, nil
		}
	}
	return decoderBuilders{
		parakeet:      build("parakeet"),
		sherpaWhisper: build("sherpa-whisper"),
		whisperCPP:    build("whispercpp"),
		transcribeCPP: build("transcribecpp"),
	}
}

func TestNewSegmentDecoderSelection(t *testing.T) {
	cases := []struct {
		name          string
		cfg           config.Config
		wantBranch    string
		wantProvider  string
		wantMode      string
		wantModelID   string
		wantRevision  string
		wantLanguages []string
		wantErr       string
	}{
		{
			name:          "default config is parakeet",
			cfg:           config.Config{},
			wantBranch:    "parakeet",
			wantProvider:  config.STTProviderParakeet,
			wantMode:      config.STTModeOffline,
			wantModelID:   config.DefaultParakeetModel,
			wantRevision:  "157c157bc51155e03e37d2466522a3a737dd9c72bb25f36eb18912964161e1ad",
			wantLanguages: []string{"en"},
		},
		{
			name:          "multilingual parakeet reports the configured language",
			cfg:           config.Config{STTProvider: "sherpa-parakeet", ParakeetModel: "tdt-0.6b-v3", Language: "fr-FR"},
			wantBranch:    "parakeet",
			wantProvider:  config.STTProviderParakeet,
			wantMode:      config.STTModeOffline,
			wantModelID:   "tdt-0.6b-v3",
			wantRevision:  "5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf",
			wantLanguages: []string{"fr"},
		},
		{
			name:          "sherpa whisper",
			cfg:           config.Config{STTProvider: "sherpa"},
			wantBranch:    "sherpa-whisper",
			wantProvider:  config.STTProviderSherpa,
			wantMode:      config.STTModeOffline,
			wantModelID:   "small",
			wantLanguages: []string{"en"},
		},
		{
			name:          "whispercpp",
			cfg:           config.Config{STTProvider: "whispercpp"},
			wantBranch:    "whispercpp",
			wantProvider:  config.STTProviderWhisperCPP,
			wantMode:      config.STTModeCLI,
			wantModelID:   "whispercpp-base.en",
			wantRevision:  "a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002",
			wantLanguages: []string{"en"},
		},
		{
			name:          "transcribecpp",
			cfg:           config.Config{STTProvider: "transcribecpp"},
			wantBranch:    "transcribecpp",
			wantProvider:  config.STTProviderTranscribeCPP,
			wantMode:      config.STTModeOffline,
			wantModelID:   config.DefaultTranscribeCPPModel,
			wantRevision:  "f0d0e99cebb6d3b83f1f7069b82b5d3c2e39a54545b0da039cb4bafd9c4e5caa",
			wantLanguages: []string{"en"},
		},
		{
			name:    "streaming cannot decode a finite span",
			cfg:     config.Config{STTProvider: "sherpa-streaming"},
			wantErr: "cannot decode finite segments",
		},
		{
			name:    "unknown provider",
			cfg:     config.Config{STTProvider: "google"},
			wantErr: "unsupported stt_provider",
		},
		{
			name:    "mode the provider does not have",
			cfg:     config.Config{STTProvider: "whispercpp", STTMode: "streaming"},
			wantErr: "is not supported by stt_provider",
		},
		{
			name:    "unknown parakeet model",
			cfg:     config.Config{ParakeetModel: "tdt-9b"},
			wantErr: "unsupported parakeet_model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var chosen string
			decoder, info, cleanup, err := newSegmentDecoder(&tc.cfg, recordingBuilders(&chosen, &stubDecoder{}, nil))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("newSegmentDecoder = %+v, want an error", info)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				if chosen != "" {
					t.Fatalf("the %s constructor ran for a request that must fail first", chosen)
				}
				if decoder != nil || cleanup != nil {
					t.Fatal("a failed construction returned a decoder or a cleanup")
				}
				return
			}
			if err != nil {
				t.Fatalf("newSegmentDecoder: %v", err)
			}
			defer cleanup()
			if chosen != tc.wantBranch {
				t.Errorf("built %q, want %q", chosen, tc.wantBranch)
			}
			if info.Provider != tc.wantProvider || info.Mode != tc.wantMode {
				t.Errorf("info provider/mode = %q/%q, want %q/%q", info.Provider, info.Mode, tc.wantProvider, tc.wantMode)
			}
			if info.ModelID != tc.wantModelID {
				t.Errorf("info.ModelID = %q, want %q", info.ModelID, tc.wantModelID)
			}
			if info.ModelRevision != tc.wantRevision {
				t.Errorf("info.ModelRevision = %q, want %q", info.ModelRevision, tc.wantRevision)
			}
			if !slices.Equal(info.Languages, tc.wantLanguages) {
				t.Errorf("info.Languages = %v, want %v", info.Languages, tc.wantLanguages)
			}
			if !slices.Contains(info.Timing, TimingUtterance) {
				t.Errorf("info.Timing = %v, want it to contain %q", info.Timing, TimingUtterance)
			}
			if slices.Contains(info.Timing, TimingWord) {
				t.Errorf("info.Timing = %v, but no shipped provider reports word timing", info.Timing)
			}
		})
	}
}

// TestNewSegmentDecoderBuilderErrorIsPreserved locks the constructor-failure
// contract: the selected branch runs, the builder error is returned as-is, and
// the caller gets a nil decoder, a zero DecoderInfo, and a nil cleanup.
func TestNewSegmentDecoderBuilderErrorIsPreserved(t *testing.T) {
	buildErr := errors.New("injected builder failure")
	var chosen string
	track := func(name string, err error) func(*config.Config) (SegmentDecoder, func(), error) {
		return func(*config.Config) (SegmentDecoder, func(), error) {
			chosen = name
			return nil, nil, err
		}
	}

	decoder, info, cleanup, err := newSegmentDecoder(&config.Config{}, decoderBuilders{
		parakeet:      track("parakeet", buildErr),
		sherpaWhisper: track("sherpa-whisper", nil),
		whisperCPP:    track("whispercpp", nil),
		transcribeCPP: track("transcribecpp", nil),
	})
	if !errors.Is(err, buildErr) {
		t.Fatalf("error %v does not preserve the builder error", err)
	}
	if chosen != "parakeet" {
		t.Fatalf("built %q, want parakeet", chosen)
	}
	if decoder != nil || cleanup != nil {
		t.Fatal("a failed construction returned a decoder or a cleanup")
	}
	if !reflect.DeepEqual(info, DecoderInfo{}) {
		t.Fatalf("info = %+v, want zero value", info)
	}
}

func TestNewSegmentDecoderStreamingErrorIsMatchable(t *testing.T) {
	var chosen string
	_, _, _, err := newSegmentDecoder(&config.Config{STTProvider: "sherpa-streaming"}, recordingBuilders(&chosen, &stubDecoder{}, nil))
	if !errors.Is(err, ErrSegmentDecodeUnsupported) {
		t.Fatalf("error %v does not match ErrSegmentDecodeUnsupported", err)
	}
}

func TestNewSegmentDecoderAppliesConfiguredCorrection(t *testing.T) {
	stub := &stubDecoder{text: "um the meeting starts at thirty five past"}
	var chosen string
	cfg := config.Config{STTFillerRemoval: true, STTNumbers: true}

	decoder, _, cleanup, err := newSegmentDecoder(&cfg, recordingBuilders(&chosen, stub, nil))
	if err != nil {
		t.Fatalf("newSegmentDecoder: %v", err)
	}
	defer cleanup()

	got, err := decoder.DecodeSegment(t.Context(), make([]float32, 1600))
	if err != nil {
		t.Fatalf("DecodeSegment: %v", err)
	}
	if got == stub.text {
		t.Fatalf("DecodeSegment returned the raw text %q; the configured stages did not run", got)
	}
	if strings.Contains(got, "um ") {
		t.Errorf("DecodeSegment = %q, still carries the filler", got)
	}
	if !strings.Contains(got, "35") {
		t.Errorf("DecodeSegment = %q, want the numbers stage to have run", got)
	}
}

func TestNewSegmentDecoderWithoutStagesIsNotWrapped(t *testing.T) {
	stub := &stubDecoder{text: "unchanged"}
	var chosen string

	decoder, _, cleanup, err := newSegmentDecoder(&config.Config{}, recordingBuilders(&chosen, stub, nil))
	if err != nil {
		t.Fatalf("newSegmentDecoder: %v", err)
	}
	defer cleanup()
	if decoder != SegmentDecoder(stub) {
		t.Fatal("a config with no correction stages still wrapped the decoder")
	}
}

func TestNewSegmentDecoderCleanupRunsOnce(t *testing.T) {
	var deletes int
	var chosen string
	_, _, cleanup, err := newSegmentDecoder(&config.Config{}, recordingBuilders(&chosen, &stubDecoder{}, func() { deletes++ }))
	if err != nil {
		t.Fatalf("newSegmentDecoder: %v", err)
	}
	cleanup()
	cleanup()
	if deletes != 1 {
		t.Fatalf("cleanup ran %d times, want 1", deletes)
	}
}

func TestNewSegmentDecoderCleanupIsSafeWithoutOne(t *testing.T) {
	var chosen string
	_, _, cleanup, err := newSegmentDecoder(&config.Config{STTProvider: "whispercpp"}, recordingBuilders(&chosen, &stubDecoder{}, nil))
	if err != nil {
		t.Fatalf("newSegmentDecoder: %v", err)
	}
	cleanup() // a provider with nothing to free still returns a callable cleanup
}

// TestInProcessDecodeSegmentCanceledContextReturnsBeforeTranscribe proves
// ParakeetSTT and SherpaOfflineSTT observe cancellation at the DecodeSegment
// boundary. A zero-value receiver has a nil recognizer; returning without
// panic means transcribe was not entered and no model was loaded.
func TestInProcessDecodeSegmentCanceledContextReturnsBeforeTranscribe(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	samples := []float32{0.1, -0.1}

	for _, tc := range []struct {
		name    string
		decoder SegmentDecoder
	}{
		{name: "parakeet", decoder: new(ParakeetSTT)},
		{name: "sherpa", decoder: new(SherpaOfflineSTT)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := tc.decoder.DecodeSegment(ctx, samples)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("DecodeSegment = %v, want context.Canceled", err)
			}
			if text != "" {
				t.Fatalf("DecodeSegment text = %q, want empty", text)
			}
		})
	}
}

func TestCorrectedDecoderReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	decoder := &correctedDecoder{inner: &stubDecoder{text: "hello"}, chain: mustChain(t)}

	if _, err := decoder.DecodeSegment(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeSegment = %v, want context.Canceled", err)
	}
}

func mustChain(t *testing.T) correct.Chain {
	t.Helper()
	chain, _ := StagesFromConfig(&config.Config{STTFillerRemoval: true})
	if chain.Len() == 0 {
		t.Fatal("expected at least one correction stage")
	}
	return chain
}
