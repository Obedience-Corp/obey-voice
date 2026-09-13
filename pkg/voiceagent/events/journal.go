package events

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"time"
)

// WorkJournalHealth describes diagnostic durability only. It does not change
// canonical work, job state or the shared spending ledger.
type WorkJournalHealth struct {
	Scope        work.WorkScope `json:"scope"`
	Incomplete   bool           `json:"incomplete"`
	Dropped      uint64         `json:"dropped"`
	LastError    string         `json:"last_error,omitempty"`
	PersistedSeq uint64         `json:"persisted_seq"`
	PersistedAt  time.Time      `json:"persisted_at"`
}

func (WorkJournalHealth) eventType() string { return "journal.health" }
