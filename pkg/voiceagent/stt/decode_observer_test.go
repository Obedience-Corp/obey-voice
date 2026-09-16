package stt

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
)

func decodeEvidenceSamples() []float32 {
	samples := longSpeech()
	// Preserve unusual finite float bits as well as audible speech.
	samples[0] = math.Float32frombits(0x80000000)
	samples[1] = math.Float32frombits(1)
	samples[2] = math.Float32frombits(0xbdcccccd)
	return samples
}

func runDecodeEvidenceLoop(ctx context.Context, preview bool, samples []float32, transcribe transcribeFunc) []Event {
	deps := offlineLoopDeps{
		frames: &scriptedFrames{chunks: [][]float32{speechFrame()}},
		seg:    &fakeSegmenter{speech: true, segments: [][]float32{samples}}, transcribe: transcribe,
	}
	events := make(chan Event, 64)
	if preview {
		runPreviewLoop(ctx, deps, events)
	} else {
		runOfflineLoop(ctx, deps, events)
	}
	close(events)
	var got []Event
	for event := range events {
		got = append(got, event)
	}
	return got
}

func sameFloatBits(a, b []float32) bool {
	return slices.EqualFunc(a, b, func(x, y float32) bool { return math.Float32bits(x) == math.Float32bits(y) })
}

func TestDecodeObserverExactInputAndOrderInBothLoops(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "preview"}[preview], func(t *testing.T) {
			want := decodeEvidenceSamples()
			var order []string
			var actualInput []float32
			parent, cancel := context.WithCancel(t.Context())
			ctx := WithDecodeObserver(parent, func(ctx context.Context, o DecodeObservation) error {
				order = append(order, "observe")
				if ctx.Err() != nil || !sameFloatBits(o.Samples, want) || &o.Samples[0] == &actualInput[0] {
					t.Fatal("observer did not receive an independent exact decoder input snapshot and derived context")
				}
				if o.Text != "Wait." || o.Err != nil || o.StartedAt.IsZero() || o.CompletedAt.Before(o.StartedAt) {
					t.Fatalf("invalid decode observation: %+v", o)
				}
				return nil
			})
			cancel()
			// Completion contexts intentionally remove cancellation while keeping
			// their values; the opt-in observer must survive that operation.
			ctx = context.WithoutCancel(ctx)
			got := runDecodeEvidenceLoop(ctx, preview, want, func(samples []float32) (string, error) {
				order = append(order, "decode")
				actualInput = samples
				if !sameFloatBits(samples, want) {
					t.Fatal("loop changed decoder samples")
				}
				return "Wait.", nil
			})
			order = append(order, "returned")
			if !slices.Equal(order, []string{"decode", "observe", "returned"}) {
				t.Fatalf("decode/observer order = %v", order)
			}
			final, ok := terminal(got).(FinalTranscript)
			if !ok || final.Text != "Wait." {
				t.Fatalf("terminal = %#v", terminal(got))
			}
		})
	}
}

func TestDecodeObserverSnapshotsBeforeMutatingDecoder(t *testing.T) {
	samples := decodeEvidenceSamples()
	want := slices.Clone(samples)
	observed := false
	ctx := WithDecodeObserver(t.Context(), func(_ context.Context, o DecodeObservation) error {
		observed = true
		if !sameFloatBits(o.Samples, want) || &o.Samples[0] == &samples[0] {
			t.Fatal("decoder mutation altered submitted-input evidence")
		}
		return nil
	})
	decode := observedTranscribe(ctx, func(input []float32) (string, error) {
		if &input[0] != &samples[0] || !sameFloatBits(input, want) {
			t.Fatal("actual decoder did not receive the original input")
		}
		clear(input)
		return "Wait.", nil
	})
	text, err := decode(samples)
	if !observed || text != "Wait." || err != nil || samples[2] != 0 {
		t.Fatalf("observed=%v text=%q err=%v mutated sample=%g", observed, text, err, samples[2])
	}
}

func TestDecodeObserverFailuresFailBothLoops(t *testing.T) {
	decodeErr, writeErr := errors.New("decoder failed"), errors.New("disk full")
	for _, preview := range []bool{false, true} {
		for _, tc := range []struct {
			name                  string
			decodeErr, observeErr error
		}{{"observer", nil, writeErr}, {"decoder", decodeErr, nil}, {"both", decodeErr, writeErr}} {
			t.Run(map[bool]string{false: "offline/", true: "preview/"}[preview]+tc.name, func(t *testing.T) {
				calls := 0
				ctx := WithDecodeObserver(t.Context(), func(_ context.Context, o DecodeObservation) error {
					calls++
					if o.Err != tc.decodeErr || o.Text != "partial result" || len(o.Samples) != minSpeechSamples {
						t.Fatalf("failed decoder evidence lost: %+v", o)
					}
					return tc.observeErr
				})
				got := runDecodeEvidenceLoop(ctx, preview, longSpeech(), func([]float32) (string, error) { return "partial result", tc.decodeErr })
				fail, ok := terminal(got).(Failure)
				if !ok || calls != 1 || (tc.decodeErr != nil && !errors.Is(fail.Err, tc.decodeErr)) || (tc.observeErr != nil && !errors.Is(fail.Err, tc.observeErr)) {
					t.Fatalf("terminal=%#v calls=%d", terminal(got), calls)
				}
				for _, event := range got {
					switch event.(type) {
					case FinalTranscript, PartialTranscript:
						t.Fatalf("failed evidence emitted a transcript: %#v", event)
					}
				}
			})
		}
	}
}

func TestDecodeObserverCancellationReachesObserverAndFailsSession(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "offline", true: "preview"}[preview], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			ctx = WithDecodeObserver(ctx, func(ctx context.Context, o DecodeObservation) error {
				calls++
				return ctx.Err()
			})
			got := runDecodeEvidenceLoop(ctx, preview, longSpeech(), func([]float32) (string, error) {
				cancel()
				return "Wait.", nil
			})
			fail, ok := terminal(got).(Failure)
			if !ok || calls != 1 || !errors.Is(fail.Err, context.Canceled) {
				t.Fatalf("canceled observer terminal=%#v calls=%d", terminal(got), calls)
			}
		})
	}
}

func TestDecodeObserverDisabledKeepsDirectCall(t *testing.T) {
	parent := WithDecodeObserver(t.Context(), func(context.Context, DecodeObservation) error {
		t.Fatal("disabled inherited observer called")
		return nil
	})
	decodeErr := errors.New("original error")
	samples := decodeEvidenceSamples()
	for _, ctx := range []context.Context{t.Context(), WithDecodeObserver(parent, nil)} {
		original := func(input []float32) (string, error) {
			if &input[0] != &samples[0] {
				t.Fatal("disabled path copied decoder input")
			}
			return "original text", decodeErr
		}
		var text string
		var err error
		allocs := testing.AllocsPerRun(10, func() { text, err = observedTranscribe(ctx, original)(samples) })
		if allocs != 0 || text != "original text" || err != decodeErr {
			t.Fatalf("disabled path allocs=%g text=%q err=%v", allocs, text, err)
		}
		got := runDecodeEvidenceLoop(ctx, false, longSpeech(), func([]float32) (string, error) { return "unchanged", nil })
		if final, ok := terminal(got).(FinalTranscript); !ok || final.Text != "unchanged" {
			t.Fatalf("nil observer changed loop result: %#v", terminal(got))
		}
	}
}
