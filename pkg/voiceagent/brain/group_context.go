package brain

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// groupContext is supplied by the persona host on response admission. CLI
// providers include it even on resumed sessions. Like history, roster changes
// are serialized with requests by the host.
type groupContext struct{ participants []GroupParticipant }

func (g *groupContext) SetGroupParticipants(participants []GroupParticipant) {
	g.participants = cloneGroupParticipants(participants)
}

func (g *groupContext) prompt(cfg *config.Config, own string, turns []Turn) string {
	return g.promptWith(cfg, own, nil, turns)
}

// promptWith also names speakers whose replies turns no longer hold.
func (g *groupContext) promptWith(cfg *config.Config, own string, speakers []string, turns []Turn) string {
	var names []string
	for _, name := range append(slices.Clone(speakers), groupAgentNames(turns)...) {
		names = appendGroupName(names, name)
	}
	group := len(g.participants) > 1
	for _, name := range names {
		if !strings.EqualFold(name, own) {
			group = true
		}
	}
	if !group {
		return ""
	}
	return groupConversationContext(cfg, own, g.participants, names)
}

// GroupSpeakers is the display names of the personas with replies in turns.
func GroupSpeakers(turns []Turn) []string { return groupAgentNames(turns) }

// Catalog availability does not establish who has spoken. Only recorded public
// display names do; raw storage IDs never become participant names.
func groupAgentNames(turns []Turn) []string {
	var names []string
	for _, turn := range turns {
		if (turn.Role == "assistant" || turn.Role == "samantha") && strings.TrimSpace(turn.Content) != "" {
			names = appendGroupName(names, turn.PersonaDisplayName)
		}
	}
	return names
}
func appendGroupName(names []string, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return names
	}
	for _, existing := range names {
		if strings.EqualFold(existing, name) {
			return names
		}
	}
	return append(names, name)
}
func groupConversationContext(cfg *config.Config, own string, roster []GroupParticipant, spoken []string) string {
	if strings.TrimSpace(own) == "" {
		own = currentAgentName(cfg)
	}
	var available []string
	for _, participant := range roster {
		if !strings.EqualFold(strings.TrimSpace(participant.Name), own) {
			available = appendGroupName(available, participant.Name)
		}
	}
	values := map[string]string{"agent_name": own, "available_context": "", "spoken_context": ""}
	if len(available) > 0 {
		encoded, _ := json.Marshal(available)
		values["available_context"] = systemPrompt(cfg, "group-available", map[string]string{"available_agents": string(encoded)})
	}
	if len(spoken) > 0 {
		encoded, _ := json.Marshal(spoken)
		values["spoken_context"] = systemPrompt(cfg, "group-spoken", map[string]string{"spoken_agents": string(encoded)})
	}
	return "\n" + systemPrompt(cfg, "group-chat", values)
}

func groupReplyInstruction(cfg *config.Config, own string) string {
	if strings.TrimSpace(own) == "" {
		return ""
	}
	return "\n\n" + systemPrompt(cfg, "group-reply", map[string]string{"agent_name": own})
}
