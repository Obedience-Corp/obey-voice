package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/listen"
	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
)

func TestDemoMeetingFinalizerUsesProductionResultsContract(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "demo.meeting")
	w, err := meetinglog.CreateBundle(bundle, "Demo", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"[speaker-1] hello", "[speaker-2] hi there"} {
		if err := w.OnUtterance(listen.Utterance{Text: text, At: w.StartedAt().Add(time.Duration(i+1) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	finalize := demoMeetingSpeakerFinalizer(w, bundle)
	result, err := finalize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SpeakerCount != 2 || len(result.Timeline.Observations) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(result.Artifact); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := newMeetingResults(summary)
	m.width, m.height = 80, 24
	m.resize()
	if content := m.content(); !strings.Contains(content, "Speaker-attributed transcript") || !strings.Contains(content, "speaker-2:") {
		t.Fatalf("attributed demo results missing:\n%s", content)
	}
}

func TestMeetingResultsFallsBackToPlainTranscript(t *testing.T) {
	w, err := meetinglog.CreateBundle(filepath.Join(t.TempDir(), "plain.meeting"), "Plain", "fake", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{Text: "a meeting without diarization", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	summary, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := newMeetingResults(summary)
	m.width, m.height = 80, 24
	m.resize()
	view := m.View()
	if !strings.Contains(view, "Transcript") || !strings.Contains(view, "a meeting without diarization") {
		t.Fatalf("plain transcript missing:\n%s", view)
	}
	if strings.Contains(view, "Speaker-attributed transcript") {
		t.Fatalf("plain meeting claimed attribution:\n%s", view)
	}
}

func TestSpeakerLabelsUseStableDistinctColors(t *testing.T) {
	labels := []string{"speaker-1", "speaker-2", "speaker-3", "speaker-4", "speaker-5", "speaker-6"}
	seen := make(map[string]string, len(labels))
	for _, label := range labels {
		color := string(speakerColor(label))
		if previous, exists := seen[color]; exists {
			t.Fatalf("%s and %s share color %s", previous, label, color)
		}
		seen[color] = label
		if again := string(speakerColor(label)); again != color {
			t.Fatalf("%s color changed from %s to %s", label, color, again)
		}
	}
	if speakerColor("speaker-7") != speakerColor("speaker-1") {
		t.Fatal("speaker palette should cycle predictably")
	}
	if speakerColor(" guest ") != speakerColor("GUEST") {
		t.Fatal("non-standard labels must normalize deterministically")
	}
}

func TestMeetingResultsContinueAndStandaloneExit(t *testing.T) {
	m := meetingResultsModel{summary: meetinglog.Summary{Description: "Done"}}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("embedded results must emit continuation")
	}
	if _, ok := cmd().(meetingResultsDoneMsg); !ok {
		t.Fatalf("embedded continuation = %T", cmd())
	}

	m.standalone = true
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("standalone results must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("standalone continuation = %T", cmd())
	}
}

// The review screen overlays attribution onto the transcript. A resumed
// segment that was never diarized has no speaker_utterance of its own, and
// replacing the transcript with the attributed events would drop it entirely.
func TestMeetingResultsKeepsUnattributedResumedLines(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "gap.meeting")
	w, err := meetinglog.CreateBundle(bundle, "Gap", "fake", "mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.OnUtterance(listen.Utterance{
		Text: "before the break", At: w.StartedAt().Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	records := w.Transcripts()
	if len(records) != 1 {
		t.Fatalf("transcripts = %d, want 1", len(records))
	}
	if err := w.WriteSpeakerAnalysis(meetinglog.SpeakerAnalysis{
		Status: "complete",
		Segments: []meetinglog.SpeakerSegment{{
			ID: "diarization-1", StartMS: 0, EndMS: 600000, Label: "speaker-1", State: "stable",
		}},
		Utterances: []meetinglog.SpeakerUtterance{{
			TranscriptRecord: records[0], Speaker: "speaker-1", State: "stable",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Resume and record without ever running diarization on the new segment.
	resumed, err := meetinglog.OpenBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.OnUtterance(listen.Utterance{
		Text: "after the break", At: time.Now().Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := resumed.Close()
	if err != nil {
		t.Fatal(err)
	}

	m := newMeetingResults(summary)
	m.width, m.height = 100, 30
	m.resize()
	content := m.content()
	if !strings.Contains(content, "speaker-1:") || !strings.Contains(content, "before the break") {
		t.Fatalf("attributed line missing from review:\n%s", content)
	}
	if !strings.Contains(content, "after the break") {
		t.Fatalf("the resumed segment's line is missing from review:\n%s", content)
	}
}
