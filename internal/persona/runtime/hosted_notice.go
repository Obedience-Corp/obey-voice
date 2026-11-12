package personaruntime

import (
	"fmt"
	"slices"
	"sync"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// hostedNotices remembers, for one conversation, which hosted providers the
// user has been told about, and for which of them that the whole conversation
// is sent. It resets when the conversation is cleared.
type hostedNotices struct {
	mu    sync.Mutex
	epoch string
	told  []string
	// whole lists the providers told that they receive the whole
	// conversation. A provider told only from an invitation is told again
	// when a persona on it would receive more.
	whole []string
	// stale is set when the saved list no longer matches told.
	stale bool
}

func (n *hostedNotices) reset(epoch string, told, whole []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.stale = len(n.told) > 0 && len(told) == 0
	n.epoch, n.told, n.whole = epoch, slices.Clone(told), slices.Clone(whole)
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
func (n *hostedNotices) sync(epoch string) (dropped bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.epoch == epoch {
		return false
	}
	dropped = len(n.told) > 0
	n.epoch, n.told, n.whole = epoch, nil, nil
	return dropped
}

func (n *hostedNotices) list() (told, whole []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.told), slices.Clone(n.whole)
}

// due reports whether the user must be told about provider before it receives
// the given share.
func (n *hostedNotices) due(epoch, provider string, whole bool) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.epoch != epoch {
		return true
	}
	return !slices.Contains(n.told, provider) || (whole && !slices.Contains(n.whole, provider))
}

// take records provider as told about the given share and reports whether
// that changed the record.
func (n *hostedNotices) take(epoch, provider string, whole bool) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.epoch != epoch {
		n.epoch, n.told, n.whole = epoch, nil, nil
	}
	changed := false
	if !slices.Contains(n.told, provider) {
		n.told, changed = append(n.told, provider), true
	}
	if whole && !slices.Contains(n.whole, provider) {
		n.whole, changed = append(n.whole, provider), true
	}
	return changed
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
// responding persona's backend is hosted, and again if a persona on it would
// receive the whole conversation after the user was told it receives only what
// is said from its invitation. The pipeline calls it before the first request
// that would send conversation text to that provider, and records the notice
// as given only once it was delivered.
func (s *Selector) HostedNotice(envelope events.ResponseEnvelope) (pipeline.HostedNotice, bool) {
	if !envelope.Hosted || envelope.BrainProvider == "" {
		return pipeline.HostedNotice{}, false
	}
	label := providerLabel(envelope.BrainProvider)
	scope, whole := s.noticeScope(envelope.PersonaID, envelope.BrainProvider, label)
	if !s.notices.due(envelope.ConversationEpoch, envelope.BrainProvider, whole) {
		return pipeline.HostedNotice{}, false
	}
	name := envelope.DisplayName
	if name == "" {
		name = "This persona"
	}
	return pipeline.HostedNotice{
		Text: fmt.Sprintf("%s runs on %s, a hosted service. %s", name, label, scope),
		Delivered: func() {
			if s.notices.take(envelope.ConversationEpoch, envelope.BrainProvider, whole) {
				s.persistNotices()
			}
		},
	}, true
}

// LiveHostedNotice is HostedNotice for the selected persona.
func (s *Selector) LiveHostedNotice() (pipeline.HostedNotice, bool) {
	envelope := responseEnvelope(s.Snapshot())
	envelope.ConversationEpoch = s.opts.Pipeline.ConversationScope().ConversationEpoch
	return s.HostedNotice(envelope)
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
// the user about, and those of them told the whole conversation is sent.
func (s *Selector) RestoreNotices(told, whole []string) {
	s.notices.reset(s.opts.Pipeline.ConversationScope().ConversationEpoch, told, whole)
	s.notices.takeStale()
}

// noticeScope says what the provider is about to receive, from the persona's
// share of the conversation rather than from the setting alone, and whether
// that is the whole conversation. A provider that can only hold a conversation
// started on it receives all of it.
func (s *Selector) noticeScope(personaID, provider, label string) (string, bool) {
	if !slices.Contains(sessionCatchUpProviders, provider) && !slices.Contains(sharedHistoryProviders, provider) {
		return fmt.Sprintf("This conversation is sent to %s.", label), true
	}
	turns := s.sharedTurns()
	from := len(turns)
	if cursor, ok := s.transcript.session(personaID, turns); ok && cursorProvider(cursor) == provider {
		from = cursor.Session.From
	} else if s.sharesWholeConversation() {
		from = 0
	}
	switch {
	case len(turns) == 0:
		return fmt.Sprintf("What is said to it is sent to %s.", label), true
	case from == 0:
		return fmt.Sprintf("The whole conversation is sent to %s.", label), true
	}
	return fmt.Sprintf("Only what is said from when it was invited is sent to %s, plus jobs it started earlier and their results; earlier lines stay on this machine.", label), false
}
