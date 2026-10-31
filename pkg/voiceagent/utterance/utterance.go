// Package utterance owns modality-neutral finalized input values. It does not
// initialize capture, decoders, models, conversation or playback resources.
package utterance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// These are persona V1 utterance limits, not limits on editor recordings.
// Finite editor jobs retain their independent full-coverage file/chunk limits.
const (
	SampleRate = 16000
	Channels   = 1
	MaxSamples = 30 * SampleRate
	MaxBytes   = 1 << 20
)

type State string

const (
	Pending     State = "pending"
	Ready       State = "ready"
	Unavailable State = "unavailable"
	Disabled    State = "disabled"
)

type Transcript struct {
	State    State  `json:"state"`
	Text     string `json:"text,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func (t Transcript) Validate() error {
	switch t.State {
	case Ready:
		if strings.TrimSpace(t.Text) == "" {
			return errors.New("ready transcript has no text")
		}
	case Pending, Unavailable, Disabled:
		if t.Text != "" {
			return errors.New("non-ready transcript must not contain text")
		}
	default:
		return fmt.Errorf("unknown transcript state %q", t.State)
	}
	return nil
}

var (
	ErrInvalidAudio     = errors.New("invalid utterance audio")
	ErrIncomplete       = errors.New("utterance capture is incomplete")
	ErrAudioUnavailable = errors.New("utterance audio is unavailable")
	ErrBudgetExceeded   = errors.New("session audio budget exceeded")
)

type AudioRef struct {
	ID          string `json:"id"`
	MIME        string `json:"mime"`
	SampleRate  int    `json:"sample_rate"`
	Channels    int    `json:"channels"`
	SampleCount int64  `json:"sample_count"`
}

func (r AudioRef) Validate() error {
	if len(r.ID) != 64 {
		return fmt.Errorf("audio hash length: %w", ErrInvalidAudio)
	}
	for _, c := range r.ID {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("audio hash encoding: %w", ErrInvalidAudio)
		}
	}
	if r.MIME != "audio/wav" || r.SampleRate != SampleRate || r.Channels != Channels || r.SampleCount < 1 || r.SampleCount > MaxSamples {
		return fmt.Errorf("expected nonempty 16 kHz mono audio of at most 30 seconds: %w", ErrInvalidAudio)
	}
	return nil
}

// Span preserves capture ordering without depending on a capture implementation.
// Unknown callback timestamps remain zero; they are not hardware timestamps.
type Span struct {
	SampleOffset        uint64    `json:"sample_offset"`
	SampleCount         int       `json:"sample_count"`
	DeviceGeneration    uint64    `json:"device_generation,omitempty"`
	CallbackAt          time.Time `json:"callback_at,omitempty"`
	EstimatedStart      time.Time `json:"estimated_start,omitempty"`
	DeviceDiscontinuity bool      `json:"device_discontinuity,omitempty"`
}

type Finalized struct {
	ID             string     `json:"id"`
	CaptureEpoch   uint64     `json:"capture_epoch"`
	Audio          *AudioRef  `json:"audio,omitempty"`
	Transcript     Transcript `json:"transcript"`
	SpeakerID      string     `json:"speaker_id,omitempty"`
	StartedAt      time.Time  `json:"started_at,omitempty"`
	EndedAt        time.Time  `json:"ended_at,omitempty"`
	EndpointReason string     `json:"endpoint_reason,omitempty"`
	Spans          []Span     `json:"spans,omitempty"`
	Incomplete     bool       `json:"incomplete,omitempty"`
}

func (f Finalized) Validate() error {
	if f.ID == "" || strings.ContainsAny(f.ID, "\x00\r\n") {
		return errors.New("utterance identity is required")
	}
	if f.Incomplete {
		return ErrIncomplete
	}
	if f.Audio == nil {
		return ErrAudioUnavailable
	}
	if err := f.Audio.Validate(); err != nil {
		return err
	}
	if err := f.Transcript.Validate(); err != nil {
		return err
	}
	if !f.StartedAt.IsZero() && !f.EndedAt.IsZero() && f.EndedAt.Before(f.StartedAt) {
		return fmt.Errorf("capture timestamps are reversed: %w", ErrIncomplete)
	}
	var count int64
	var next uint64
	var generation uint64
	for i, span := range f.Spans {
		if span.SampleCount < 1 || int64(span.SampleCount) > f.Audio.SampleCount-count || span.SampleOffset > math.MaxUint64-uint64(span.SampleCount) || span.DeviceDiscontinuity {
			return ErrIncomplete
		}
		if i > 0 && (span.SampleOffset != next || (generation != 0 && span.DeviceGeneration != 0 && span.DeviceGeneration != generation)) {
			return ErrIncomplete
		}
		count += int64(span.SampleCount)
		next = span.SampleOffset + uint64(span.SampleCount)
		if span.DeviceGeneration != 0 {
			generation = span.DeviceGeneration
		}
	}
	if len(f.Spans) > 0 && count != f.Audio.SampleCount {
		return ErrIncomplete
	}
	return nil
}

// Resolver loads only bytes authorized by the owning session. Implementations
// validate the reference/hash and enforce their memory/retention budget.
type Resolver interface {
	ResolveAudio(context.Context, AudioRef) ([]byte, error)
}
type Provider interface {
	Start(context.Context) (Session, error)
	Available() bool
}
type Session interface {
	Events() <-chan Event
	Close() error
}

type EventKind string

const (
	Phase          EventKind = "phase"
	Level          EventKind = "level"
	FinalizedEvent EventKind = "finalized"
	Timeout        EventKind = "timeout"
	Failure        EventKind = "failure"
)

type Event struct {
	Kind    EventKind
	Phase   string
	Elapsed time.Duration
	Level   float64
	Input   *Finalized
	Err     error
}

// Clone detaches mutable reference/span fields at asynchronous boundaries.
func (f *Finalized) Clone() *Finalized {
	if f == nil {
		return nil
	}
	out := *f
	if f.Audio != nil {
		ref := *f.Audio
		out.Audio = &ref
	}
	out.Spans = append([]Span(nil), f.Spans...)
	return &out
}
