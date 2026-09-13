package pipeline

import "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"

// SetJournalHealth publishes copied diagnostics outside coordinator locks.
// Polling this projection lets the UI recover a dropped health notification.
func (p *Pipeline) SetJournalHealth(h events.WorkJournalHealth) {
	if p == nil || h.Scope != p.WorkSnapshot().Scope {
		return
	}
	p.journalMu.Lock()
	p.journalHealth = h
	p.journalMu.Unlock()
	p.emit(h)
}

func (p *Pipeline) JournalHealth() events.WorkJournalHealth {
	if p == nil {
		return events.WorkJournalHealth{}
	}
	p.journalMu.RLock()
	defer p.journalMu.RUnlock()
	return p.journalHealth
}
