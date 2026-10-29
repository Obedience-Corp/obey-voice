package stt

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const WordTimingMethod = "parakeet-tdt-token-duration"

// TimedWord is raw recognizer text and a measured interval relative to the
// supplied PCM. These are model estimates, not qualified automatic cut points.
// Confidence is absent: token log probabilities are not word confidence.
type TimedWord struct {
	Text           string
	StartUS, EndUS int64
}

// WordDecoder is deliberately separate from corrected conversational decoding.
type WordDecoder interface {
	DecodeWords(context.Context, []float32) (string, []TimedWord, error)
}

// NewWordDecoder reuses the native decoder but never installs correction stages
// or acoustic-name rewriting. Unsupported providers fail before model creation.
func NewWordDecoder(cfg *config.Config) (SegmentDecoder, DecoderInfo, func(), error) {
	norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	if err != nil || norm.Provider != config.STTProviderParakeet || (cfg.ParakeetModel != "" && cfg.ParakeetModel != "tdt-0.6b-v2") {
		return nil, DecoderInfo{}, nil, fmt.Errorf("verbatim word timing requires parakeet tdt-0.6b-v2: %w", ErrSegmentDecodeUnsupported)
	}
	raw := *cfg
	raw.AcousticNameEnabled = false
	p, err := NewParakeetSTT(&raw, nil, nil)
	if err != nil {
		return nil, DecoderInfo{}, nil, err
	}
	info := DecoderInfo{Provider: norm.Provider, Mode: norm.Mode,
		ModelID: p.model.Name, ModelRevision: p.model.ArchiveSHA256,
		Languages: modelLanguages(p.model.EnglishOnly(), &raw), Timing: []string{TimingWord}}
	return p, info, p.Delete, nil
}

// tokenWords groups the decoder's space-prefixed tokens, retaining the exact
// text. Punctuation contributes text but does not extend an acoustic word edge.
// Missing, unordered and invalid measured durations are errors, never guesses.
func tokenWords(text string, tokens []string, starts, durations []float32, sampleCount int) ([]TimedWord, error) {
	if len(tokens) != len(starts) || len(tokens) != len(durations) {
		return nil, fmt.Errorf("decoder did not return a start and duration for every token")
	}
	if strings.Join(strings.Fields(strings.Join(tokens, "")), "") != strings.Join(strings.Fields(text), "") {
		return nil, fmt.Errorf("decoder tokens do not match raw text")
	}
	var words []TimedWord
	var current TimedWord
	measured := false
	flush := func() error {
		if current.Text == "" {
			return nil
		}
		if !measured || current.EndUS <= current.StartUS {
			return fmt.Errorf("word %q has no measured interval", current.Text)
		}
		if len(words) > 0 && current.StartUS < words[len(words)-1].EndUS {
			return fmt.Errorf("word %q starts at %d us before predecessor ends at %d us", current.Text, current.StartUS, words[len(words)-1].EndUS)
		}
		words = append(words, current)
		current, measured = TimedWord{}, false
		return nil
	}
	last := float64(-1)
	limit := float64(sampleCount) / audio.SampleRate
	for i, token := range tokens {
		start, duration := float64(starts[i]), float64(durations[i])
		if math.IsNaN(start) || math.IsNaN(duration) || math.IsInf(start, 0) || math.IsInf(duration, 0) || start < 0 || duration < 0 || start < last {
			return nil, fmt.Errorf("invalid timing at token %d", i)
		}
		last = start
		trimmed := strings.TrimSpace(token)
		if token != strings.TrimLeftFunc(token, unicode.IsSpace) && current.Text != "" {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		if trimmed == "" {
			continue
		}
		if len(strings.Fields(trimmed)) != 1 {
			return nil, fmt.Errorf("token contains multiple words")
		}
		current.Text += trimmed
		lexical := strings.ContainsFunc(token, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) })
		if lexical {
			end := start + duration
			// float32 decoder arithmetic can differ by less than a sample.
			if end > limit+1.0/audio.SampleRate {
				return nil, fmt.Errorf("token %d ends outside PCM", i)
			}
			// Canonicalize floating decoder times to the PCM sample clock.
			// Adjacent frame boundaries can otherwise differ by a microsecond
			// after float32 addition. This does not repair real overlaps.
			beginSample := int64(math.Round(start * audio.SampleRate))
			endSample := min(int64(math.Round(end*audio.SampleRate)), int64(sampleCount))
			beginUS := int64(math.Round(float64(beginSample) * 1e6 / audio.SampleRate))
			endUS := int64(math.Round(float64(endSample) * 1e6 / audio.SampleRate))
			if !measured {
				current.StartUS = beginUS
				measured = true
			}
			current.EndUS = max(current.EndUS, endUS)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return words, nil
}
