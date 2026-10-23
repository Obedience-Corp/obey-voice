//go:build integration

package openaispeech_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	ttsproviders "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/providers"
)

// TestOpenAISpeechVsKokoroRealtime measures both engines through the real
// provider seam (ttsproviders.NewProvider on the live config, read-only): time
// to the first PCM frame and whole-utterance synthesis against the audio
// produced. No playback device is opened. It mirrors TestKokoroRealtimeFactor
// so the numbers are comparable with N7.
//
//	go test -tags integration ./pkg/voiceagent/tts/openaispeech/ -run TestOpenAISpeechVsKokoroRealtime -v
//
// OPENAI_SPEECH_AB_OUT=path writes the per-case medians as JSON.
func TestOpenAISpeechVsKokoroRealtime(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no config: %v", err)
	}
	cfg.SpeechSpeed = 0.95

	cases := []struct{ name, text string }{
		{"one sentence", "It continues here."},
		{"two sentences", "It continues here. Then a third thought arrives."},
		{"long reply", "Sure. I moved the meeting notes into the campaign folder and the summary is ready whenever you want it. The two action items are assigned, and I flagged the budget question for Thursday."},
	}
	type row struct {
		Engine, Case                   string
		FirstFrameMS, SynthMS, AudioMS float64
		RTF                            float64
		// LeadSilenceMS/TailSilenceMS count samples under silenceThreshold at
		// the start and end of the utterance. A per-request pad that does not
		// shrink with length is dead air between sentences, not pacing.
		LeadSilenceMS, TailSilenceMS float64
		Runs                         int
	}
	var rows []row
	for _, engine := range []string{"kokoro", config.OpenAISpeechProviderName} {
		c := *cfg
		c.TTSProvider = engine
		if engine == "kokoro" {
			c.TTSVoice = "af_heart"
		}
		p, cleanup, err := ttsproviders.NewProvider(&c)
		if err != nil {
			t.Skipf("%s unavailable: %v", engine, err)
		}
		// Warm: model load is not charged to a case.
		if s, err := p.Synthesize(context.Background(), "warm up"); err == nil {
			for range s.Frames() {
			}
		}
		for _, tc := range cases {
			const runs = 5
			var firsts, synths []float64
			var audioMS, leadMS, tailMS float64
			for range runs {
				start := time.Now()
				stream, err := p.Synthesize(context.Background(), tc.text)
				if err != nil {
					t.Fatalf("%s %s: %v", engine, tc.name, err)
				}
				rate, err := stream.WaitReady(context.Background())
				if err != nil {
					t.Fatalf("%s %s: WaitReady: %v", engine, tc.name, err)
				}
				samples, first := 0, time.Duration(0)
				var all []float32
				for f := range stream.Frames() {
					if samples == 0 {
						first = time.Since(start)
					}
					samples += len(f)
					all = append(all, f...)
				}
				if err := stream.Err(); err != nil {
					t.Fatalf("%s %s: %v", engine, tc.name, err)
				}
				synths = append(synths, float64(time.Since(start))/1e6)
				firsts = append(firsts, float64(first)/1e6)
				if rate > 0 {
					audioMS = float64(samples) / float64(rate) * 1000
					lead, tail := silenceEdges(all)
					leadMS = float64(lead) / float64(rate) * 1000
					tailMS = float64(tail) / float64(rate) * 1000
				}
			}
			r := row{Engine: engine, Case: tc.name, FirstFrameMS: median(firsts), SynthMS: median(synths), AudioMS: audioMS, LeadSilenceMS: leadMS, TailSilenceMS: tailMS, Runs: runs}
			if r.SynthMS > 0 {
				r.RTF = r.AudioMS / r.SynthMS
			}
			rows = append(rows, r)
			t.Logf("%-14s %-14s first frame %7.1f ms  synth %7.1f ms  audio %7.0f ms  %5.1fx realtime  lead %4.0f ms  tail %4.0f ms", engine, tc.name, r.FirstFrameMS, r.SynthMS, r.AudioMS, r.RTF, r.LeadSilenceMS, r.TailSilenceMS)
		}
		if d, ok := p.(tts.Describer); ok {
			t.Logf("%s model tier %q", engine, d.ModelTier())
		}
		cleanup()
	}
	if out := os.Getenv("OPENAI_SPEECH_AB_OUT"); out != "" {
		data, _ := json.MarshalIndent(rows, "", "  ")
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Println("wrote", out)
	}
}

// silenceThreshold is about -54 dBFS: below room noise, above codec dither.
const silenceThreshold = 0.002

// silenceEdges counts the sub-threshold samples before the first and after the
// last audible sample.
func silenceEdges(samples []float32) (lead, tail int) {
	first, last := -1, -1
	for i, s := range samples {
		if s > silenceThreshold || s < -silenceThreshold {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return len(samples), 0
	}
	return first, len(samples) - 1 - last
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}
