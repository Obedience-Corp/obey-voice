package brain

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// currentRequestContext belongs only to a new human conversational request.
// History is shared across personas, but selecting one does not assign it the
// unfinished requests in that history. Internal turns retain their own prompt
// contracts, and workers receive their explicit task brief instead.
func currentRequestContext(cfg *config.Config, opts StreamOptions) string {
	if !hasCurrentRequest(cfg, opts) {
		return ""
	}
	return "\n\n" + systemPrompt(cfg, "current-request", map[string]string{"agent_name": currentAgentName(cfg)})
}

func currentRequestReminder(cfg *config.Config, opts StreamOptions) string {
	if !hasCurrentRequest(cfg, opts) {
		return ""
	}
	return "\n\n" + systemPrompt(cfg, "current-request-reply", map[string]string{"agent_name": currentAgentName(cfg)})
}

func hasCurrentRequest(cfg *config.Config, opts StreamOptions) bool {
	if cfg == nil || strings.TrimSpace(cfg.ActivePersona) == "" {
		return false
	}
	if opts.Background || opts.AgentContinuation || opts.TransientHistory || opts.OmitTurnInstruction || opts.Discussion != nil ||
		cfg.IsWorker {
		return false
	}
	return true
}
