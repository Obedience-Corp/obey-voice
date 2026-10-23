package tui

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	ttsproviders "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/providers"
)

// personaVoiceCatalog holds the Voice row's live catalog for providers whose
// static list is only a placeholder (openai-speech: the configured voice plus
// `default`, when the server may have a hundred). It is fetched on form open
// and when the TTS row lands on such a provider; every other provider answers
// from the static catalog and never waits.
type personaVoiceCatalog struct {
	// load is the injection seam; production constructs the provider, which
	// for openai-speech is three HTTP calls and no model load.
	load func(ctx context.Context, cfg *config.Config, provider string) ([]tts.Voice, string, error)

	seq      int
	loading  bool
	provider string
	voices   []tts.Voice
	notice   string
	cancel   context.CancelFunc
}

// personaVoicesMsg carries a catalog fetch back to the form.
type personaVoicesMsg struct {
	seq      int
	provider string
	voices   []tts.Voice
	notice   string
	err      error
}

// voiceCatalogTimeout bounds one fetch; the provider's own probe is 3 s.
const voiceCatalogTimeout = 5 * time.Second

// voiceLocaleAll is language index 0: no filter.
const voiceLocaleAll = "(all)"

// liveVoiceCatalogProvider reports providers whose voices are only known by
// asking the running target.
func liveVoiceCatalogProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), config.OpenAISpeechProviderName)
}

