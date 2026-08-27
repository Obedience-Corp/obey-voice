package speaker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// recordingDiarizer stands in for an injected review engine and records what
// the analyzer handed it.
type recordingDiarizer struct {
	calls       int
	numSpeakers int
	timeline    Timeline
	err         error
}

func (d *recordingDiarizer) Diarize(_ context.Context, _ []float32, numSpeakers int) (Timeline, error) {
	d.calls++
	d.numSpeakers = numSpeakers
	return d.timeline, d.err
}

func meetingConfigWithDiarizer(diarizer string) Config {
	cfg := Config{Enabled: true}
	cfg.Meeting.Enabled = true
	cfg.Meeting.Diarizer = diarizer
	return cfg
}

func TestMeetingDiarizerConfig(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantErr  string
		resolved string
	}{
		{name: "unknown value is refused", value: "pyannote", wantErr: "unsupported speaker.meeting.diarizer"},
		{name: "unset is the sherpa default", value: "", resolved: config.DiarizerSherpa},
		{name: "sherpa", value: "sherpa", resolved: config.DiarizerSherpa},
		{name: "sortformer", value: "sortformer", resolved: config.DiarizerSortformer},
		{name: "case and padding are tolerated", value: " Sortformer ", resolved: config.DiarizerSortformer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := meetingConfigWithDiarizer(tt.value)
			err := cfg.Validate()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Validate() error = %v, want it to mention %q", err, tt.wantErr)
				}
				// An unusable value must fail construction, not silently run
				// whichever engine happens to be compiled in.
				if _, err := NewAnalyzer(cfg, &FakeEngine{}); err == nil {
					t.Fatal("NewAnalyzer() error = nil, want the invalid diarizer to fail construction")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			got, err := cfg.MeetingDiarizer()
			if err != nil {
				t.Fatalf("MeetingDiarizer() error = %v", err)
			}
			if got != tt.resolved {
				t.Fatalf("MeetingDiarizer() = %q, want %q", got, tt.resolved)
			}
		})
	}
}

// The default install must reach the engine's own Diarize, so nothing about the
// shipped review path changes when no diarizer is injected.
func TestFinalizeUsesTheEngineByDefault(t *testing.T) {
	engine := &FakeEngine{}
	analyzer, err := NewAnalyzer(meetingConfigWithDiarizer(""), engine)
	if err != nil {
		t.Fatalf("NewAnalyzer() error = %v", err)
	}
	defer func() { _ = analyzer.Close() }()

	timeline, err := analyzer.Finalize(context.Background(), make([]float32, 16000))
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if len(timeline.Observations) == 0 {
		t.Fatal("Finalize() produced no observations; the engine path did not run")
	}
}

func TestFinalizeUsesTheInjectedDiarizer(t *testing.T) {
	engine := &FakeEngine{FailDiarize: fmt.Errorf("engine diarize must not run")}
	diarizer := &recordingDiarizer{timeline: Timeline{Observations: []Observation{{
		SegmentID: "sortformer-1", StartMS: 0, EndMS: 4000,
		Label: "speaker-1", State: StateStable, Source: SourceRecording,
	}}}}
	cfg := meetingConfigWithDiarizer(config.DiarizerSortformer)
	cfg.Meeting.NumSpeakers = 3

	analyzer, err := NewAnalyzerWithDiarizer(cfg, engine, diarizer)
	if err != nil {
		t.Fatalf("NewAnalyzerWithDiarizer() error = %v", err)
	}
	defer func() { _ = analyzer.Close() }()

	timeline, err := analyzer.Finalize(context.Background(), make([]float32, 16000))
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if diarizer.calls != 1 {
		t.Fatalf("diarizer calls = %d, want 1", diarizer.calls)
	}
	if diarizer.numSpeakers != 3 {
		t.Errorf("numSpeakers = %d, want the configured hint passed through unchanged", diarizer.numSpeakers)
	}
	if len(timeline.Observations) != 1 || timeline.Observations[0].SegmentID != "sortformer-1" {
		t.Fatalf("observations = %+v, want the injected timeline", timeline.Observations)
	}
	// Live identification is untouched by the review swap.
	obs, err := analyzer.IdentifySegment(context.Background(), Segment{
		ID: "live-1", Samples: make([]float32, minLiveEmbedSamples), Source: SourceLocalMic,
	})
	if err != nil {
		t.Fatalf("IdentifySegment() error = %v", err)
	}
	if obs.State != StateStable {
		t.Errorf("live observation state = %s, want the engine path to still run", obs.State)
	}
}
