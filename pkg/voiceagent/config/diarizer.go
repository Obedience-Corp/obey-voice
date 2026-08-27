package config

import (
	"fmt"
	"strings"
)

// Meeting-review diarization engines for speaker.meeting.diarizer.
//
// This key chooses which model produces the offline review timeline that
// meeting labels are read from (docs/meetings.md). Live provisional labels are
// a separate embedding path and are unaffected by it.
const (
	// DiarizerSherpa is the shipped default: pyannote segmentation 3.0 plus
	// NeMo TitaNet, in process through sherpa-onnx.
	DiarizerSherpa = "sherpa"
	// DiarizerSortformer runs NVIDIA Streaming Sortformer through the
	// transcribe.cpp CLI. Optional; measured in
	// docs/benchmarks/T3-diar-ab-sortformer-2026-08-26.json.
	DiarizerSortformer = "sortformer"
)

// DefaultMeetingDiarizer is what an install that never set the key uses.
const DefaultMeetingDiarizer = DiarizerSherpa

// MeetingDiarizers lists the selectable values, sorted for the schema enum.
func MeetingDiarizers() []string { return []string{DiarizerSherpa, DiarizerSortformer} }

// NormalizeMeetingDiarizer resolves a speaker.meeting.diarizer value. Empty
// selects the default; an unrecognized value is an error rather than a silent
// fall back, because a typo must never decide which model labels a meeting.
func NormalizeMeetingDiarizer(value string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(value)); v {
	case "":
		return DefaultMeetingDiarizer, nil
	case DiarizerSherpa, DiarizerSortformer:
		return v, nil
	default:
		return "", fmt.Errorf("unsupported speaker.meeting.diarizer %q; choose one of %s",
			value, strings.Join(MeetingDiarizers(), ", "))
	}
}

// MeetingDiarizerFrom resolves the configured engine. A nil config is the
// default, matching how an absent key reads.
func MeetingDiarizerFrom(cfg *Config) (string, error) {
	if cfg == nil {
		return DefaultMeetingDiarizer, nil
	}
	return NormalizeMeetingDiarizer(cfg.Speaker.Meeting.Diarizer)
}

// diagnoseMeetingDiarizer reports the optional Sortformer review diarizer. It
// stays silent on the default sherpa path, the way the whisper.cpp and
// transcribe.cpp CLI rows appear only when their provider is selected.
func diagnoseMeetingDiarizer(cfg *Config, modelsDir string, lookPath func(string) (string, error)) []Diagnostic {
	selected, err := MeetingDiarizerFrom(cfg)
	if err != nil {
		return []Diagnostic{{
			Name:        "meeting-diarizer",
			Severity:    SeverityError,
			Detail:      err.Error(),
			Remediation: "set speaker.meeting.diarizer to " + strings.Join(MeetingDiarizers(), " or "),
		}}
	}
	if selected != DiarizerSortformer {
		return nil
	}

	asset := SortformerDiarizerAsset()
	diags := []Diagnostic{diagnoseTranscribeCLI(transcribeCLICheck{
		name:   "sortformer-diarizer",
		binary: cfg.TranscribeCPPBinary,
		model:  func() (string, error) { return asset.ModelPath(modelsDir), nil },
		// Sortformer on CPU is still 2.6x the sherpa review path it replaces
		// (T3), so unlike the STT provider a CPU-only machine is usable.
		requireGPU:    false,
		missingRemedy: "run 'obey-voice models ensure' to download the Sortformer weights",
	}, lookPath)}

	// Sortformer takes no speaker count. Saying so beats silently ignoring a
	// setting the user deliberately changed.
	if cfg.Speaker.Meeting.NumSpeakers > 0 {
		diags = append(diags, Diagnostic{
			Name:     "sortformer-expected-speakers",
			Severity: SeverityWarn,
			Detail: fmt.Sprintf("expected speakers is %d, but Sortformer has no speaker-count input; it resolves up to %d on its own",
				cfg.Speaker.Meeting.NumSpeakers, SortformerMaxSpeakers),
			Remediation: "set speaker.meeting.num_speakers 0 to match what runs, or keep it for a switch back to the sherpa diarizer",
		})
	}
	return diags
}
