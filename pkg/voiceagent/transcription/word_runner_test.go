package transcription

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

type rawWordDecoder struct {
	words  []stt.TimedWord
	cancel context.CancelFunc
}

func (d *rawWordDecoder) DecodeSegment(context.Context, []float32) (string, error) {
	panic("corrected decoder used")
}
func (d *rawWordDecoder) DecodeWords(ctx context.Context, _ []float32) (string, []stt.TimedWord, error) {
	if d.cancel != nil {
		d.cancel()
		return "", nil, ctx.Err()
	}
	return "Um, yes.", append([]stt.TimedWord(nil), d.words...), nil
}
func TestWordRunnerOffsetsNativeIntervalsAndRejectsInvalidOrCancelledResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		end    int64
		cancel bool
		want   string
	}{
		{"source relative", 300000, false, ""}, {"outside span", 2000000, false, "invalid decoder word interval"}, {"cancelled", 300000, true, "cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pcm := make([]float32, 32000)
			for i := range pcm {
				pcm[i] = .01
			}
			source := &fakeSource{chunks: [][]float32{pcm}, total: 32000}
			detector := &fakeDetector{spans: []Span{{StartSample: 16000, EndSample: 32000, Samples: pcm[:16000]}}}
			decoder := &rawWordDecoder{words: []stt.TimedWord{{Text: "Um,", StartUS: 100000, EndUS: tc.end}, {Text: "yes.", StartUS: 400000, EndUS: 600000}}}
			if tc.cancel {
				decoder.cancel = cancel
			}
			sink := &fakeSink{}
			_, err := Run(ctx, Request{Timing: stt.TimingWord}, Deps{Source: source, Detector: detector, Decoder: decoder, Sink: sink})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("wanted %s, got %v", tc.want, err)
				}
				if tc.cancel && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if len(sink.segments()) != 0 {
					t.Fatal("invalid result escaped")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := sink.segments()
			if len(got) != 1 || got[0].Words[0].StartUS != 1100000 || got[0].Words[0].EndUS != 1300000 || got[0].Text != "Um, yes." {
				t.Fatalf("%+v", got)
			}
		})
	}
}
