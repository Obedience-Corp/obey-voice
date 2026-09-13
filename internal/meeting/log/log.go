// Package log records a meeting as a human-readable document plus a structured
// JSONL event stream. Both files are synced after every event so a crash
// never loses what was already captured.
//
// Import as:
//
//	import meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
//
// JSONL event types: session_start, utterance, note, bookmark, error,
// speaker_analysis, speaker_segment, speaker_utterance, session_end, routed.
// Notes and bookmarks carry offset_ms from session start so they can be
// aligned with the transcript later (Plaude-style important moments).
package log

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer is the crash-safe document/event sink for one meeting recording.
type Writer struct {
	mu                 sync.Mutex
	log                *os.File
	jsonl              *os.File
	path               string
	jsonlPath          string
	bundlePath         string
	description        string
	sttLabel           string
	started            time.Time
	offsetOrigin       time.Time // wall clock that maps onto offset 0; resumed sessions skip the gap
	utterances         int
	notes              int
	bookmarks          int
	errors             int
	transcripts        []TranscriptRecord
	priorUtterances    map[string]SpeakerUtterance
	segmentStartMS     int64 // meeting-relative offset this writer's segment begins at
	lastUtteranceEndMS int64
	speakerAnalysis    SpeakerAnalysis
	speakerCount       int
	closed             bool
	summary            Summary // last Close result; returned again on idempotent Close
}

// CreateBundle creates one private meeting directory with a canonical
// meeting.md document and hidden machine event stream. bundlePath must not
// already exist, preventing accidental mixing of two recordings. source names
// the capture surface (mac, ios, watch); empty omits it from the document and
// event stream.
func CreateBundle(bundlePath, description, sttLabel, source string) (*Writer, error) {
	if err := os.Mkdir(bundlePath, 0o700); err != nil {
		return nil, fmt.Errorf("meetinglog: create bundle %s: %w", bundlePath, err)
	}
	internalDir := filepath.Join(bundlePath, BundleInternalDirName)
	if err := os.Mkdir(internalDir, 0o700); err != nil {
		_ = os.Remove(bundlePath)
		return nil, fmt.Errorf("meetinglog: create bundle internals %s: %w", internalDir, err)
	}
	documentPath := filepath.Join(bundlePath, BundleDocumentName)
	eventsPath := filepath.Join(internalDir, BundleEventsName)
	w, err := createAt(documentPath, eventsPath, bundlePath, description, sttLabel, source)
	if err != nil {
		_ = os.RemoveAll(bundlePath)
		return nil, err
	}
	return w, nil
}

func createAt(path, jsonlPath, bundlePath, description, sttLabel, source string) (*Writer, error) {
	// 0o600: meeting transcripts are private (credentials, personal speech).
	logF, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: create %s: %w", path, err)
	}
	jsonlF, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = logF.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("meetinglog: create %s: %w", jsonlPath, err)
	}

	w := &Writer{
		log:         logF,
		jsonl:       jsonlF,
		path:        path,
		jsonlPath:   jsonlPath,
		bundlePath:  bundlePath,
		description: description,
		sttLabel:    sttLabel,
		started:     time.Now(),
	}
	w.offsetOrigin = w.started
	header := fmt.Sprintf("# Meeting: %s\n# Started: %s\n# STT: %s\n",
		description, w.started.Format(time.RFC3339), sttLabel)
	if source != "" {
		header += "# Source: " + source + "\n"
	}
	header += fmt.Sprintf("# JSONL: %s\n\n", jsonlPath)
	if err := w.writeLog(header); err != nil {
		_ = w.abortCreate()
		return nil, err
	}
	if err := w.writeEvent(Event{
		Type:   TypeSessionStart,
		Desc:   description,
		STT:    sttLabel,
		Source: source,
	}); err != nil {
		_ = w.abortCreate()
		return nil, err
	}
	return w, nil
}

func (w *Writer) abortCreate() error {
	_ = w.log.Close()
	_ = w.jsonl.Close()
	_ = os.Remove(w.path)
	_ = os.Remove(w.jsonlPath)
	return nil
}

// Description returns the meeting title from session_start (or the resume).
func (w *Writer) Description() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.description
}

// Path returns the canonical human-readable meeting document.
func (w *Writer) Path() string { return w.path }

// JSONLPath returns the structured event stream path.
func (w *Writer) JSONLPath() string { return w.jsonlPath }

