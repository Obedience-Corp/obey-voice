package stt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

// Timing granularities a decoder can report for the text it returns.
const (
	// TimingUtterance is one interval per speech span. Every segment decoder
	// reports it.
	TimingUtterance = "utterance"
	// TimingWord is one interval per word. No shipped provider reports it; it
	// is named here so a capability list can say so.
	TimingWord = "word"
)

// SegmentDecoder decodes one finite span of audio to text. It is the seam the
// file transcription runner depends on: no session, no events, no capture, no
// endpointing. Implementations are safe to call sequentially.
type SegmentDecoder interface {
	DecodeSegment(ctx context.Context, samples []float32) (string, error)
}

// DecoderInfo describes the decoder a run actually resolved to. It reports what
// was selected, never the preference string that selected it.
type DecoderInfo struct {
	Provider      string   // normalized provider, e.g. "parakeet"
	Mode          string   // normalized mode, e.g. "offline"
	ModelID       string   // e.g. "tdt-0.6b-v2"
	ModelRevision string   // archive or file digest from the asset table, "" if none
	Languages     []string // what the selected model transcribes
	Timing        []string // always contains TimingUtterance
}

// ErrSegmentDecodeUnsupported is returned for a provider that cannot decode a
// finite span, such as a streaming recognizer that only consumes live audio.
var ErrSegmentDecodeUnsupported = errors.New("stt: provider cannot decode finite segments")

// The shipped utterance-final recognizers are segment decoders.
var (
	_ SegmentDecoder = (*ParakeetSTT)(nil)
	_ SegmentDecoder = (*SherpaOfflineSTT)(nil)
	_ SegmentDecoder = (*WhisperCPPSTT)(nil)
	_ SegmentDecoder = (*TranscribeCPPSTT)(nil)
)

// NewSegmentDecoder builds the configured provider as a segment decoder,
// together with what it resolved to and a cleanup to run when the caller is
// done. It selects with the same normalization as NewProvider and constructs
// with the same constructors, so there is one model registry, not two. The
// capture and VAD those constructors take are nil here: a file run drives the
// recognizer directly and never opens a microphone.
//
// Constructing is not free: two providers load models and transcribecpp spawns
// its CLI to enumerate compute lanes. A command that only reports capabilities
// must read the manifest and the spec table instead of calling this.
func NewSegmentDecoder(cfg *config.Config) (SegmentDecoder, DecoderInfo, func(), error) {
	return newSegmentDecoder(cfg, defaultDecoderBuilders())
}

// decoderBuilders is the construction seam. Tests inject builders so the
// selection, the info table and the correction wrapper can be exercised without
// model files on disk.
type decoderBuilders struct {
	parakeet      func(*config.Config) (SegmentDecoder, func(), error)
	sherpaWhisper func(*config.Config) (SegmentDecoder, func(), error)
	whisperCPP    func(*config.Config) (SegmentDecoder, func(), error)
	transcribeCPP func(*config.Config) (SegmentDecoder, func(), error)
}

func defaultDecoderBuilders() decoderBuilders {
	return decoderBuilders{
		parakeet: func(cfg *config.Config) (SegmentDecoder, func(), error) {
			p, err := NewParakeetSTT(cfg, nil, nil)
			if err != nil {
				return nil, nil, err
			}
			return p, p.Delete, nil
		},
		sherpaWhisper: func(cfg *config.Config) (SegmentDecoder, func(), error) {
			p, err := NewSherpaOfflineSTT(cfg, nil, nil)
			if err != nil {
				return nil, nil, err
			}
			return p, p.Delete, nil
		},
		whisperCPP: func(cfg *config.Config) (SegmentDecoder, func(), error) {
			p, err := NewWhisperCPPSTT(cfg, nil, nil)
			if err != nil {
				return nil, nil, err
			}
			return p, nil, nil
		},
		transcribeCPP: func(cfg *config.Config) (SegmentDecoder, func(), error) {
			p, err := NewTranscribeCPPSTT(cfg, nil, nil)
			if err != nil {
				return nil, nil, err
			}
			return p, nil, nil
		},
	}
}

// segmentDecodeSupported reports whether a normalized provider can decode a
// finite span. A streaming recognizer cannot: it consumes a live stream and
// endpoints itself.
func segmentDecodeSupported(norm config.NormalizedSTT) bool {
	return !(norm.Provider == config.STTProviderSherpa && norm.Mode == config.STTModeStreaming)
}

