package stt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// DecodeObservation is one actual offline decoder invocation, including live
// preview decodes. Samples contains the exact float32 input, not capture audio
// or a reconstructed VAD interval. It is snapshotted before decoding so an
// adapter's input mutations cannot change the evidence. The snapshot is
// read-only and valid only during the observer call; observers retaining it
// must copy it.
type DecodeObservation struct {
	Samples                []float32
	Text                   string
	Err                    error
	StartedAt, CompletedAt time.Time
}

// DecodeObserver runs synchronously after decoding, before its buffer can be
// reused or its result emitted. Returning an error fails the STT session so an
// evidence recording failure cannot silently become a successful transcript.
type DecodeObserver func(context.Context, DecodeObservation) error

type decodeObserverKey struct{}

// WithDecodeObserver enables observation of offline decoder calls in this
// context. A nil observer disables observation, including an inherited one.
// Streaming recognizers do not use this hook. The observer must honor context
// cancellation and must not mutate the borrowed samples.
func WithDecodeObserver(ctx context.Context, observer DecodeObserver) context.Context {
	return context.WithValue(ctx, decodeObserverKey{}, observer)
}

func observedTranscribe(ctx context.Context, transcribe transcribeFunc) transcribeFunc {
	observer, _ := ctx.Value(decodeObserverKey{}).(DecodeObserver)
	if observer == nil {
		return transcribe
	}
	return func(samples []float32) (string, error) {
		snapshot := slices.Clone(samples)
		started := time.Now()
		text, err := transcribe(samples)
		observation := DecodeObservation{Samples: snapshot, Text: text, Err: err, StartedAt: started, CompletedAt: time.Now()}
		if observeErr := observer(ctx, observation); observeErr != nil {
			evidenceErr := fmt.Errorf("record offline decode evidence: %w", observeErr)
			if err != nil {
				return text, errors.Join(err, evidenceErr)
			}
			return text, evidenceErr
		}
		return text, err
	}
}
