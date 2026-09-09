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

func resumeTimeline(label string) speaker.Timeline {
	return speaker.Timeline{Observations: []speaker.Observation{{
		SegmentID: "diarization-" + label, StartMS: 0, EndMS: 600000,
		Label: label, State: speaker.StateStable, Source: speaker.SourceRecording,
	}}}
}

func workingPCMPath(bundle string) string {
	return filepath.Join(bundle, meetinglog.BundleInternalDirName, workingAudioPartName)
}

// With record_audio off the working PCM is discarded at every stop, so a
// resumed pass holds only the segment it recorded. It must attribute just that
// segment and leave the earlier pass's labels standing, and the routed note
// must still show each line once.
func TestResumedMeetingAttributesOnlyTheAnalyzedSegment(t *testing.T) {
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
	first := &recordingSpeakerAnalyzer{timeline: resumeTimeline("speaker-1")}
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
	if _, err := os.Stat(workingPCMPath(bundle)); !os.IsNotExist(err) {
		t.Fatalf("record_audio off must not keep the working PCM past a stop, stat err = %v", err)
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
	second := &recordingSpeakerAnalyzer{timeline: resumeTimeline("speaker-2")}
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
	if _, err := os.Stat(workingPCMPath(bundle)); !os.IsNotExist(err) {
		t.Fatalf("resumed pass must not keep the working PCM either, stat err = %v", err)
	}

	if second.sampleCount != 16000 {
		t.Fatalf("resumed diarizer saw %d samples, want only its own 16000-sample segment", second.sampleCount)
	}

	events, err := ReadEvents(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string][]string{}
	attributed := 0
	for _, e := range events {
		if e.Type != meetinglog.TypeSpeakerUtterance {
			continue
		}
		attributed++
		labels[e.ID] = append(labels[e.ID], e.Label)
	}
	if attributed != 2 {
		t.Fatalf("speaker_utterance events = %d, want 2 — the resumed pass must not rewrite the earlier line", attributed)
	}
	if got := labels["utterance-1"]; len(got) != 1 || got[0] != "speaker-1" {
		t.Fatalf("utterance-1 labels = %v, want the first pass's [speaker-1] untouched", got)
	}
	if got := labels["utterance-2"]; len(got) != 1 || got[0] != "speaker-2" {
		t.Fatalf("utterance-2 labels = %v, want the resumed pass's [speaker-2]", got)
	}

	summary, err := LoadSummaryFromJSONL(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	note, err := Render(summary, BodyNotes)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"**speaker-1:** first segment line",
		"**speaker-2:** second segment line",
	} {
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

// A resumed segment whose diarization never ran — Ctrl+C during "Diarizing
// speakers…", the setting off, or a model load failure — has no attributed
// events of its own, and the earlier pass's do not cover it. The readers must
// overlay attribution onto the transcript, not replace the transcript with it,
// or the whole segment disappears from the routed note while the header still
// counts its utterances.
func TestResumedSegmentWithoutDiarizationStillRenders(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "gap.meeting")
	writer, err := meetinglog.CreateBundle(bundle, "Gap", "fake", "mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.OnUtterance(listen.Utterance{
		Text: "before the break", At: writer.StartedAt().Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	capture := &fakeCaptureSubscriber{}
	analyzer := &recordingSpeakerAnalyzer{timeline: resumeTimeline("speaker-1")}
	session, err := NewSpeakerSession(capture, analyzer, writer, bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	capture.publish(make([]float32, 32000))
	if _, err := session.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	// Resume and record, but never finalize a speaker session.
	resumed, err := meetinglog.OpenBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.OnUtterance(listen.Utterance{
		Text: "after the break", At: time.Now().Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Close(); err != nil {
		t.Fatal(err)
	}

	summary, err := LoadSummaryFromJSONL(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	note, err := Render(summary, BodyNotes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note.Body, "**speaker-1:** before the break") {
		t.Fatalf("diarized line lost its label:\n%s", note.Body)
	}
	if !strings.Contains(note.Body, "after the break") {
		t.Fatalf("the resumed segment's line is missing from the routed note:\n%s", note.Body)
	}
	if strings.Contains(note.Body, "**speaker-1:** after the break") {
		t.Fatalf("an un-diarized line must stay plain, not borrow a label:\n%s", note.Body)
	}
	if summary.Utterances != 2 {
		t.Fatalf("header counts %d utterances but the transcript must show both", summary.Utterances)
	}
}

// Each pass's diarizer numbers its segments from 1, so a resumed bundle would
// otherwise carry two diarization-1 events describing different audio.
func TestResumedPassNamespacesItsSpeakerSegmentIDs(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "ids.meeting")
	writer, err := meetinglog.CreateBundle(bundle, "Ids", "fake", "mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.OnUtterance(listen.Utterance{
		Text: "first", At: writer.StartedAt().Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	capture := &fakeCaptureSubscriber{}
	session, err := NewSpeakerSession(capture, &recordingSpeakerAnalyzer{timeline: resumeTimeline("speaker-1")}, writer, bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	capture.publish(make([]float32, 32000))
	if _, err := session.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_ = session.Close()

	resumed, err := meetinglog.OpenBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.OnUtterance(listen.Utterance{
		Text: "second", At: time.Now().Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	capture2 := &fakeCaptureSubscriber{}
	session2, err := NewSpeakerSession(capture2, &recordingSpeakerAnalyzer{timeline: resumeTimeline("speaker-2")}, resumed, bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	capture2.publish(make([]float32, 16000))
	if _, err := session2.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	_ = session2.Close()

	events, err := ReadEvents(bundleEventsPath(bundle))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range events {
		if e.Type == meetinglog.TypeSpeakerSegment {
			seen[e.ID]++
		}
	}
	if len(seen) != 2 {
		t.Fatalf("speaker_segment ids = %v, want one per pass", seen)
	}
	if seen["diarization-speaker-1"] != 1 {
		t.Fatalf("the first pass must keep its bare id, got %v", seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("id %q appears %d times, want 1", id, n)
		}
	}
}
