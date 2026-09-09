package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureBundle lays down a .meeting bundle whose event stream is exactly
// these lines. trailingNewline off simulates a recording killed mid-append.
func fixtureBundle(t *testing.T, lines []string, trailingNewline bool) string {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "standup.meeting")
	internal := filepath.Join(bundle, BundleInternalDirName)
	if err := os.MkdirAll(internal, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n")
	if trailingNewline {
		body += "\n"
	}
	if err := os.WriteFile(filepath.Join(internal, BundleEventsName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, BundleDocumentName), []byte("# Meeting: Standup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// Background diarization appends speaker_analysis at whatever offset the wall
// clock had reached, well past the end of the audio. Rebasing on that would
// push every post-resume offset past the recording it belongs to.
func TestOpenBundleBaselineIgnoresPostRecordingEnrichment(t *testing.T) {
	bundle := fixtureBundle(t, []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Standup"}`,
		`{"type":"utterance","ts":"2026-07-22T09:29:00Z","offset_ms":1740000,"id":"utterance-1","text":"before the break","start_ms":1735000,"end_ms":1740000}`,
		`{"type":"session_end","ts":"2026-07-22T09:30:00Z","offset_ms":1800000}`,
		`{"type":"speaker_analysis","ts":"2026-07-22T09:33:00Z","offset_ms":1980000,"status":"complete"}`,
		`{"type":"speaker_utterance","ts":"2026-07-22T09:33:00Z","offset_ms":1980000,"id":"utterance-1","text":"before the break","label":"speaker-1","start_ms":1735000,"end_ms":1740000}`,
	}, true)

	w, err := OpenBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	events, err := ReadEvents(filepath.Join(bundle, BundleInternalDirName, BundleEventsName))
	if err != nil {
		t.Fatal(err)
	}
	var resume Event
	for _, e := range events {
		if e.Type == TypeResume {
			resume = e
		}
	}
	if resume.Type == "" {
		t.Fatal("no resume event written")
	}
	if resume.OffsetMs != 1800000 {
		t.Fatalf("resume offset_ms = %d, want 1800000 (session_end, not the 33m diarization append)", resume.OffsetMs)
	}
}

// A recording killed mid-append leaves half a JSONL line. The bundle is still
// the user's meeting: it has to list and it has to resume.
func TestReadEventsAndOpenBundleTolerateATornLastLine(t *testing.T) {
	bundle := fixtureBundle(t, []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Standup"}`,
		`{"type":"utterance","ts":"2026-07-22T09:29:00Z","offset_ms":1740000,"id":"utterance-1","text":"before the break","start_ms":1735000,"end_ms":1740000}`,
		`{"type":"utterance","ts":"2026-07-22T09:30:00Z","offset_ms":18000`,
	}, false)
	eventsPath := filepath.Join(bundle, BundleInternalDirName, BundleEventsName)

	events, err := ReadEvents(eventsPath)
	if err != nil {
		t.Fatalf("torn last line must not fail the read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want the 2 complete lines", len(events))
	}

	w, err := OpenBundle(bundle)
	if err != nil {
		t.Fatalf("torn bundle must still resume: %v", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The append starts on its own line rather than completing the torn one.
	after, err := ReadEvents(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after {
		if e.Type == TypeResume {
			return
		}
	}
	t.Fatalf("no resume event after reopening a torn bundle: %+v", after)
}

// A malformed line that is not the torn tail is real corruption, not a
// half-flushed write, and must not be silently dropped.
func TestReadEventsRejectsCorruptionAwayFromTheTail(t *testing.T) {
	bundle := fixtureBundle(t, []string{
		`{"type":"session_start","ts":"2026-07-22T09:00:00Z","offset_ms":0,"description":"Standup"}`,
		`{"type":"utterance", not json at all`,
		`{"type":"session_end","ts":"2026-07-22T09:30:00Z","offset_ms":1800000}`,
	}, true)

	if _, err := ReadEvents(filepath.Join(bundle, BundleInternalDirName, BundleEventsName)); err == nil {
		t.Fatal("expected a parse error for a corrupt interior line")
	}
}
