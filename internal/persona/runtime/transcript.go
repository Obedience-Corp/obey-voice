package personaruntime

import (
	"errors"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// transcript is the canonical conversation while the live provider keeps no
// shared history of its own. Only an explicit clear or restore replaces its
// turns; a turn recorded for a conversation that is no longer current is
// refused rather than added to the next one.
type transcript struct {
	mu      sync.Mutex
	current func() string
	turns   []brain.Turn
}

func newTranscript(current func() string, turns []brain.Turn) *transcript {
	return &transcript{current: current, turns: canonicalTurns(turns)}
}

func (t *transcript) reset(turns []brain.Turn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns = canonicalTurns(turns)
}

func (t *transcript) Clear() { t.reset(nil) }

func (t *transcript) Turns() []brain.Turn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return brain.CloneTurns(t.turns)
}

func (t *transcript) Append(epoch string, turns ...brain.Turn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if epoch != t.current() {
		return errors.New("transcript: turn belongs to a replaced conversation")
	}
	t.turns = append(t.turns, canonicalTurns(turns)...)
	return nil
}

// SetRecordedReplyMedia moves a reply's images into attachments, as the
// providers do for their own replies.
func (t *transcript) SetRecordedReplyMedia(reply brain.Turn, text string, refs []chatmedia.Ref) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return brain.SetRecordedReplyMedia(t.turns, reply, text, refs)
}

// sessionView is the part of the transcript loaded into a provider without
// shared history: the window it keeps after a live turn, so a long restored
// conversation does not look like an overdue compaction.
func sessionView(turns []brain.Turn, cfg *config.Config) []brain.Turn {
	if cfg == nil || cfg.MaxHistory <= 0 || len(turns) <= cfg.MaxHistory {
		return turns
	}
	return brain.HistoryWindow(turns, cfg.MaxHistory)
}

// canonicalTurns copies turns into the shared role scheme. Prompt-based
// providers saved replies under their own role name.
func canonicalTurns(turns []brain.Turn) []brain.Turn {
	out := brain.CloneTurns(turns)
	for i := range out {
		if out[i].Role == "samantha" {
			out[i].Role = "assistant"
		}
	}
	return out
}
