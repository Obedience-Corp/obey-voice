package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// meetingSettings is a Meeting tab whose writes are recorded and whose PATH
// lookups are answered by the test, so the diarizer row never depends on what
// happens to be installed on the machine running it. Diarization starts on:
// that is the state where an install gap is worth reporting at all.
func meetingSettings(t *testing.T, diarizer string, installed ...string) (*settingsModel, map[string]any) {
	t.Helper()
	cfg := &config.Config{ModelsDir: t.TempDir()}
	cfg.Speaker.Enabled = true
	cfg.Speaker.Meeting.Enabled = true
	cfg.Speaker.Meeting.Diarizer = diarizer
	saved := map[string]any{}
	m := newSettings(cfg, nil)
	m.section = sectionMeeting
	m.width, m.height = 120, 24
	m.saveConfig = func(key string, value any) error {
		saved[key] = value
		return nil
	}
	m.lookPath = func(name string) (string, error) {
		if slices.Contains(installed, name) {
			return "/usr/local/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	return &m, saved
}

// installSortformerWeights puts the managed GGUF where the row looks for it.
func installSortformerWeights(t *testing.T, modelsDir string) {
	t.Helper()
	path := config.SortformerDiarizerAsset().ModelPath(modelsDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsMeetingDiarizerRow(t *testing.T) {
	tests := []struct {
		name           string
		diarizer       string
		installed      []string
		weights        bool
		diarizationOff bool
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "unset reads as the default", diarizer: "",
			wantContains:   []string{"Diarization engine: sherpa", "default"},
			wantNotContain: []string{"sortformer", "optional"},
		},
		{
			name: "sherpa", diarizer: config.DiarizerSherpa,
			wantContains:   []string{"Diarization engine: sherpa"},
			wantNotContain: []string{"sortformer"},
		},
		{
			// The copy marks it optional, and says what is missing instead of
			// claiming an engine that cannot run here is simply selected.
			name: "sortformer with nothing installed", diarizer: config.DiarizerSortformer,
			wantContains: []string{"Diarization engine: sortformer", "(optional)", "transcribe-cli not installed"},
		},
		{
			name: "sortformer with the CLI but no weights", diarizer: config.DiarizerSortformer,
			installed:      []string{"transcribe-cli"},
			wantContains:   []string{"(optional)", "weights not downloaded"},
			wantNotContain: []string{"transcribe-cli not installed"},
		},
		{
			name: "sortformer fully installed", diarizer: config.DiarizerSortformer,
			installed: []string{"transcribe-cli"}, weights: true,
			wantContains:   []string{"Diarization engine: sortformer", "(optional)"},
			wantNotContain: []string{"not installed", "not downloaded"},
		},
		{
			// With the review pass off, 'models ensure' plans no weights, so a
			// row telling the user to run it would be an unfixable instruction.
			name: "sortformer with diarization off", diarizer: config.DiarizerSortformer,
			diarizationOff: true,
			wantContains:   []string{"(optional)", "diarization is off"},
			wantNotContain: []string{"not installed", "not downloaded", "models ensure"},
		},
		{
			// A value this build cannot resolve is named, not silently rendered
			// as the default it is not.
			name: "hand-edited value", diarizer: "pyannote",
			wantContains: []string{"pyannote", "unknown value", config.DefaultMeetingDiarizer},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := meetingSettings(t, tt.diarizer, tt.installed...)
			if tt.weights {
				installSortformerWeights(t, m.cfg.ModelsDir)
			}
			if tt.diarizationOff {
				m.cfg.Speaker.Meeting.Enabled = false
			}
			row := m.meetingItems()[meetingRowDiarizer]
			for _, want := range tt.wantContains {
				if !strings.Contains(row, want) {
					t.Errorf("row = %q, want it to mention %q", row, want)
				}
			}
			for _, unwanted := range tt.wantNotContain {
				if strings.Contains(row, unwanted) {
					t.Errorf("row = %q, want it not to mention %q", row, unwanted)
				}
			}
		})
	}
}

// Both engines are reachable from the row, and the value written is the one
// the schema takes.
func TestSettingsMeetingDiarizerCycleWritesTheKey(t *testing.T) {
	tests := []struct {
		name string
		from string
		want string
	}{
		{name: "sherpa to sortformer", from: config.DiarizerSherpa, want: config.DiarizerSortformer},
		{name: "unset cycles off the default", from: "", want: config.DiarizerSortformer},
		{name: "sortformer back to sherpa", from: config.DiarizerSortformer, want: config.DiarizerSherpa},
		// Stepping past a typo would let it choose the engine that labels a
		// meeting; an unreadable value lands on the default instead.
		{name: "unknown value lands on the default", from: "pyannote", want: config.DefaultMeetingDiarizer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, saved := meetingSettings(t, tt.from, "transcribe-cli")
			m.cursor = meetingRowDiarizer
			m.adjustRow(1)

			if saved["speaker.meeting.diarizer"] != tt.want {
				t.Fatalf("saved = %#v, want %q", saved["speaker.meeting.diarizer"], tt.want)
			}
			if m.cfg.Speaker.Meeting.Diarizer != tt.want {
				t.Fatalf("live config = %q, want %q", m.cfg.Speaker.Meeting.Diarizer, tt.want)
			}
			if got, err := config.NormalizeMeetingDiarizer(tt.want); err != nil || got != tt.want {
				t.Fatalf("wrote a value the schema refuses: %q (%v)", tt.want, err)
			}
			if row := m.meetingItems()[meetingRowDiarizer]; !strings.Contains(row, tt.want) {
				t.Fatalf("row after cycling = %q, want it to show %q", row, tt.want)
			}
		})
	}
}

// A round trip returns to where it started, so neither engine is a one-way door.
func TestSettingsMeetingDiarizerCycleIsARoundTrip(t *testing.T) {
	m, _ := meetingSettings(t, config.DiarizerSherpa, "transcribe-cli")
	m.cursor = meetingRowDiarizer
	for range config.MeetingDiarizers() {
		m.adjustRow(1)
	}
	if m.cfg.Speaker.Meeting.Diarizer != config.DiarizerSherpa {
		t.Fatalf("after a full cycle = %q, want sherpa", m.cfg.Speaker.Meeting.Diarizer)
	}
}

// Picking an engine this computer cannot run still saves — the setting is
// valid — but the message says what is missing and how to fix it.
func TestSettingsMeetingDiarizerReportsAMissingInstall(t *testing.T) {
	m, saved := meetingSettings(t, config.DiarizerSherpa)
	m.cursor = meetingRowDiarizer
	m.adjustRow(1)

	if saved["speaker.meeting.diarizer"] != config.DiarizerSortformer {
		t.Fatalf("saved = %#v, want sortformer saved despite the gap", saved["speaker.meeting.diarizer"])
	}
	if !strings.Contains(m.message, "transcribe-cli not installed") {
		t.Errorf("message = %q, want the missing CLI", m.message)
	}
	if !strings.Contains(m.message, "models ensure") && !strings.Contains(m.message, "transcribecpp_binary") {
		t.Errorf("message = %q, want a remediation", m.message)
	}
	// Live labels are a separate embedding path and must not be implied to move.
	if !strings.Contains(m.message, "sherpa") {
		t.Errorf("message = %q, want it to say live labels stay on sherpa", m.message)
	}
}

// With the review pass switched off, 'models ensure' plans nothing — so
// neither the row nor the status line may send anyone to run it.
func TestSettingsMeetingDiarizerNeverAsksForAnInstallItCannotPlan(t *testing.T) {
	m, _ := meetingSettings(t, config.DiarizerSherpa)
	m.cfg.Speaker.Meeting.Enabled = false
	m.cursor = meetingRowDiarizer
	m.adjustRow(1)

	if m.cfg.Speaker.Meeting.Diarizer != config.DiarizerSortformer {
		t.Fatalf("engine = %q, want the pick to save with diarization off", m.cfg.Speaker.Meeting.Diarizer)
	}
	if strings.Contains(m.message, "models ensure") {
		t.Errorf("message = %q, want no models ensure while the review pass is off", m.message)
	}
	if !strings.Contains(m.message, "diarization is off") {
		t.Errorf("message = %q, want it to say why nothing installs", m.message)
	}
}

// The count reaches nothing under sortformer, so the row says so — and says
// nothing of the kind under sherpa, where it applies.
func TestSettingsMeetingExpectedSpeakersNoteOnlyUnderSortformer(t *testing.T) {
	tests := []struct {
		name        string
		diarizer    string
		numSpeakers int
		wantNote    bool
	}{
		{name: "sherpa auto", diarizer: config.DiarizerSherpa},
		{name: "sherpa fixed", diarizer: config.DiarizerSherpa, numSpeakers: 2},
		{name: "unknown engine is not sortformer", diarizer: "pyannote", numSpeakers: 2},
		{name: "sortformer auto", diarizer: config.DiarizerSortformer, wantNote: true},
		{name: "sortformer fixed", diarizer: config.DiarizerSortformer, numSpeakers: 2, wantNote: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := meetingSettings(t, tt.diarizer, "transcribe-cli")
			m.cfg.Speaker.Meeting.NumSpeakers = tt.numSpeakers
			row := m.meetingItems()[meetingRowNumSpeakers]

			if got := strings.Contains(row, "no effect"); got != tt.wantNote {
				t.Fatalf("row = %q, want the no-effect note = %v", row, tt.wantNote)
			}
			// Never "ignored": the count reaches neither the model nor the merge.
			if strings.Contains(strings.ToLower(row), "ignored") {
				t.Errorf("row = %q claims the count is ignored", row)
			}
			if tt.wantNote {
				// Tuning advice for a setting that changes nothing is worse than none.
				if strings.Contains(row, "1:1 interviews") || strings.Contains(row, "over-split") {
					t.Errorf("row = %q gives sherpa tuning advice under sortformer", row)
				}
			}
		})
	}
}

// Cycling the count under sortformer repeats the warning where the user is
// looking — the status line — and names the way back.
func TestSettingsMeetingNumSpeakersMessageUnderSortformer(t *testing.T) {
	m, _ := meetingSettings(t, config.DiarizerSortformer, "transcribe-cli")
	m.cursor = meetingRowNumSpeakers
	m.adjustRow(1)

	if !strings.Contains(m.message, "no effect") || !strings.Contains(m.message, config.DiarizerSherpa) {
		t.Fatalf("message = %q, want the no-effect note and the way back to sherpa", m.message)
	}
}

// The row is on screen, in the tab that owns meeting diarization.
func TestSettingsMeetingViewShowsTheDiarizerRow(t *testing.T) {
	m, _ := meetingSettings(t, config.DiarizerSortformer, "transcribe-cli")
	view := stripANSI(m.View())
	for _, want := range []string{"Diarization engine", "sortformer", "Expected speakers"} {
		if !strings.Contains(view, want) {
			t.Fatalf("meeting settings view missing %q:\n%s", want, view)
		}
	}
}
