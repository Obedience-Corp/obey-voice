package personaruntime

import (
	"fmt"
	"slices"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// hostedNotices remembers, for one conversation, which hosted providers the
// user has been told about. It resets when the conversation is cleared.
type hostedNotices struct {
	mu    sync.Mutex
	epoch string
	// startedOn is the provider the conversation began on. Its notice is
	// shown but not spoken, so a conversation that never left a hosted
	// backend does not announce it at every launch.
	startedOn string
	told      []string
	// stale is set when the saved list no longer matches told.
	stale bool
}

func (n *hostedNotices) reset(epoch, startedOn string, told []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stale = len(n.told) > 0 && len(told) == 0
	n.epoch, n.startedOn, n.told = epoch, startedOn, slices.Clone(told)
}

func (n *hostedNotices) takeStale() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	stale := n.stale
	n.stale = false
	return stale
}

// sync starts a new record when the conversation epoch has changed, and
// reports whether that dropped providers already told.
func (n *hostedNotices) sync(epoch, live string) (dropped bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.epoch == epoch {
		return false
	}
	dropped = len(n.told) > 0
	n.epoch, n.startedOn, n.told = epoch, live, nil
	return dropped
}

func (n *hostedNotices) list() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.told)
}

// take records provider as told and reports whether it was new, and whether
// it is the provider the conversation started on.
func (n *hostedNotices) take(epoch, provider string) (fresh, started bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.epoch != epoch {
		n.epoch, n.startedOn, n.told = epoch, "", nil
	}
	if slices.Contains(n.told, provider) {
		return false, false
	}
	n.told = append(n.told, provider)
	return true, n.startedOn == provider
}

var providerLabels = map[string]string{
	"claude": "Claude", "codex": "Codex", "cursor": "Cursor", "grok": "Grok", "muse-spark": "Muse Spark", "obey": "Obey",
}

func providerLabel(provider string) string {
	if label, ok := providerLabels[provider]; ok {
		return label
	}
	return provider
}

// sharesWholeConversation reports whether a persona on a hosted backend
// receives the conversation from its start.
func (s *Selector) sharesWholeConversation() bool {
	return s.opts.Base != nil && s.opts.Base.HostedPersonaSharing == "whole_conversation"
}

// HostedNotice tells the user, once per conversation and provider, that the
// responding persona's backend is hosted. The pipeline calls it before the
// first request that would send conversation text to that provider.
func (s *Selector) HostedNotice(envelope events.ResponseEnvelope) (pipeline.HostedNotice, bool) {
	if !envelope.Hosted || envelope.BrainProvider == "" {
		return pipeline.HostedNotice{}, false
	}
	fresh, started := s.notices.take(envelope.ConversationEpoch, envelope.BrainProvider)
	if !fresh {
		return pipeline.HostedNotice{}, false
	}
	s.persistNotices()
	label := providerLabel(envelope.BrainProvider)
	name := envelope.DisplayName
	if name == "" {
		name = "This persona"
	}
	scope := s.noticeScope(envelope.PersonaID, envelope.BrainProvider, label)
	return pipeline.HostedNotice{
		Text:   fmt.Sprintf("%s runs on %s, a hosted service. %s", name, label, scope),
		Spoken: !started,
	}, true
}

// persistNotices saves which providers the user has been told about with the
// session, so a resumed conversation does not repeat them.
func (s *Selector) persistNotices() {
	if s.opts.PersistHostedNotified != nil {
		s.warnPersistence(s.opts.PersistHostedNotified(s.notices.list()))
	}
}

// RestoreNotices follows RestoreSession and RestoreContexts at the same
// serialized boundary, with the providers a saved conversation already told
// the user about.
func (s *Selector) RestoreNotices(providers []string) {
	s.notices.reset(s.opts.Pipeline.ConversationScope().ConversationEpoch, s.restored.provider, providers)
	s.notices.takeStale()
}

func (s *Selector) liveProvider() string {
	if s.binding == nil {
		return ""
	}
	return s.binding.BrainProvider
}

// noticeScope says what the provider is about to receive, from the persona's
// share of the conversation rather than from the setting alone. A provider
// that can only hold a conversation started on it receives all of it.
func (s *Selector) noticeScope(personaID, provider, label string) string {
	if !slices.Contains(sessionCatchUpProviders, provider) && !slices.Contains(sharedHistoryProviders, provider) {
		return fmt.Sprintf("This conversation is sent to %s.", label)
	}
	turns := s.sharedTurns()
	from := len(turns)
	if cursor, ok := s.transcript.session(personaID, turns); ok {
		from = cursor.Session.From
	} else if s.sharesWholeConversation() {
		from = 0
	}
	switch {
	case len(turns) == 0:
		return fmt.Sprintf("What is said to it is sent to %s.", label)
	case from == 0:
		return fmt.Sprintf("The whole conversation is sent to %s.", label)
	}
	return fmt.Sprintf("Only what is said from when it was invited is sent to %s; earlier lines stay on this machine.", label)
}