func newSegmentDecoder(cfg *config.Config, builders decoderBuilders) (SegmentDecoder, DecoderInfo, func(), error) {
	norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil {
		if _, ok := config.NormalizeSTT(cfg.STTProvider); !ok {
			return nil, DecoderInfo{}, nil, unsupportedProviderError(cfg.STTProvider)
		}
		return nil, DecoderInfo{}, nil, err
	}
	if !segmentDecodeSupported(norm) {
		return nil, DecoderInfo{}, nil, fmt.Errorf("stt: %s-%s: %w", norm.Provider, norm.Mode, ErrSegmentDecodeUnsupported)
	}

	info := DecoderInfo{Provider: norm.Provider, Mode: norm.Mode, Timing: []string{TimingUtterance}}
	var build func(*config.Config) (SegmentDecoder, func(), error)

	switch norm.Provider {
	case config.STTProviderParakeet:
		asset, err := config.ParakeetModel(cfg.ParakeetModel)
		if err != nil {
			return nil, DecoderInfo{}, nil, err
		}
		info.ModelID = asset.Name
		info.ModelRevision = asset.ArchiveSHA256
		info.Languages = modelLanguages(asset.EnglishOnly(), cfg)
		build = builders.parakeet
	case config.STTProviderSherpa:
		model, err := config.SherpaOfflineWhisperModel(cfg.WhisperModel)
		if err != nil {
			return nil, DecoderInfo{}, nil, err
		}
		info.ModelID = model
		info.Languages = modelLanguages(strings.HasSuffix(model, ".en"), cfg)
		build = builders.sherpaWhisper
	case config.STTProviderWhisperCPP:
		asset, err := config.WhisperCPPModelAsset(cfg.WhisperCPPModel)
		if err != nil {
			return nil, DecoderInfo{}, nil, err
		}
		info.ModelID = asset.Name
		info.ModelRevision = asset.SHA256
		info.Languages = modelLanguages(strings.Contains(asset.Filename, ".en."), cfg)
		build = builders.whisperCPP
	case config.STTProviderTranscribeCPP:
		asset, err := config.TranscribeCPPModel(cfg.TranscribeCPPModel)
		if err != nil {
			return nil, DecoderInfo{}, nil, err
		}
		info.ModelID = asset.Name
		info.ModelRevision = asset.SHA256
		info.Languages = modelLanguages(true, cfg)
		build = builders.transcribeCPP
	default:
		return nil, DecoderInfo{}, nil, unsupportedProviderError(cfg.STTProvider)
	}

	decoder, cleanup, err := build(cfg)
	if err != nil {
		return nil, DecoderInfo{}, nil, err
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	return withCorrectionStages(decoder, cfg), info, sync.OnceFunc(cleanup), nil
}

// modelLanguages reports what the selected model transcribes. An English-only
// model says so whatever the config asks for; a multilingual one reports the
// configured decode language, which is what it is asked to produce.
func modelLanguages(englishOnly bool, cfg *config.Config) []string {
	if englishOnly {
		return []string{"en"}
	}
	return []string{config.LanguageCode(cfg.Language)}
}

// withCorrectionStages runs decoded text through the same stages as the live
// path (StagesFromConfig), so a file transcript and a spoken turn spell names
// and numbers the same way.
func withCorrectionStages(decoder SegmentDecoder, cfg *config.Config) SegmentDecoder {
	chain, _ := StagesFromConfig(cfg)
	if chain.Len() == 0 {
		return decoder
	}
	return &correctedDecoder{inner: decoder, chain: chain}
}

type correctedDecoder struct {
	inner SegmentDecoder
	chain correct.Chain
}

// DecodeSegment implements SegmentDecoder. A stage failure keeps the raw
// transcript, matching the live wrapper; the caller's own cancellation is
// reported.
func (d *correctedDecoder) DecodeSegment(ctx context.Context, samples []float32) (string, error) {
	text, err := d.inner.DecodeSegment(ctx, samples)
	if err != nil || strings.TrimSpace(text) == "" {
		return text, err
	}
	corrected, correctErr := d.chain.Correct(ctx, text)
	if correctErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return text, nil
	}
	return corrected, nil
}
