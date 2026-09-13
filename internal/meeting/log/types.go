package log

import (
	"time"
)

// Event kinds written to the JSONL sidecar.
const (
	TypeSessionStart     = "session_start"
	TypeUtterance        = "utterance"
	TypeNote             = "note"
	TypeBookmark         = "bookmark"
	TypeError            = "error"
	TypeSpeakerAnalysis  = "speaker_analysis"
	TypeSpeakerSegment   = "speaker_segment"
	TypeSpeakerUtterance = "speaker_utterance"
	TypeSessionEnd       = "session_end"
	// TypeRoutePlan is the durable start-of-meeting routing intent. A bundle
	// holding a route_plan but no routed event has an undelivered route.
	TypeRoutePlan = "route_plan"

	// Control events are additive: only remote (phone/Watch) recordings emit
	// them, and readers that predate them simply skip unknown types. Idea
	// spans bracket a moment the user wants filed as a separate intent.
	TypePause     = "pause"
	TypeResume    = "resume"
	TypeIdeaStart = "idea_start"
	TypeIdeaEnd   = "idea_end"
	// TypeIdeaFiled records that a span's idea was filed as an Inbox intent
	// (Label = span id). Its presence is the dedupe: a reprocessed bundle
	// files only spans without one.
	TypeIdeaFiled = "idea_filed"
	// TypeSegmentGap marks audio the client could not deliver (outbox
	// overflow or an abandoned tail) so later readers never mistake the gap
	// for silence.
	TypeSegmentGap = "segment_gap"

	// Bundle filenames keep one visible item per meeting while preserving the
	// machine event stream needed for recovery, routing, and reprocessing.
	BundleDocumentName        = "meeting.md"
	BundleInternalDirName     = ".samantha"
	BundleEventsName          = "events.jsonl"
	BundleSpeakerAnalysisName = "speaker-analysis.json"
	BundleAudioName           = "audio.wav"
)

// Event is one JSONL record. OffsetMs is milliseconds since session start.
type Event struct {
	Type         string  `json:"type"`
	TS           string  `json:"ts"`                // RFC3339
	OffsetMs     int64   `json:"offset_ms"`         // from session start
	ID           string  `json:"id,omitempty"`      // stable within this meeting
	Text         string  `json:"text,omitempty"`    // utterance / note body
	Label        string  `json:"label,omitempty"`   // bookmark or speaker label
	Message      string  `json:"message,omitempty"` // error text
	STT          string  `json:"stt,omitempty"`     // session_start only
	Source       string  `json:"source,omitempty"`  // session_start only: capture surface (mac, ios, watch)
	Desc         string  `json:"description,omitempty"`
	Status       string  `json:"status,omitempty"`
	Artifact     string  `json:"artifact,omitempty"`
	AudioFile    string  `json:"audio_file,omitempty"`
	StartMS      int64   `json:"start_ms,omitempty"`
	EndMS        int64   `json:"end_ms,omitempty"`
	Confidence   float32 `json:"confidence,omitempty"`
	State        string  `json:"state,omitempty"`
	Timing       string  `json:"timing,omitempty"`
	SpeakerCount int     `json:"speaker_count,omitempty"`
}

// TranscriptRecord is the timestamp estimate retained for post-capture
// attribution. It contains text only; no PCM or embeddings are persisted.
type TranscriptRecord struct {
	ID      string
	StartMS int64
	EndMS   int64
	Text    string
}

// SpeakerSegment and SpeakerUtterance are persistence-only projections of the
// analysis domain, keeping this log package independent from meeting policy.
type SpeakerSegment struct {
	ID         string
	StartMS    int64
	EndMS      int64
	Label      string
	Confidence float32
	State      string
}

type SpeakerUtterance struct {
	TranscriptRecord
	Speaker    string
	Confidence float32
	State      string
}

type SpeakerAnalysis struct {
	Status     string
	Error      string
	Artifact   string
	AudioFile  string
	Segments   []SpeakerSegment
	Utterances []SpeakerUtterance
}

// Summary describes a finished recording, for the file trailer and the
// command's console/JSON summaries.
type Summary struct {
	Description         string    `json:"description"`
	Bundle              string    `json:"bundle,omitempty"` // one meeting-level directory
	File                string    `json:"file"`             // canonical human document
	JSONLFile           string    `json:"jsonl_file"`       // structured event stream
	StartedAt           time.Time `json:"started_at"`
	EndedAt             time.Time `json:"ended_at"`
	DurationSeconds     int64     `json:"duration_seconds"`
	Utterances          int       `json:"utterances"`
	Notes               int       `json:"notes"`
	Bookmarks           int       `json:"bookmarks"`
	Errors              int       `json:"errors"`
	SpeakerStatus       string    `json:"speaker_status,omitempty"`
	SpeakerCount        int       `json:"speaker_count,omitempty"`
	SpeakerAnalysisFile string    `json:"speaker_analysis_file,omitempty"`
	AudioFile           string    `json:"audio_file,omitempty"`
	SpeakerError        string    `json:"speaker_error,omitempty"`
}

// Duration reports the recording length. DurationSeconds wins when it is set
// because a resumed meeting's wall-clock span includes the break, while the
// recorded offsets do not.
func (s Summary) Duration() time.Duration {
	if s.DurationSeconds > 0 {
		return time.Duration(s.DurationSeconds) * time.Second
	}
	return s.EndedAt.Sub(s.StartedAt)
}

// RecordedOffset reports whether an event's offset_ms measures recorded time.
// Diarization and routing append to a finished bundle at whatever offset the
// wall clock had reached by then, so counting those would stretch the meeting
// by the enrichment runtime — wrong for both the resume baseline and any
// duration derived from the stream.
func RecordedOffset(eventType string) bool {
	switch eventType {
	case TypeSessionStart, TypeSessionEnd, TypeUtterance, TypeNote, TypeBookmark,
		TypeError, TypeResume, TypePause, TypeIdeaStart, TypeIdeaEnd, TypeSegmentGap:
		return true
	}
	return false
}
