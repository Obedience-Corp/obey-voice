package brain

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

// loadSkillsCatalog returns the Agent Skills catalog when skills_enabled is
// true; otherwise an empty catalog. Discovery uses the working directory and
// nearest workspace ancestor, then the configured Obey Voice skills_dir.
// Shared ~/.agents/skills is never imported implicitly. .claude/skills is
// intentionally not scanned — Claude Code owns that path. Missing dirs yield
// empty contributions, not errors.
// SkillsCatalogFor exposes the loader the ollama brain uses, so the CLI preview
// renders the same catalog the model sees rather than a re-derived one.
func SkillsCatalogFor(ctx context.Context, cfg *config.Config, workDir string) ([]skills.Skill, error) {
	return loadSkillsCatalog(ctx, cfg, workDir)
}

func loadSkillsCatalog(ctx context.Context, cfg *config.Config, workDir string) ([]skills.Skill, error) {
	if cfg == nil || !cfg.SkillsEnabled {
		return nil, nil
	}
	configured := cfg.SkillsDir
	if configured == "" {
		configured = config.SkillsDir()
	}
	dirs := skills.DefaultSearchPaths(workDir, configured)
	catalog, err := skills.Loader{Dirs: dirs, Disabled: cfg.SkillsDisabled, Builtins: true}.Catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading skills: %w", err)
	}
	return catalog, nil
}

// refreshSystemPrompt re-reads persona YAML before a request. Reassembly
// remains deterministic: unchanged files produce the same system prefix, while
// edits become visible without restarting the conversation.
func (o *OllamaBrain) refreshSystemPrompt(onWarn func(message string)) {
	if o.personaReloader == nil {
		return
	}
	persona, changed, err := o.personaReloader.resolve(o.cfg)
	if err != nil && onWarn != nil {
		onWarn(err.Error())
	}
	if changed {
		o.systemPrompt = persona
	}
	// Equal content preserves the provider's prefix cache. Application YAML is
	// resolved separately for relevant requests by buildMessagesForTurn.
	o.setFullSystemPrompt(assembleSystemPrompt(o.systemPrompt, o.workDir, o.skills, o.turnInstruction, o.cfg))
}

// setFullSystemPrompt publishes a rebuilt prompt to a Warmup that may be
// reading it from its own goroutine.
func (o *OllamaBrain) setFullSystemPrompt(prompt string) {
	o.stateMu.Lock()
	o.fullSystemPrompt = prompt
	o.stateMu.Unlock()
}

// assembleSystemPrompt combines the resolved persona, application YAML,
// environment and skill catalog. Unchanged inputs yield byte-identical output.
func assembleSystemPrompt(personaPrompt, workDir string, catalog []skills.Skill, turn string, cfg *config.Config) string {
	return AssembleSystemPrompt(SystemPromptInput{
		Provider: providerOllama,
		Persona:  personaPrompt,
		WorkDir:  workDir,
		Skills:   catalog,
		Turn:     turn,
		Cfg:      cfg,
	})
}

// buildMessages assembles one chat request: the resolved system prompt, the
// full retained history (trimHistory owns the bound), and — when skills
// activated for this turn — their context spliced onto the latest user
// message. Splicing at the tail keeps every earlier token byte-identical
// across requests, so the server's prefix cache re-processes only the new
// turn instead of the whole transcript.
func (o *OllamaBrain) buildMessages(skillCtx string) []api.Message {
	return o.buildMessagesForTurn(skillCtx, false)
}

func (o *OllamaBrain) buildMessagesForTurn(skillCtx string, agentContinuation bool) []api.Message {
	msgs := make([]api.Message, 0, len(o.history)+1)
	system := o.fullSystemPrompt + o.conversationIdentityGuidance()
	lastUser := lastUserIndex(o.history)
	if !agentContinuation && lastUser >= 0 {
		_, input := decodeOllamaUser(o.history[lastUser].Content)
		if app := ApplicationRequestContext(o.cfg, providerOllama, input); app != "" {
			system += "\n\n" + app
		}
	}
	if agentContinuation {
		system += "\n" + systemPrompt(o.cfg, "peer-reply", nil)
	}
	msgs = append(msgs, api.Message{Role: "system", Content: system})
	recall := o.recallNotes()
	for i, m := range o.history {
		if i == lastUser && recall != "" {
			msgs = append(msgs, recallMessages(recall)...)
		}
		if m.Role == "user" {
			m.Content = o.formatStoredUser(m.Content)
		}
		if i == lastUser && skillCtx != "" {
			m.Content += "\n" + skillCtx
		}
		if m.Role == ollamaSharedRole || m.Role == ollamaSharedToolRole {
			m.Role = "user"
		}
		// Keep the selected responder explicit beside the newest input. Small
		// models otherwise continue the last quoted agent's voice even with a
		// correct group roster in the leading system message. This stays on the
		// request projection, never in canonical user text.
		if ((i == lastUser && !agentContinuation) || (agentContinuation && i == len(o.history)-1)) && m.Role == "user" && o.groupModeEnabled() && o.cfg != nil {
			m.Content += groupReplyInstruction(o.cfg, o.cfg.AgentName)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// Warmup and live requests must share the same stable identity prefix.
func (o *OllamaBrain) conversationIdentityGuidance() string {
	identity := "\n" + systemPrompt(o.cfg, "conversation-history", map[string]string{"agent_name": currentAgentName(o.cfg)})
	if o.groupModeEnabled() {
		identity += o.groupConversationGuidance()
	}
	return identity
}

func (o *OllamaBrain) groupConversationGuidance() string {
	own := "the current persona"
	if o.cfg != nil && strings.TrimSpace(o.cfg.AgentName) != "" {
		own = strings.TrimSpace(o.cfg.AgentName)
	}
	o.stateMu.Lock()
	roster := cloneGroupParticipants(o.groupParticipants)
	speakers := append([]string(nil), o.groupSpeakers...)
	o.stateMu.Unlock()
	return groupConversationContext(o.cfg, own, roster, speakers)
}

func (o *OllamaBrain) routeSkillContext(ctx context.Context, input string, onStart func(ToolCall), onEnd func(ToolResult)) string {
	if o.skillRouter == nil {
		return ""
	}
	matched, err := o.skillRouter.Match(ctx, input)
	if err == nil && len(matched) > 0 {
		matched, err = o.confirmSkillMatches(ctx, input, matched)
	}
	if err != nil {
		if !o.skillRouterWarned {
			fmt.Fprintf(os.Stderr, "obey-voice: semantic skill routing failed (%v); continuing with the Agent Skills catalog\n", err)
			o.skillRouterWarned = true
		}
		return ""
	}
	if len(matched) > 0 {
		names := make([]string, len(matched))
		for i, skill := range matched {
			names[i] = skill.Name
		}
		if onStart != nil || onEnd != nil {
			o.skillCallSeq++
			call := ToolCall{
				ID:      fmt.Sprintf("activate_skill#%d", o.skillCallSeq),
				Name:    "activate_skill",
				Kind:    KindSkill,
				Summary: strings.Join(names, ", "),
			}
			if onStart != nil {
				onStart(call)
			}
			if onEnd != nil {
				preview := fmt.Sprintf("injected %d skill(s): %s", len(names), strings.Join(names, ", "))
				onEnd(ToolResult{ToolCall: call, Preview: preview})
			}
		}
	}
	return activatedSkillContext(o.cfg, matched)
}