// BundlePath returns the meeting-level directory.
func (w *Writer) BundlePath() string { return w.bundlePath }

// StartedAt returns when the session opened.
func (w *Writer) StartedAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started
}

// SegmentStartMS reports the meeting-relative offset where this writer's own
// recording segment begins: 0 for a new bundle, the resume baseline for a
// reopened one. Post-capture analysis needs it to know which part of the
// meeting the audio it just read actually covers.
func (w *Writer) SegmentStartMS() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.segmentStartMS
}

// Transcripts returns an owned snapshot for post-capture speaker attribution.
func (w *Writer) Transcripts() []TranscriptRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]TranscriptRecord(nil), w.transcripts...)
}

// Close writes the trailer / session_end and closes both files.
// Safe to call more than once: later calls return the first Summary and a nil
// error so embedded teardown and the CLI summary path can both Close.
func (w *Writer) Close() (Summary, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.summary, nil
	}
	s := Summary{
		Description:         w.description,
		Bundle:              w.bundlePath,
		File:                w.path,
		JSONLFile:           w.jsonlPath,
		StartedAt:           w.started,
		EndedAt:             time.Now(),
		Utterances:          w.utterances,
		Notes:               w.notes,
		Bookmarks:           w.bookmarks,
		Errors:              w.errors,
		SpeakerStatus:       w.speakerAnalysis.Status,
		SpeakerCount:        w.speakerCount,
		SpeakerAnalysisFile: w.speakerAnalysis.Artifact,
		AudioFile:           w.speakerAnalysis.AudioFile,
		SpeakerError:        w.speakerAnalysis.Error,
	}
	s.DurationSeconds = int64(s.EndedAt.Sub(w.origin()).Round(time.Second) / time.Second)
	if s.DurationSeconds < 0 {
		s.DurationSeconds = 0
	}
	trailer := fmt.Sprintf("\n# Ended: %s (duration %s, %d utterances, %d notes, %d bookmarks, %d errors)\n",
		s.EndedAt.Format(time.RFC3339), (time.Duration(s.DurationSeconds) * time.Second),
		s.Utterances, s.Notes, s.Bookmarks, s.Errors)
	werr := w.writeLog(trailer)
	jerr := w.writeEvent(Event{Type: TypeSessionEnd, TS: s.EndedAt.Format(time.RFC3339)})
	cerr1 := w.log.Close()
	cerr2 := w.jsonl.Close()
	w.closed = true
	w.summary = s
	if werr != nil {
		return s, werr
	}
	if jerr != nil {
		return s, jerr
	}
	if cerr1 != nil {
		return s, cerr1
	}
	return s, cerr2
}

func (w *Writer) writeLog(line string) error {
	if _, err := w.log.WriteString(line); err != nil {
		return fmt.Errorf("meetinglog: write log: %w", err)
	}
	if err := w.log.Sync(); err != nil {
		return fmt.Errorf("meetinglog: sync log: %w", err)
	}
	return nil
}

// writeEvent appends one JSONL line. TS/OffsetMs are filled if zero-ish.
// Caller must hold w.mu.
func (w *Writer) writeEvent(e Event) error {
	now := time.Now()
	if e.TS == "" {
		// Preserve sub-second ordering between short/test meetings while staying
		// RFC3339-compatible for existing readers.
		e.TS = now.Format(time.RFC3339Nano)
	}
	// Prefer wall clock of TS when parseable for offset; else now.
	at := now
	if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
		at = t
	}
	return w.writeEventAt(e, at.Sub(w.origin()).Milliseconds())
}

func (w *Writer) origin() time.Time {
	if !w.offsetOrigin.IsZero() {
		return w.offsetOrigin
	}
	return w.started
}

// writeEventAt appends one JSONL line at an explicit meeting-relative offset.
// Caller must hold w.mu.
func (w *Writer) writeEventAt(e Event, offsetMs int64) error {
	if e.TS == "" {
		e.TS = time.Now().Format(time.RFC3339Nano)
	}
	if offsetMs < 0 {
		offsetMs = 0
	}
	e.OffsetMs = offsetMs
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("meetinglog: marshal event: %w", err)
	}
	if _, err := w.jsonl.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("meetinglog: write jsonl: %w", err)
	}
	if err := w.jsonl.Sync(); err != nil {
		return fmt.Errorf("meetinglog: sync jsonl: %w", err)
	}
	return nil
}
