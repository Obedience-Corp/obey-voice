package brain

import (
	"fmt"
	"os"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

func systemPrompt(cfg *config.Config, name string, values map[string]string) string {
	dir := ""
	if cfg != nil {
		dir = config.PromptsDirFrom(cfg)
	}
	text, err := prompts.RenderSystem(dir, name, values)
	if err != nil {
		fmt.Fprintf(os.Stderr, "obey-voice: %v\n", err)
	}
	return text
}

func currentAgentName(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.AgentName) != "" {
		return strings.TrimSpace(cfg.AgentName)
	}
	return "assistant"
}
