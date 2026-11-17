package brain

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/ollama/ollama/api"
)

func (o *OllamaBrain) projectTurn(t Turn, all []Turn, index int, sharedUnowned bool) api.Message {
	return o.projectTurnWithAudio(t, all, index, sharedUnowned, o.audioProjection)
}

func (o *OllamaBrain) projectTurnWithAudio(t Turn, all []Turn, index int, sharedUnowned bool, projection map[string][]byte) api.Message {
	t = o.effectiveTurn(t)
	role := t.Role
	if role == "samantha" {
		role = "assistant"
	}
	if role == "user" {
		content := t.Content
		if t.RoutedInput != "" {
			content = t.RoutedInput
		}
		if strings.TrimSpace(t.Speaker) != "" {
			content = encodeOllamaUser(t.Speaker, content)
		}
		images, _ := loadImageData(t.Attachments) // Validated before a model request; assets are immutable.
		if t.Utterance != nil && t.Utterance.Audio != nil {
			if data := projection[t.Utterance.Audio.ID]; data != nil {
				images = []api.ImageData{data}
				content = t.TypedInput
			}
			if len(images) == 0 && t.Utterance.Transcript.State == utterance.Ready {
				content = audioTranscriptProjection(t)
			}
		}
		return api.Message{Role: "user", Content: content, Images: images}
	}
	owner, name := strings.TrimSpace(t.PersonaID), strings.TrimSpace(t.PersonaDisplayName)
	legacy := role == "assistant" && legacyHistoricalAssistantHeader.MatchString(t.Content)
	if role == "tool" && owner == "" {
		owner, name, legacy = precedingToolOwner(all, index)
	} else if role == "tool" {
		_, _, legacy = precedingToolOwner(all, index)
	}
	current := o.activePersona()
	// Unbound providers cannot claim an attributed reply as their own.
	own := owner == current
	if current == "" && o.groupModeEnabled() {
		own = false
	}
	// Contaminated replies and synthetic summaries are always quoted.
	shared := !own || legacy || (sharedUnowned && index == 0 && owner == "")
	content := modelTurnContent(t)
	if role == "assistant" {
		content = stripLegacyHistoricalHeader(content)
	}
	if shared && role == "assistant" {
		for _, tc := range t.ToolCalls {
			content += "\nTool call: " + tc.Name + "(" + tc.Arguments + ")"
		}
		if sharedUnowned && owner == "" {
			return api.Message{Role: ollamaSharedRole, Content: sharedContextContent(content)}
		}
		return api.Message{Role: ollamaSharedRole, Content: attributedAssistantContent(name, content)}
	}
	if shared && role == "tool" {
		return api.Message{Role: ollamaSharedToolRole, Content: attributedToolContent(name, t.ToolName, content)}
	}
	m := api.Message{Role: role, Content: content, ToolName: t.ToolName, ToolCallID: t.ToolCallID}
	for _, tc := range t.ToolCalls {
		args := api.NewToolCallFunctionArguments()
		if strings.TrimSpace(tc.Arguments) != "" {
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
		}
		m.ToolCalls = append(m.ToolCalls, api.ToolCall{ID: tc.ID, Function: api.ToolCallFunction{Name: tc.Name, Arguments: args}})
	}
	return m
}

func precedingToolOwner(turns []Turn, index int) (string, string, bool) {
	for i := index - 1; i >= 0; i-- {
		if turns[i].Role == "user" {
			break
		}
		if turns[i].Role == "assistant" || turns[i].Role == "samantha" {
			for _, call := range turns[i].ToolCalls {
				if call.ID == turns[index].ToolCallID {
					return turns[i].PersonaID, turns[i].PersonaDisplayName, legacyHistoricalAssistantHeader.MatchString(turns[i].Content)
				}
			}
		}
	}
	return "", "", false
}

func attributedAssistantContent(name, content string) string {
	if strings.TrimSpace(name) == "" {
		return "Unknown agent:\n" + content
	}
	return strings.TrimSpace(name) + " agent:\n" + content
}

func attributedToolContent(name, tool, content string) string {
	prefix := "Unknown agent tool"
	if tool != "" {
		prefix += " (" + tool + ")"
	}
	if strings.TrimSpace(name) != "" {
		prefix = strings.TrimSpace(name) + " tool"
		if tool != "" {
			prefix += " (" + tool + ")"
		}
	}
	return prefix + ":\n" + content
}

func sharedContextContent(content string) string { return "Shared conversation context:\n" + content }

// Remove complete attribution markers only from assistant output/projections.
// User quotations and the canonical transcript are left untouched.
var legacyHistoricalAssistantHeader = regexp.MustCompile(`\[Historical assistant: [^\]\r\n]+\](?:\r?\n)?`)

func stripLegacyHistoricalHeader(content string) string {
	return legacyHistoricalAssistantHeader.ReplaceAllString(content, "")
}