// liveVoiceCatalog is the production loader: build the provider for cfg with
// the TTS provider swapped in, list, and release it.
func liveVoiceCatalog(ctx context.Context, cfg *config.Config, provider string) ([]tts.Voice, string, error) {
	if cfg == nil {
		return nil, "", errors.New("no config")
	}
	probe := *cfg
	probe.TTSProvider = provider
	type result struct {
		voices []tts.Voice
		notice string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		p, cleanup, err := ttsproviders.NewProvider(&probe)
		if err != nil {
			done <- result{err: err}
			return
		}
		voices := p.ListVoices("", "")
		notice := tts.CatalogNoticeOf(p)
		if cleanup != nil {
			cleanup()
		}
		done <- result{voices: voices, notice: notice}
	}()
	select {
	case r := <-done:
		return r.voices, r.notice, r.err
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}

// loadVoiceCatalog starts a fetch for the effective TTS provider when it has a
// live catalog, and clears any live state otherwise. The language filter is
// reset either way: locales belong to a catalog.
func (m *personasModel) loadVoiceCatalog() tea.Cmd {
	m.voiceLocale = ""
	m.cancelVoiceCatalog()
	provider := m.effectiveStackTTSProvider()
	if !liveVoiceCatalogProvider(provider) || m.voices.load == nil {
		m.resetVoiceCatalog()
		return nil
	}
	if m.voices.provider == provider && !m.voices.loading && m.voices.voices != nil {
		return nil
	}
	m.voices.seq++
	m.voices.loading = true
	m.voices.provider = provider
	m.voices.voices = nil
	m.voices.notice = ""
	seq, load, cfg := m.voices.seq, m.voices.load, m.cfg
	ctx, cancel := context.WithTimeout(context.Background(), voiceCatalogTimeout)
	m.voices.cancel = cancel
	return func() tea.Msg {
		voices, notice, err := load(ctx, cfg, provider)
		return personaVoicesMsg{seq: seq, provider: provider, voices: voices, notice: notice, err: err}
	}
}

func (m *personasModel) cancelVoiceCatalog() {
	if m.voices.cancel != nil {
		m.voices.cancel()
		m.voices.cancel = nil
	}
}

// resetVoiceCatalog drops live state and retires any fetch in flight: the seq
// bump means its message, cancelled or not, is ignored when it lands.
func (m *personasModel) resetVoiceCatalog() {
	m.cancelVoiceCatalog()
	m.voices.seq++
	m.voices.loading = false
	m.voices.provider = ""
	m.voices.voices = nil
	m.voices.notice = ""
}

// applyVoiceCatalog lands a fetch. A failed fetch leaves the static list and
// says why on the row; the persona can still be saved with a typed voice.
func (m *personasModel) applyVoiceCatalog(msg personaVoicesMsg) {
	if msg.seq != m.voices.seq || msg.provider != m.voices.provider || !m.voices.loading {
		return
	}
	m.cancelVoiceCatalog()
	m.voices.loading = false
	if msg.err != nil {
		m.voices.voices = nil
		m.voices.notice = "voice catalog unavailable: " + firstLine(msg.err.Error())
		return
	}
	m.voices.voices = msg.voices
	m.voices.notice = msg.notice
	m.clampSelectedVoice()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// voiceEntries is the catalog behind the Voice row for the effective
// provider: the live list when one landed, else the static one.
func (m personasModel) voiceEntries() []tts.Voice {
	provider := m.effectiveStackTTSProvider()
	if liveVoiceCatalogProvider(provider) && m.voices.provider == provider && m.voices.voices != nil {
		return m.voices.voices
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "qwen3-tts") {
		if voices, err := tts.StaticVoicesFor(m.cfg, provider, "", ""); err == nil {
			return voices
		}
	}
	names := stackVoiceCatalog(m.cfg, provider)
	out := make([]tts.Voice, 0, len(names))
	for _, name := range names {
		out = append(out, tts.Voice{Name: name, FriendlyName: name})
	}
	return out
}

// voiceLocales lists the Language row: (all) plus every locale the catalog
// states, in catalog order.
func (m personasModel) voiceLocales() []string {
	out := []string{voiceLocaleAll}
	seen := map[string]bool{}
	for _, v := range m.voiceEntries() {
		loc := strings.TrimSpace(v.Locale)
		if loc == "" || seen[loc] {
			continue
		}
		seen[loc] = true
		out = append(out, loc)
	}
	sort.Strings(out[1:])
	return out
}

// voiceLanguageUsable reports whether the Language row filters anything.
func (m personasModel) voiceLanguageUsable() bool { return len(m.voiceLocales()) > 2 }

func (m personasModel) voiceLocaleIndex() int {
	if m.voiceLocale == "" {
		return 0
	}
	for i, loc := range m.voiceLocales() {
		if i > 0 && strings.EqualFold(loc, m.voiceLocale) {
			return i
		}
	}
	return 0
}

// cycleVoiceLocale moves ←/→ through the languages; no-op for one language.
func (m *personasModel) cycleVoiceLocale(delta int) {
	if !m.voiceLanguageUsable() {
		return
	}
	list := m.voiceLocales()
	idx := (m.voiceLocaleIndex() + delta + len(list)) % len(list)
	if idx == 0 {
		m.voiceLocale = ""
		return
	}
	m.voiceLocale = list[idx]
}

// filteredVoiceEntries applies the Language row to the catalog. A voice that
// states no locale stays visible under every filter, like the "default" voice.
func (m personasModel) filteredVoiceEntries() []tts.Voice {
	entries := m.voiceEntries()
	if m.voiceLocale == "" {
		return entries
	}
	out := make([]tts.Voice, 0, len(entries))
	for _, v := range entries {
		if v.Locale == "" || strings.EqualFold(v.Locale, m.voiceLocale) {
			out = append(out, v)
		}
	}
	return out
}

// selectedVoiceEntry is the catalog entry for the selected voice, if listed.
func (m personasModel) selectedVoiceEntry() (tts.Voice, bool) {
	v := strings.TrimSpace(m.selectedVoice)
	if v == "" {
		return tts.Voice{}, false
	}
	for _, e := range m.voiceEntries() {
		if strings.EqualFold(e.Name, v) {
			return e, true
		}
	}
	return tts.Voice{}, false
}

// voiceDetail is the dim line under the Voice row: what the catalog knows
// about the selected voice, or the catalog's own notice.
func (m personasModel) voiceDetail() string {
	if m.voices.loading && liveVoiceCatalogProvider(m.effectiveStackTTSProvider()) {
		return "fetching the voice catalog…"
	}
	if e, ok := m.selectedVoiceEntry(); ok {
		parts := []string{}
		for _, s := range []string{e.Locale, e.Gender, e.Age, e.Description} {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " · ")
		}
	}
	return m.voices.notice
}

// formInnerWidth is the text budget inside a form box.
func (m personasModel) formInnerWidth() int { return m.formBoxWidth() - 4 }
