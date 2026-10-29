package tui

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	ttsproviders "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/providers"
)

// voiceCatalog is the live voice list for providers whose static list is only
// a placeholder (openai-speech: the configured voice plus `default`, when the
// server may have a hundred). The Personas form and Settings each hold one;
// every other provider answers from the static catalog and never waits.
type voiceCatalog struct {
	// load is the injection seam; production constructs the provider, which
	// for openai-speech is three HTTP calls and no model load.
	load func(ctx context.Context, cfg *config.Config, provider string) ([]tts.Voice, string, error)

	seq      int64
	loading  bool
	provider string
	voices   []tts.Voice
	notice   string
	cancel   context.CancelFunc
}

// voiceCatalogResult is one fetch's answer. Each screen wraps it in its own
// message type, so an answer can only land on the screen that asked.
type voiceCatalogResult struct {
	seq      int64
	provider string
	voices   []tts.Voice
	notice   string
	err      error
}

// voiceCatalogSeq numbers fetches across models: Settings is rebuilt on every
// visit, and a fetch from the last visit must not match one from this visit.
var voiceCatalogSeq atomic.Int64

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
// the TTS provider swapped in, list, and release it. cfg is the fetch's own
// copy.
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

// fetch points the catalog at provider. A provider with a live catalog gets a
// fetch unless one is in flight or landed for it; any other provider clears
// the live state. The returned func runs the fetch; nil means there is none.
func (c *voiceCatalog) fetch(cfg *config.Config, provider string) func() voiceCatalogResult {
	if !liveVoiceCatalogProvider(provider) || c.load == nil {
		c.reset()
		return nil
	}
	if c.provider == provider && (c.loading || c.voices != nil) {
		return nil
	}
	c.stop()
	c.seq = voiceCatalogSeq.Add(1)
	c.loading, c.provider, c.voices, c.notice = true, provider, nil, ""
	seq, load := c.seq, c.load
	// The fetch runs off the Update goroutine, which keeps writing the shared
	// config (a voice chosen while it runs); it reads a copy taken here.
	var snapshot *config.Config
	if cfg != nil {
		copied := *cfg
		snapshot = &copied
	}
	ctx, cancel := context.WithTimeout(context.Background(), voiceCatalogTimeout)
	c.cancel = cancel
	return func() voiceCatalogResult {
		voices, notice, err := load(ctx, snapshot, provider)
		return voiceCatalogResult{seq: seq, provider: provider, voices: voices, notice: notice, err: err}
	}
}

// reset drops the live state and outdates any fetch in flight.
func (c *voiceCatalog) reset() {
	c.stop()
	c.seq = voiceCatalogSeq.Add(1)
	c.loading, c.provider, c.voices, c.notice = false, "", nil, ""
}

func (c *voiceCatalog) stop() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

// apply lands the fetch in flight and reports whether r was it. A failed
// fetch leaves no live list and says why in the notice.
func (c *voiceCatalog) apply(r voiceCatalogResult) bool {
	if r.seq != c.seq || !c.loading || r.provider != c.provider {
		return false
	}
	c.stop()
	c.loading = false
	if r.err != nil {
		c.voices = nil
		c.notice = "voice catalog unavailable: " + firstLine(r.err.Error())
		return true
	}
	c.voices, c.notice = r.voices, r.notice
	return true
}

// landed is provider's live list, or nil until one has landed for it.
func (c voiceCatalog) landed(provider string) []tts.Voice {
	if liveVoiceCatalogProvider(provider) && c.provider == provider {
		return c.voices
	}
	return nil
}

// note is the dim line beside a voice list: the fetch in flight, why it
// failed, or why the catalog is short.
func (c voiceCatalog) note(provider string) string {
	if !liveVoiceCatalogProvider(provider) || c.provider != provider {
		return ""
	}
	if c.loading {
		return "fetching the voice catalog…"
	}
	return c.notice
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// voiceLocalesOf is a Language filter's options: (all), then every locale the
// voices state, sorted.
func voiceLocalesOf(voices []tts.Voice) []string {
	out := []string{voiceLocaleAll}
	seen := map[string]bool{}
	for _, v := range voices {
		loc := strings.TrimSpace(v.Locale)
		if loc == "" || seen[strings.ToLower(loc)] {
			continue
		}
		seen[strings.ToLower(loc)] = true
		out = append(out, loc)
	}
	sort.Strings(out[1:])
	return out
}

// voicesInLocale filters voices to locale ("" keeps all). A voice that states
// no locale stays under every filter, like the "default" voice, and so does
// the one named keep.
func voicesInLocale(voices []tts.Voice, locale, keep string) []tts.Voice {
	if locale == "" {
		return voices
	}
	out := make([]tts.Voice, 0, len(voices))
	for _, v := range voices {
		loc := strings.TrimSpace(v.Locale)
		if loc == "" || strings.EqualFold(loc, locale) || (keep != "" && strings.EqualFold(v.Name, keep)) {
			out = append(out, v)
		}
	}
	return out
}

// voiceEntryDetail is what a catalog says about one voice:
// locale · gender · age · description, skipping what it does not state.
func voiceEntryDetail(v tts.Voice) string {
	parts := []string{}
	for _, s := range []string{v.Locale, v.Gender, v.Age, v.Description} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}
