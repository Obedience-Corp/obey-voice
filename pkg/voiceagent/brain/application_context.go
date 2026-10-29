package brain

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

// ApplicationContext renders the editable system/application YAML document.
func ApplicationContext(cfg *config.Config) string {
	return systemPrompt(cfg, "application", map[string]string{"calibre_enabled": strconv.FormatBool(cfg != nil && cfg.CalibreEnabled)})
}

var applicationName = regexp.MustCompile(`(?i)\bobey[- ]voice\b`)
var applicationOperation = regexp.MustCompile(`(?i)\b(configure|configuration|settings|capabilities|features|support|supports|built-in|built in|how|can|create|convert|enable|disable|change|edit|set|setup|install|list|show|render|narrate|audiobook|calibre|persona|command|help)\b`)
var documentAudioRequest = regexp.MustCompile(`(?i)\b(convert|render|create|make|narrate|read|generate|turn)\b`)
var documentSource = regexp.MustCompile(`(?i)\b(ebook|epub|pdf|calibre|markdown|html|text|url)\b|\.(md|txt|html?)\b|\bhttps?://`)
var implementationRequest = regexp.MustCompile(`(?i)\b(implement|build|code|script|api|app|project|website|service)\b`)
var audioOutput = regexp.MustCompile(`(?i)\b(audio|audiobook|aloud|speech)\b`)

// ApplicationRequestContext is a request projection, never session grounding or
// canonical history. Only the current input selects it: a previous app task must
// not keep injecting app instructions after the user changes topics.
func ApplicationRequestContext(cfg *config.Config, provider, input string) string {
	relevant := applicationName.MatchString(input) && applicationOperation.MatchString(input)
	relevant = relevant || (documentAudioRequest.MatchString(input) && documentSource.MatchString(input) && audioOutput.MatchString(input) && !implementationRequest.MatchString(input))
	if !relevant {
		return ""
	}
	text := ApplicationContext(cfg)
	if !SkillMenuApplies(provider) && applicationSkillEnabled(cfg) {
		text += "\n\n" + skills.ApplicationInstructions()
	}
	return strings.TrimSpace(text)
}
