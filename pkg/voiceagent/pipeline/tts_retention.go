package pipeline

import (
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type ttsRetention struct {
	primary, fallback tts.Provider
	done              chan struct{}
}

// RetainTTS keeps a worker's admitted voices alive until its result has been
// spoken or discarded. The voice manager waits for these leases on replacement.
func (p *Pipeline) RetainTTS(primary, fallback tts.Provider) func() {
	r := &ttsRetention{primary: primary, fallback: fallback, done: make(chan struct{})}
	p.ttsMu.Lock()
	if p.ttsRetentions == nil {
		p.ttsRetentions = make(map[*ttsRetention]struct{})
	}
	p.ttsRetentions[r] = struct{}{}
	p.ttsMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.ttsMu.Lock()
			delete(p.ttsRetentions, r)
			close(r.done)
			p.ttsMu.Unlock()
		})
	}
}

// TTSRetentions returns outstanding leases for a voice set after replacement.
// Shutdown may close the set without waiting; ordinary retirement must wait.
func (p *Pipeline) TTSRetentions(primary, fallback tts.Provider) []<-chan struct{} {
	p.ttsMu.RLock()
	defer p.ttsMu.RUnlock()
	var pending []<-chan struct{}
	for r := range p.ttsRetentions {
		if (primary != nil && (r.primary == primary || r.fallback == primary)) ||
			(fallback != nil && (r.primary == fallback || r.fallback == fallback)) {
			pending = append(pending, r.done)
		}
	}
	return pending
}
