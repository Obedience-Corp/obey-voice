// Package workjournal retains bounded diagnostic evidence. The normal session
// store remains the sole authority for restoring work and delivering results.
package workjournal

import (
	"encoding/json"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

const Schema = "obey-voice.work/v1"

type Record struct {
	DetailPayload     *DetailPayload  `json:"-"`
	Schema            string          `json:"schema"`
	SessionID         string          `json:"session_id"`
	RuntimeID         string          `json:"runtime_id"`
	ConversationEpoch string          `json:"conversation_epoch"`
	Seq               uint64          `json:"seq"`
	At                time.Time       `json:"at"`
	MonoMS            int64           `json:"mono_ms"`
	Type              string          `json:"type"`
	Scope             string          `json:"scope"`
	Revision          uint64          `json:"revision"`
	JobID             string          `json:"job_id,omitempty"`
	CallID            string          `json:"call_id,omitempty"`
	ResultID          string          `json:"result_id,omitempty"`
	RequestID         string          `json:"request_id,omitempty"`
	TurnID            string          `json:"turn_id,omitempty"`
	Data              json.RawMessage `json:"data"`
}

// DetailPayload contains only the observed, already-redacted tool preview.
// OriginalBytes may describe a larger output that was never captured here.
type DetailPayload struct {
	Text          string
	OriginalBytes int64
}

type Gap struct {
	RuntimeID string `json:"runtime_id,omitempty"`
	From      uint64 `json:"from"`
	To        uint64 `json:"to"`
	Reason    string `json:"reason"`
}

type Health struct {
	Incomplete   bool      `json:"incomplete"`
	Dropped      uint64    `json:"dropped"`
	Gaps         []Gap     `json:"gaps,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	PersistedSeq uint64    `json:"persisted_seq"`
	PersistedAt  time.Time `json:"persisted_at"`
	RetainedFrom uint64    `json:"retained_from"`
}

// Checkpoint is a redacted diagnostic projection, never a restore input.
type Checkpoint struct {
	Projection string        `json:"projection"`
	Sealed     bool          `json:"sealed"`
	Schema     string        `json:"schema"`
	Authority  string        `json:"authority"` // always "diagnostic_only"
	At         time.Time     `json:"at"`
	LastSeq    uint64        `json:"last_seq"`
	Snapshot   work.Snapshot `json:"snapshot"`
	Health     Health        `json:"health"`
}

type Options struct {
	MaxRecords         int
	MaxQueueBytes      int
	SegmentBytes       int64
	SessionBytes       int64
	GlobalSealedBytes  int64
	DetailBytes        int
	SessionDetailBytes int64
	ExpireAfter        time.Duration
	SyncInterval       time.Duration
}

func (o Options) defaults() Options {
	if o.MaxRecords <= 0 {
		o.MaxRecords = 4096
	}
	if o.MaxQueueBytes <= 0 {
		o.MaxQueueBytes = 8 << 20
	}
	if o.SegmentBytes <= 0 {
		o.SegmentBytes = 8 << 20
	}
	if o.SessionBytes <= 0 {
		o.SessionBytes = 64 << 20
	}
	if o.GlobalSealedBytes <= 0 {
		o.GlobalSealedBytes = 256 << 20
	}
	if o.DetailBytes <= 0 {
		o.DetailBytes = 64 << 10
	}
	if o.SessionDetailBytes <= 0 {
		o.SessionDetailBytes = 16 << 20
	}
	if o.ExpireAfter <= 0 {
		o.ExpireAfter = 30 * 24 * time.Hour
	}
	if o.SyncInterval <= 0 {
		o.SyncInterval = 250 * time.Millisecond
	}
	return o
}

type Detail struct {
	Ref           string `json:"ref,omitempty"`
	OriginalBytes int64  `json:"original_bytes"`
	RetainedBytes int    `json:"retained_bytes"`
	Truncated     bool   `json:"truncated"`
	Unavailable   string `json:"unavailable,omitempty"`
}

type Report struct {
	Schema        string       `json:"schema"`
	Source        string       `json:"source"`
	PersistedOnly bool         `json:"persisted_only"`
	Checkpoints   []Checkpoint `json:"checkpoints"`
	Records       []Record     `json:"records"`
	Incomplete    bool         `json:"incomplete"`
	Notes         []string     `json:"notes,omitempty"`
}
