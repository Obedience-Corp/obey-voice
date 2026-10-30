package stt

import (
	"context"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"math"
	"strings"
	"testing"
)

func TestTokenWordsPreserveFillersAndExcludePunctuationTiming(t *testing.T) {
	words, err := tokenWords("Um, I like it.", []string{" U", "m", ",", " I", " like", " it", "."}, []float32{0, .08, .24, .4, .6, .9, 1.1}, []float32{.08, .16, .16, .2, .3, .2, .1}, 32000)
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 4 || words[0].Text != "Um," || words[0].StartUS != 0 || words[0].EndUS != 240000 || words[3].EndUS != 1100000 {
		t.Fatalf("words = %+v", words)
	}
}
func TestTokenWordsRefuseInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, text        string
		tokens            []string
		starts, durations []float32
	}{
		{"missing end", "um", []string{" um"}, []float32{0}, nil},
		{"nan", "um", []string{" um"}, []float32{float32(math.NaN())}, []float32{.2}},
		{"negative duration", "um", []string{" um"}, []float32{0}, []float32{-.2}},
		{"out of bounds", "um", []string{" um"}, []float32{.9}, []float32{.2}},
		{"zero interval", "um", []string{" um"}, []float32{0}, []float32{0}},
		{"overlap", "um yes", []string{" um", " yes"}, []float32{0, .1}, []float32{.2, .2}},
		{"text mismatch", "yes", []string{" um"}, []float32{0}, []float32{.2}},
		{"backwards", "um yes", []string{" um", " yes"}, []float32{.4, .1}, []float32{.1, .1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tokenWords(tc.text, tc.tokens, tc.starts, tc.durations, 16000); err == nil {
				t.Fatal("accepted invalid word evidence")
			}
		})
	}
}
func TestWordDecoderRejectsUnsupportedProviderBeforeLoading(t *testing.T) {
	_, _, _, err := NewWordDecoder(&config.Config{STTProvider: "whispercpp"})
	if err == nil || !strings.Contains(err.Error(), "requires parakeet") {
		t.Fatal(err)
	}
}
func TestWordDecodeChecksCancellationBeforeNativeAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var decoder ParakeetSTT
	if _, _, err := decoder.DecodeWords(ctx, nil); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestTokenWordBreaksPreserveApostropheLedWord(t *testing.T) {
	words, err := tokenWords("is'cause", []string{" is", " ", "'", "cause"}, []float32{0, .2, .2, .2}, []float32{.2, 0, 0, .3}, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 2 || words[1].Text != "'cause" || words[1].StartUS != 200000 {
		t.Fatalf("%+v", words)
	}
}

// The native decoder uses float32 seconds. 11.6 can round to 11599999 us
// while the preceding token's start+duration rounds to 11600000 us.
func TestTokenWordsUsePCMClockForSharedFloatBoundary(t *testing.T) {
	words, err := tokenWords("I think", []string{" I", " think"}, []float32{11.52, 11.599999}, []float32{.08, .32}, 320000)
	if err != nil {
		t.Fatal(err)
	}
	if words[0].EndUS != words[1].StartUS || words[1].StartUS != 11600000 {
		t.Fatalf("%+v", words)
	}
}

func TestWordDecoderRejectsUnqualifiedModel(t *testing.T) {
	_, _, _, err := NewWordDecoder(&config.Config{STTProvider: "parakeet", ParakeetModel: "tdt-0.6b-v3"})
	if err == nil {
		t.Fatal("unqualified model advertised word mode")
	}
}
