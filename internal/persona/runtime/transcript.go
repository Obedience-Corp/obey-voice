package personaruntime

import (
	"errors"
	"slices"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

// transcript is the canonical conversation while the live provider keeps no
// shared history of its own. It is fenced by conversation epoch: turns
// recorded for a cleared or replaced conversation never reach the next one.
type transcript struct {
	mu      sync.Mutex
	current func() string
	epoch   string
	turns   []brain.Turn
}

func newTranscript(current func() string, turns []brain.Turn) *transcript {
	t := &transcript{current: current}
	t.reset(turns)
	return t
}

func (t *transcript) reset(turns []brain.Turn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.epoch, t.turns = t.current(), canonicalTurns(turns)
}

func (t *transcript) fence() {
	if epoch := t.current(); epoch != t.epoch {
		t.epoch, t.turns = epoch, nil
	}
}

func (t *transcript) Turns() []brain.Turn {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fence()
	return brain.CloneTurns(t.turns)
}

func (t *transcript) Append(epoch string, turns ...brain.Turn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fence()
	if epoch != t.epoch {
		return errors.New("transcript: turn belongs to a replaced conversation")
	}
	t.turns = append(t.turns, canonicalTurns(turns)...)
	return nil
}

// SetReplyMedia moves a reply's images into attachments, as the providers do
// for their own replies.
func (t *transcript) SetReplyMedia(text string, refs []chatmedia.Ref) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fence()
	for i := len(t.turns) - 1; i >= 0; i-- {
		if t.turns[i].Role == "assistant" && len(t.turns[i].ToolCalls) == 0 {
			t.turns[i].Content = text
			t.turns[i].Attachments = slices.Clone(refs)
			return nil
		}
	}
	return errors.New("transcript: no assistant reply to attach images to")
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
