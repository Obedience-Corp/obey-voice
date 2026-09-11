package voiceagent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// qwenProviderName is the only provider place applies to; kokoro always speaks
// on this computer.
const qwenProviderName = "qwen3-tts"

// TTSSet is the selected provider plus its explicitly configured
// conversational fallback. Preview/test/batch paths intentionally continue to
// use tts.NewProvider directly so they prove the selected voice instead of
// silently producing a different one.
type TTSSet struct {
	Primary         tts.Provider
	Fallback        tts.Provider
	FallbackWarning error
	// VoiceNotice is a non-fatal warning about the voice this set will
	// actually speak in — today, a clone voice that cannot cross the LAN.
	// Displayed wherever FallbackWarning is: talk start and Settings reload.
	VoiceNotice error
	cleanups    []func()
}

func NewTTSSet(cfg *config.Config) (*TTSSet, error) {
	primary, primaryCleanup, err := tts.NewProvider(cfg)
	if err != nil {
		if set, ok := speechPlaceFallbackSet(cfg, err); ok {
			return set, nil
		}
		return nil, err
	}
	set := &TTSSet{Primary: primary, VoiceNotice: speechVoiceNotice(cfg)}
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

// speechPlaceFallbackSet answers a speech worker that is not there at startup.
// A conversation must still start (D7): mid-turn recovery (pipeline
// playFallback) never runs for a provider that failed to construct, so the
// swap to Kokoro has to happen here. It is deliberately narrow — only an
// unreachable LAN speech worker qualifies. A misconfigured URL, a strict
// operator, or a disabled fallback all still refuse to enter talk.
func speechPlaceFallbackSet(cfg *config.Config, cause error) (*TTSSet, bool) {
	switch {
	case cfg == nil,
		config.TTSPlaceOf(cfg) != config.TTSPlaceLAN,
		!strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwenProviderName),
		!errors.Is(cause, tts.ErrProviderUnavailable),
		cfg.TTSLanStrict,
		!strings.EqualFold(strings.TrimSpace(cfg.TTSFallbackProvider), string(tts.FallbackKokoro)):
		return nil, false
	}

	fallbackCfg := *cfg
	fallbackCfg.TTSProvider = "kokoro"
	fallbackCfg.TTSPlace = config.TTSPlaceLocal
	kokoro, cleanup, err := tts.NewProvider(&fallbackCfg)
	if err != nil {
		// No voice at all: report the speech worker, which is what broke.
		return nil, false
	}
	set := &TTSSet{
		Primary: kokoro,
		// The voice notice describes the configuration, not this session, so it
		// still applies: when that computer comes back it speaks its own voice.
		VoiceNotice: speechVoiceNotice(cfg),
		FallbackWarning: fmt.Errorf("speech worker %s is unreachable; speaking with Kokoro on this computer: %w",
			strings.TrimSpace(cfg.TTSWorkerURL), cause),
	}
	if cleanup != nil {
		set.cleanups = append(set.cleanups, cleanup)
	}
	return set, true
}

// speechVoiceNotice adapts the config-level notice to an error for display.
func speechVoiceNotice(cfg *config.Config) error {
	if notice := config.SpeechVoiceNotice(cfg); notice != "" {
		return errors.New(notice)
	}
	return nil
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
// utterance already handed to it can finish. It matches the native worker's
// per-request timeout (qwen_tts_timeout default 120s): Settings → ReloadVoice
// can land mid-turn, and a 45s drain would kill a synth still inside its
// advertised budget.
const defaultTTSRetireDelay = 120 * time.Second

// LiveTTSManager owns the live TTS set and any set it just replaced.
//
// Install swaps the pipeline onto the new set immediately and closes the
// previous set after RetireDelay so a turn already synthesizing can finish
// (Settings → ReloadVoice can land mid-turn). ReplaceAndClose is the
// after-turn path (persona switch): no drain. Without either, every voice
// change leaked another qwen3-tts-worker (4 GB for the 1.7b tier) until
// serve exited.
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
	return m.swap(p, set, m.retireDelay())
}

// ReplaceAndClose swaps the pipeline onto set and closes the previous set
// immediately. Serve's persona switch runs after the current turn has
// finished, so there is no in-flight utterance to drain — holding a 4 GB
// 1.7b worker for the ReloadVoice delay would be a leak with a timer.
func (m *LiveTTSManager) ReplaceAndClose(p *pipeline.Pipeline, set *TTSSet) bool {
	return m.swap(p, set, 0)
}

// CommitPrepared runs a host selection transaction while holding the manager
// lifetime fence, then publishes the new voice before allowing Close to run.
// The callback should invoke PreparedBrainReplacement.Commit with the durable
// session persistence function. Keeping this lock across that callback means a
// successful persistence cannot be followed by a closed-manager TTS install.
// The callback must not call this manager recursively.
// Ownership transfers only on success. On error the caller still owns set
// and must close it, matching Install and ReplaceAndClose.
func (m *LiveTTSManager) CommitPrepared(p *pipeline.Pipeline, set *TTSSet, commit func() error) error {
	if m == nil {
		return errors.New("tts manager is unavailable")
	}
	if p == nil {
		return errors.New("tts pipeline is unavailable")
	}
	if set == nil || set.Primary == nil {
		return errors.New("prepared TTS set is incomplete")
	}
	if commit == nil {
		return errors.New("prepared selection commit is required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("tts manager is closed")
	}
	m.ensureDone()
	if err := commit(); err != nil {
		m.mu.Unlock()
		return err
	}
	retired, done := m.swapLocked(p, set)
	delay := m.retireDelay()
	m.mu.Unlock()
	if delay <= 0 {
		m.finishRetire(retired)
	} else {
		go m.retireAfter(retired, delay, done)
	}
	return nil
}

// ReplaceTransaction is the host-facing name for CommitPrepared. It keeps
// the selection callback and voice publication under one LiveTTSManager
// lifetime fence.
func (m *LiveTTSManager) ReplaceTransaction(p *pipeline.Pipeline, set *TTSSet, commit func() error) error {
	return m.CommitPrepared(p, set, commit)
}

func (m *LiveTTSManager) swap(p *pipeline.Pipeline, set *TTSSet, delay time.Duration) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	m.ensureDone()
	retired, done := m.swapLocked(p, set)
	m.mu.Unlock()

	if delay <= 0 {
		m.finishRetire(retired)
		return true
	}
	go m.retireAfter(retired, delay, done)
	return true
}

func (m *LiveTTSManager) swapLocked(p *pipeline.Pipeline, set *TTSSet) ([]*TTSSet, <-chan struct{}) {
	var retired []*TTSSet
	for _, previous := range m.sets {
		if previous != set {
			retired = append(retired, previous)
		}
	}
	m.sets = []*TTSSet{set}
	m.retiring = append(m.retiring, retired...)
	p.ReplaceTTS(set.Primary, set.Fallback)
	done := m.done
	return retired, done
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
	owned := takeSets(&m.retiring, sets)
	m.mu.Unlock()
	closeTTSSets(owned)
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

func takeSets(from *[]*TTSSet, want []*TTSSet) []*TTSSet {
	if from == nil || len(*from) == 0 || len(want) == 0 {
		return nil
	}
	claim := make(map[*TTSSet]struct{}, len(want))
	for _, s := range want {
		claim[s] = struct{}{}
	}
	taken := make([]*TTSSet, 0, len(want))
	kept := make([]*TTSSet, 0, len(*from))
	for _, s := range *from {
		if _, ok := claim[s]; ok {
			taken = append(taken, s)
			continue
		}
		kept = append(kept, s)
	}
	*from = kept
	return taken
}
