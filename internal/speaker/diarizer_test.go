package speaker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// recordingDiarizer stands in for an injected review engine and records what
// the analyzer handed it. fixedCount makes it declare FixedSpeakerCount, the
// way SortformerDiarizer does.
type recordingDiarizer struct {
	calls       int
	numSpeakers int
	timeline    Timeline
	err         error
	fixedCount  bool
}

func (d *recordingDiarizer) Diarize(_ context.Context, _ []float32, numSpeakers int) (Timeline, error) {
	d.calls++
	d.numSpeakers = numSpeakers
	return d.timeline, d.err
}

func (d *recordingDiarizer) SpeakerCountIsFixed() bool { return d.fixedCount }

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

// sparseTimeline has one dominant label and one span far under the merge floors
// (2 s / 1% of speech mass), so MergeSparseLabels visibly renames it.
func sparseTimeline() Timeline {
	return Timeline{Observations: []Observation{
		{SegmentID: "a", StartMS: 0, EndMS: 300000, Label: "speaker-1", State: StateStable, Source: SourceRecording},
		{SegmentID: "b", StartMS: 300000, EndMS: 300500, Label: "speaker-2", State: StateStable, Source: SourceRecording},
	}}
}

// Expected speakers gates the sparse-label merge for a clusterer it can steer,
// and must reach nothing at all for a diarizer that resolves its own count —
// otherwise "the setting is ignored" would be false, and a count the model
// never saw would still be renaming observations.
func TestExpectedSpeakersOnlySteersASteerableDiarizer(t *testing.T) {
	tests := []struct {
		name        string
		fixedCount  bool
		numSpeakers int
		wantLabels  []string
	}{
		{
			name:        "steerable diarizer, auto: sparse labels merge as before",
			numSpeakers: 0, wantLabels: []string{"speaker-1"},
		},
		{
			name:        "steerable diarizer, fixed count: merge is skipped as before",
			numSpeakers: 2, wantLabels: []string{"speaker-1", "speaker-2"},
		},
		{
			name:       "fixed-count diarizer, auto: no merge, nothing to un-split",
			fixedCount: true, numSpeakers: 0, wantLabels: []string{"speaker-1", "speaker-2"},
		},
		{
			name:       "fixed-count diarizer, a count it never saw: same result",
			fixedCount: true, numSpeakers: 3, wantLabels: []string{"speaker-1", "speaker-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := meetingConfigWithDiarizer("")
			cfg.Meeting.NumSpeakers = tt.numSpeakers
			diarizer := &recordingDiarizer{timeline: sparseTimeline(), fixedCount: tt.fixedCount}

			analyzer, err := NewAnalyzerWithDiarizer(cfg, &FakeEngine{}, diarizer)
			if err != nil {
				t.Fatalf("NewAnalyzerWithDiarizer() error = %v", err)
			}
			defer func() { _ = analyzer.Close() }()

			timeline, err := analyzer.Finalize(context.Background(), make([]float32, 16000))
			if err != nil {
				t.Fatalf("Finalize() error = %v", err)
			}
			got := SortedSpeakerLabels(timeline)
			if strings.Join(got, ",") != strings.Join(tt.wantLabels, ",") {
				t.Fatalf("labels = %v, want %v", got, tt.wantLabels)
			}
		})
	}
}

// The claim the doctor and docs make rests on this: the shipped Sortformer
// diarizer really does declare its count fixed.
func TestSortformerDeclaresAFixedSpeakerCount(t *testing.T) {
	var d Diarizer = &SortformerDiarizer{}
	if !speakerCountIsFixed(d) {
		t.Fatal("SortformerDiarizer must declare FixedSpeakerCount; expected speakers would otherwise still gate the merge")
	}
	// The sherpa engine keeps today's behaviour: steerable.
	if speakerCountIsFixed(&FakeEngine{}) {
		t.Fatal("an engine without the interface must stay steerable")
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
