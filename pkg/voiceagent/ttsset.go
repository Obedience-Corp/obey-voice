package voiceagent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// TTSSet is the selected provider plus its explicitly configured
// conversational fallback. Preview/test/batch paths intentionally continue to
// use tts.NewProvider directly so they prove the selected voice instead of
// silently producing a different one.
type TTSSet struct {
	Primary         tts.Provider
	Fallback        tts.Provider
	FallbackWarning error
	cleanups        []func()
}

func NewTTSSet(cfg *config.Config) (*TTSSet, error) {
	primary, primaryCleanup, err := tts.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	set := &TTSSet{Primary: primary}
	if primaryCleanup != nil {
		set.cleanups = append(set.cleanups, primaryCleanup)
	}

	if strings.EqualFold(strings.TrimSpace(cfg.TTSFallbackProvider), string(tts.FallbackKokoro)) &&
		!strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "kokoro") {
		fallbackCfg := *cfg
		fallbackCfg.TTSProvider = "kokoro"
		fallback, fallbackCleanup, fallbackErr := tts.NewProvider(&fallbackCfg)
		if fallbackErr != nil {
			set.FallbackWarning = fmt.Errorf("fallback TTS (Kokoro) unavailable: %w", fallbackErr)
		} else {
			set.Fallback = fallback
			if fallbackCleanup != nil {
				set.cleanups = append(set.cleanups, fallbackCleanup)
			}
		}
	}
	return set, nil
}

func (s *TTSSet) Close() {
	if s == nil {
		return
	}
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		s.cleanups[i]()
	}
	s.cleanups = nil
}

// defaultTTSRetireDelay is how long a replaced TTS set stays loaded so an
// utterance already handed to it can finish. The 1.7b native worker's warm
// time-to-first-audio is 12–31s in the field; 45s covers that plus playback
// drain without stacking workers across persona / Settings reloads.
const defaultTTSRetireDelay = 45 * time.Second

// LiveTTSManager owns the live TTS set and any set it just replaced.
//
// Install swaps the pipeline onto the new set immediately. The previous set
// is closed after RetireDelay so a turn already synthesizing on the old
// worker can finish; without that drain, every voice or persona change
// leaked another qwen3-tts-worker (4 GB for the 1.7b tier) until serve exited.
type LiveTTSManager struct {
	mu sync.Mutex
	// RetireDelay is how long a replaced set stays loaded. Zero uses
	// defaultTTSRetireDelay; negative closes the previous set immediately
	// (tests).
	RetireDelay time.Duration

	closed   bool
	sets     []*TTSSet
	retiring []*TTSSet
	done     chan struct{}
}

func (m *LiveTTSManager) retireDelay() time.Duration {
	if m == nil {
		return defaultTTSRetireDelay
	}
	if m.RetireDelay < 0 {
		return 0
	}
	if m.RetireDelay == 0 {
		return defaultTTSRetireDelay
	}
	return m.RetireDelay
}

func (m *LiveTTSManager) ensureDone() {
	if m.done == nil {
		m.done = make(chan struct{})
	}
}

// Adopt tracks a set already installed on the pipeline so a later Install
// can retire it. The caller must not Close the set; the manager owns it.
func (m *LiveTTSManager) Adopt(set *TTSSet) bool {
	if m == nil || set == nil {
		return set == nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.ensureDone()
	m.sets = append(m.sets, set)
	return true
}

func (m *LiveTTSManager) Install(p *pipeline.Pipeline, set *TTSSet) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	m.ensureDone()
	retired := m.sets
	m.sets = []*TTSSet{set}
	m.retiring = append(m.retiring, retired...)
	p.ReplaceTTS(set.Primary, set.Fallback)
	delay := m.retireDelay()
	done := m.done
	m.mu.Unlock()

	if delay <= 0 {
		m.finishRetire(retired)
		return true
	}
	go m.retireAfter(retired, delay, done)
	return true
}

func (m *LiveTTSManager) retireAfter(sets []*TTSSet, delay time.Duration, done <-chan struct{}) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		m.finishRetire(sets)
	case <-done:
		// Close() owns the sets.
	}
}

func (m *LiveTTSManager) finishRetire(sets []*TTSSet) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.retiring = dropSets(m.retiring, sets)
	m.mu.Unlock()
	closeTTSSets(sets)
}

func (m *LiveTTSManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.ensureDone()
	close(m.done)
	all := append(append([]*TTSSet{}, m.sets...), m.retiring...)
	m.sets = nil
	m.retiring = nil
	m.mu.Unlock()
	closeTTSSets(all)
}

func closeTTSSets(sets []*TTSSet) {
	for i := len(sets) - 1; i >= 0; i-- {
		sets[i].Close()
	}
}

func dropSets(from, drop []*TTSSet) []*TTSSet {
	if len(from) == 0 || len(drop) == 0 {
		return from
	}
	remove := make(map[*TTSSet]struct{}, len(drop))
	for _, s := range drop {
		remove[s] = struct{}{}
	}
	kept := make([]*TTSSet, 0, len(from))
	for _, s := range from {
		if _, drop := remove[s]; !drop {
			kept = append(kept, s)
		}
	}
	return kept
}
