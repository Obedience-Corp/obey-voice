package brain

import (
	"encoding/json"
	"strings"
)

// groupContext is supplied by the persona host on response admission. CLI
// providers include it even on resumed sessions. Like history, roster changes
// are serialized with requests by the host.
type groupContext struct{ participants []GroupParticipant }

func (g *groupContext) SetGroupParticipants(participants []GroupParticipant) {
	g.participants = cloneGroupParticipants(participants)
}

func (g *groupContext) prompt(own string, turns []Turn) string {
	names := groupAgentNames(turns)
	group := len(g.participants) > 1
	for _, name := range names {
		if !strings.EqualFold(name, own) {
			group = true
		}
	}
	if !group {
		return ""
	}
	return groupConversationContext(own, g.participants, names)
}

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
func groupConversationContext(own string, roster []GroupParticipant, spoken []string) string {
	own = strings.TrimSpace(own)
	if own == "" {
		own = "the current persona"
	}
	text := "\nThis is a group chat already taking place inside Obey Voice, with the human user and independently configured agent personas. You are " + own + ". Each agent has its own identity and context; the supplied shared transcript records what others said. Their replies are not your own, and selecting you does not start a new conversation."
	var available []string
	for _, participant := range roster {
		if !strings.EqualFold(strings.TrimSpace(participant.Name), own) {
			available = appendGroupName(available, participant.Name)
		}
	}
	if len(available) > 0 {
		encoded, _ := json.Marshal(available)
		text += " Other available agent personas (display names, not evidence that all have spoken): " + string(encoded) + "."
	}
	if len(spoken) > 0 {
		encoded, _ := json.Marshal(spoken)
		text += " Agents with replies recorded in this thread: " + string(encoded) + "."
	}
	return text + " Use this thread's context when the user says this chat, the group, or the other agents. A request to demonstrate the current conversation is a request to participate here; do not turn it into a setup or persona-creation task unless the user asks for that. Speech transcription may mishear names or multi-agent terminology; use the conversation and the user's later clarification instead of repeatedly correcting an earlier mishearing. Answer only as yourself. You may deliberately address a direct question to an available named agent to hand them the floor; a third-person mention does not do that. Do not fabricate another agent's reply or claim access to their private thoughts. Do not speak labels, roster metadata, or internal role markers."
}

func groupReplyInstruction(own string) string {
	if strings.TrimSpace(own) == "" {
		return ""
	}
	return "\n\n[Host conversation context: the current responder is " + own + ". Give only " + own + "'s next reply in this existing group chat. Do not write another participant's lines or repeat speaker labels.]"
}
