package meeting

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/listen"
	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
)

func resumeTimeline() speaker.Timeline {
	return speaker.Timeline{Observations: []speaker.Observation{{
		SegmentID: "diarization-1", StartMS: 0, EndMS: 600000,
		Label: "speaker-1", State: speaker.StateStable, Source: speaker.SourceRecording,
	}}}
}

// A resumed meeting diarizes the whole recording again, so the second pass
// re-attributes lines the first pass already wrote. The routed note and the
// review must still show each line once, and — with record_audio off — the
// second diarizer must see the whole recording, not just its own segment.
func TestResumedMeetingKeepsOneTranscriptLineAndWholeAudio(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "standup.meeting")

	writer, err := meetinglog.CreateBundle(bundle, "Standup", "fake", "mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.OnUtterance(listen.Utterance{
		Text: "first segment line", At: writer.StartedAt().Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	capture := &fakeCaptureSubscriber{}
	first := &recordingSpeakerAnalyzer{timeline: resumeTimeline()}
	session, err := NewSpeakerSession(capture, first, writer, bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	capture.publish(make([]float32, 32000)) // 2s at 16 kHz
	if _, err := session.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := meetinglog.OpenBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.OnUtterance(listen.Utterance{
		Text: "second segment line", At: time.Now().Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	capture2 := &fakeCaptureSubscriber{}
	second := &recordingSpeakerAnalyzer{timeline: resumeTimeline()}
	session2, err := NewSpeakerSession(capture2, second, resumed, bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	capture2.publish(make([]float32, 16000)) // 1s more
	if _, err := session2.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session2.Close(); err != nil {
		t.Fatal(err)
	}

	if second.sampleCount != 48000 {
		t.Fatalf("resumed diarizer saw %d samples, want the whole 48000-sample recording", second.sampleCount)
	}

	events, err := ReadEvents(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	attributed := 0
	for _, e := range events {
		if e.Type == meetinglog.TypeSpeakerUtterance {
			attributed++
		}
	}
	if attributed != 2 {
		t.Fatalf("speaker_utterance events = %d, want 2 (the second pass must not re-emit unchanged lines)", attributed)
	}

	summary, err := LoadSummaryFromJSONL(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	note, err := Render(summary, BodyNotes)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"first segment line", "second segment line"} {
		if got := strings.Count(note.Body, line); got != 1 {
			t.Fatalf("%q appears %d times in the routed note, want 1:\n%s", line, got, note.Body)
		}
	}
}

// An older bundle already holds the duplicates, so the reader still has to
// collapse them — keeping the newest labelling for each id.
func TestRenderCollapsesRepeatedSpeakerUtterancesByID(t *testing.T) {
	summary := meetinglog.Summary{Description: "Standup", JSONLFile: "x.jsonl"}
	events := []meetinglog.Event{
		{Type: meetinglog.TypeSpeakerUtterance, ID: "utterance-1", Text: "opening line", Label: "speaker-1", OffsetMs: 1000},
		{Type: meetinglog.TypeSpeakerUtterance, ID: "utterance-1", Text: "opening line", Label: "speaker-2", OffsetMs: 1000},
		{Type: meetinglog.TypeSpeakerUtterance, ID: "utterance-2", Text: "closing line", Label: "speaker-1", OffsetMs: 5000},
	}
	body := RenderEvents(summary, events, BodyNotes).Body
	if got := strings.Count(body, "opening line"); got != 1 {
		t.Fatalf("opening line appears %d times, want 1:\n%s", got, body)
	}
	if !strings.Contains(body, "**speaker-2:** opening line") {
		t.Fatalf("want the latest labelling for utterance-1:\n%s", body)
	}
	if got := strings.Count(body, "closing line"); got != 1 {
		t.Fatalf("closing line appears %d times, want 1:\n%s", got, body)
	}
}

// meeting list, the HTTP index, and the routed note read DurationSeconds. A
// resumed meeting's wall-clock span includes the break; the recorder's own
// trailer does not, and post-capture diarization events must not extend it.
func TestSummaryDurationCountsRecordedTimeOnly(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "standup.meeting")
	jsonl := writeFixtureEvents(t, bundle, []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Standup"}`,
		`{"type":"utterance","ts":"2026-07-22T09:29:00Z","offset_ms":1740000,"id":"utterance-1","text":"before the break"}`,
		`{"type":"session_end","ts":"2026-07-22T09:30:00Z","offset_ms":1800000}`,
		`{"type":"resume","ts":"2026-07-22T11:30:00Z","offset_ms":1800000,"text":"recording continued"}`,
		`{"type":"utterance","ts":"2026-07-22T11:39:00Z","offset_ms":2340000,"id":"utterance-2","text":"after the break"}`,
		`{"type":"session_end","ts":"2026-07-22T11:40:00Z","offset_ms":2400000}`,
		`{"type":"speaker_analysis","ts":"2026-07-22T11:45:00Z","offset_ms":2700000,"status":"complete","speaker_count":1}`,
	})

	summary, err := LoadSummaryFromJSONL(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if summary.DurationSeconds != 2400 {
		t.Fatalf("DurationSeconds = %d, want 2400 (40 recorded minutes, not the 2h40m span)", summary.DurationSeconds)
	}
	if got := summary.Duration(); got != 40*time.Minute {
		t.Fatalf("Duration() = %s, want 40m0s", got)
	}
}

// writeFixtureEvents lays down a .meeting bundle holding exactly these JSONL
// lines and returns the event stream path.
func writeFixtureEvents(t *testing.T, bundle string, lines []string) string {
	t.Helper()
	internal := filepath.Join(bundle, meetinglog.BundleInternalDirName)
	if err := os.MkdirAll(internal, 0o700); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(internal, meetinglog.BundleEventsName)
	if err := os.WriteFile(jsonl, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return jsonl
}
