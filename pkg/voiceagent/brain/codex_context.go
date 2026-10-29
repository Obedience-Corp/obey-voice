package brain

import "encoding/json"

// Replay is data, not native tool traffic. Preserve provenance across provider
// switches without sending internal response IDs or attributing old assistants
// to the currently selected persona.
func cliHistoryRecord(t Turn, names SpeakerNames) string {
	record := struct {
		Role       string         `json:"role"`
		Content    string         `json:"content"`
		Speaker    string         `json:"speaker,omitempty"`
		Persona    string         `json:"persona,omitempty"`
		ToolCalls  []TurnToolCall `json:"tool_calls,omitempty"`
		ToolName   string         `json:"tool_name,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
	}{Role: t.Role, Content: t.Content, ToolCalls: t.ToolCalls, ToolName: t.ToolName, ToolCallID: t.ToolCallID}
	if t.Role == "user" {
		record.Speaker = UserSpeakerLabel(t.Speaker, names)
	}
	record.Persona = t.PersonaDisplayName
	if record.Persona == "" {
		record.Persona = t.PersonaID
	}
	if t.Role == "samantha" {
		record.Role = "assistant"
		if record.Persona == "" {
			record.Persona = "Samantha"
		}
	}
	// This struct contains only strings and slices of string-only structs.
	data, _ := json.Marshal(record)
	return string(data)
}
