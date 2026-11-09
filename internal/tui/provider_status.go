package tui

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func (m *conversationModel) applyProviderStatus(status *events.ProviderStatus, trace *events.TraceContext) {
	if status == nil || status.Binding == "" {
		return
	}
	if trace != nil {
		if trace.ResponseID != nil && *trace.ResponseID != m.providerResponseID {
			return
		}
		if m.sessionID != "" && trace.SessionID != "" && trace.SessionID != m.sessionID {
			return
		}
		if trace.PersonaID != nil && m.selection.PersonaID != "" && *trace.PersonaID != m.selection.PersonaID {
			return
		}
		if m.selection.ConversationEpoch != "" && trace.ConversationEpoch != "" && trace.ConversationEpoch != m.selection.ConversationEpoch {
			return
		}
	}
	if m.providerHealth == nil {
		m.providerHealth = make(map[string]events.ProviderStatus)
	}
	previous, exists := m.providerHealth[status.Role]
	if exists && previous.Revision >= status.Revision {
		return
	}
	// A successful different model/endpoint cannot heal the failing binding.
	if status.State == "available" && exists && previous.Binding != status.Binding {
		return
	}
	m.providerHealth[status.Role] = *status
}

func (m conversationModel) providerWarning() string {
	var messages []string
	for _, role := range []string{"brain", "speech"} {
		status := m.providerHealth[role]
		if status.State == "unavailable" || status.State == "retrying" {
			messages = append(messages, status.Message)
		}
	}
	return strings.Join(messages, " · ")
}
