package log

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenBundle reopens an existing meeting directory for another recording
// segment. Offsets continue from the last event without counting the wall-clock
// gap. A resume marker is written so the document shows the break. session_end
// may already be present (a clean stop); another one is written when this
// writer Closes.
func OpenBundle(bundlePath string) (*Writer, error) {
	if !strings.HasSuffix(strings.ToLower(filepath.Base(strings.TrimSpace(bundlePath))), ".meeting") {
		return nil, fmt.Errorf("meetinglog: %s is not a .meeting bundle", bundlePath)
	}
	documentPath := filepath.Join(bundlePath, BundleDocumentName)
	eventsPath := filepath.Join(bundlePath, BundleInternalDirName, BundleEventsName)
	events, err := ReadEvents(eventsPath)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("meetinglog: empty event stream %s", eventsPath)
	}

	w := &Writer{
		path:       documentPath,
		jsonlPath:  eventsPath,
		bundlePath: bundlePath,
	}
	w.priorUtterances = make(map[string]SpeakerUtterance)
	for _, e := range events {
		switch e.Type {
		case TypeSessionStart:
			w.description = e.Desc
			w.sttLabel = e.STT
			if t, err := parseEventTime(e.TS); err == nil {
				w.started = t
			}
		case TypeUtterance:
			w.utterances++
			w.transcripts = append(w.transcripts, TranscriptRecord{
				ID: e.ID, StartMS: e.StartMS, EndMS: e.EndMS, Text: e.Text,
			})
			if e.EndMS > w.lastUtteranceEndMS {
				w.lastUtteranceEndMS = e.EndMS
			}
		case TypeNote:
			w.notes++
		case TypeBookmark:
			w.bookmarks++
		case TypeError:
			w.errors++
		case TypeSpeakerUtterance:
			if e.ID != "" {
				w.priorUtterances[e.ID] = SpeakerUtterance{
					TranscriptRecord: TranscriptRecord{
						ID: e.ID, StartMS: e.StartMS, EndMS: e.EndMS, Text: e.Text,
					},
					Speaker: e.Label, Confidence: e.Confidence, State: e.State,
				}
			}
		}
		if RecordedOffset(e.Type) && e.OffsetMs > w.lastUtteranceEndMS {
			w.lastUtteranceEndMS = e.OffsetMs
		}
	}
	if w.started.IsZero() {
		return nil, fmt.Errorf("meetinglog: %s has no session_start", eventsPath)
	}
	if w.description == "" {
		w.description = "meeting"
	}

	logF, err := os.OpenFile(documentPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: reopen log %s: %w", documentPath, err)
	}
	// Drop a torn tail before appending: writing after a half-flushed line
	// would glue this session's first event onto it and turn a tolerable
	// broken tail into corruption in the middle of the stream.
	if err := trimTornTail(eventsPath); err != nil {
		_ = logF.Close()
		return nil, err
	}
	jsonlF, err := os.OpenFile(eventsPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = logF.Close()
		return nil, fmt.Errorf("meetinglog: reopen jsonl %s: %w", eventsPath, err)
	}
	w.log, w.jsonl = logF, jsonlF
	w.segmentStartMS = w.lastUtteranceEndMS
	w.offsetOrigin = time.Now().Add(-time.Duration(w.lastUtteranceEndMS) * time.Millisecond)

	now := time.Now()
	if err := w.writeLog(fmt.Sprintf("\n[%s] ▶ resumed\n", now.Format("15:04:05"))); err != nil {
		_ = w.log.Close()
		_ = w.jsonl.Close()
		return nil, err
	}
	if err := w.writeEventAt(Event{
		Type: TypeResume,
		Text: "recording continued",
		TS:   now.Format(time.RFC3339Nano),
	}, w.lastUtteranceEndMS); err != nil {
		_ = w.log.Close()
		_ = w.jsonl.Close()
		return nil, err
	}
	return w, nil
}

func parseEventTime(ts string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, ts)
}
