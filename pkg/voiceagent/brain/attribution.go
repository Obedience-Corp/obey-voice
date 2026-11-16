package brain

import "strings"

// SpeakerNames resolves a stable speaker id (speaker-1) to a display label for
// model prompts. Implementations may return the id itself when no rename is set.
// A nil names map means ids are shown as-is.
type SpeakerNames interface {
	Display(id string) string
}

// unidentifiedSpeaker is the live analyzer's label for a voice that matched no
// enrolled speaker. It names nobody, so a model must never read it as a name.
const unidentifiedSpeaker = "unknown"

// PromptSpeaker returns the speaker id a model may see for a user turn, or ""
// when the speaker is absent or unidentified. Every prompt surface resolves
// user attribution through this, so an unidentified turn reads as no speaker.
func PromptSpeaker(speakerID string) string {
	speakerID = strings.TrimSpace(speakerID)
	if strings.EqualFold(speakerID, unidentifiedSpeaker) {
		return ""
	}
	return speakerID
}

// UserSpeakerLabel returns the left-hand role used for a user turn in prompts.
// No (or an unidentified) speaker → "User". With a rename, the human name alone
// is preferred so the model addresses people naturally; without one, the
// stable id is used.
func UserSpeakerLabel(speakerID string, names SpeakerNames) string {
	speakerID = PromptSpeaker(speakerID)
	if speakerID == "" {
		return "User"
	}
	if names != nil {
		if d := strings.TrimSpace(names.Display(speakerID)); d != "" {
			return d
		}
	}
	return speakerID
}

// HumanSpeakerName names the human author of a user turn where a name is
// always written, such as group chat headers and routing data. A renamed
// anonymous id gives its rename and an identified speaker its own name. With
// no identified speaker the turn belongs to the configured user, or the neutral
// "User" when none is configured. An unrenamed anonymous id (speaker-2) names
// nobody known and reads "Unknown".
func HumanSpeakerName(speakerID string, names SpeakerNames, configured string) string {
	speakerID = PromptSpeaker(speakerID)
	if speakerID == "" {
		if configured = strings.TrimSpace(configured); configured != "" {
			return configured
		}
		return "User"
	}
	if names != nil {
		if d := strings.TrimSpace(names.Display(speakerID)); d != "" && d != speakerID && !anonymousSpeakerID(d) {
			return d
		}
	}
	if anonymousSpeakerID(speakerID) {
		return "Unknown"
	}
	return speakerID
}

// anonymousSpeakerID reports a diarization id (speaker-N), which carries no name.
func anonymousSpeakerID(id string) bool {
	n, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(id)), "speaker-")
	if !ok || n == "" {
		return false
	}
	return strings.Trim(n, "0123456789") == ""
}

// FormatUserLine builds "Label: content" for a user turn.
func FormatUserLine(speakerID, content string, names SpeakerNames) string {
	return UserSpeakerLabel(speakerID, names) + ": " + content
}

// AttachSpeakerNames wires a rename table onto providers that support it.
// No-ops for fakes and providers without attribution.
func AttachSpeakerNames(p Provider, names SpeakerNames) {
	type setter interface {
		SetSpeakerNames(SpeakerNames)
	}
	if s, ok := p.(setter); ok {
		s.SetSpeakerNames(names)
	}
}

// promptUserLine formats one history turn for Claude/Grok flatten prompts.
func promptUserLine(t Turn, agentName string, names SpeakerNames) string {
	switch t.Role {
	case "samantha", "assistant":
		if t.PersonaDisplayName != "" {
			agentName = t.PersonaDisplayName
		} else if t.PersonaID != "" {
			agentName = t.PersonaID
		}
		if agentName == "" {
			agentName = "Obey"
		}
		return agentName + ": " + t.Content
	default:
		return FormatUserLine(t.Speaker, t.Content, names)
	}
}
