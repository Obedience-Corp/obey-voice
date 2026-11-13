package brain

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
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

func (o *OllamaBrain) buildMessagesForTurn(skillCtx string, agentContinuation bool, options ...StreamOptions) []api.Message {
	msgs := make([]api.Message, 0, len(o.history)+1)
	system := o.fullSystemPrompt + o.conversationIdentityGuidance()
	opts := StreamOptions{AgentContinuation: agentContinuation}
	if len(options) > 0 {
		opts = options[0]
	}
	system += currentRequestContext(o.cfg, opts)
	lastUser := lastUserIndex(o.history)
	if o.audioResolver != nil && lastUser >= 0 && lastUser < len(o.working) && o.working[lastUser].Utterance != nil && len(o.history[lastUser].Images) > 0 {
		system += "\n\nThe user request is in the attached recording. Answer the speaker's request. The persona examples illustrate style; do not repeat them or claim an action unless its tool result confirms it."
	}
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
	// shared marks the other speakers' turns and current the message being
	// answered, both by position in msgs.
	shared, current := map[int]bool{}, map[int]bool{}
	recall := o.recallNotes()
	for i, m := range o.history {
		if i == lastUser && recall != "" {
			msgs = append(msgs, recallMessages(recall)...)
		}
		if m.Role == ollamaSharedRole || m.Role == ollamaSharedToolRole {
			shared[len(msgs)] = true
		}
		if i == lastUser || (agentContinuation && i == len(o.history)-1) {
			current[len(msgs)] = true
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
		// Place the human request boundary beside the actual input as well as
		// in system context. Never add competing text to a native recording.
		if len(options) > 0 && i == lastUser && !(o.audioResolver != nil && opts.Utterance != nil) {
			m.Content += currentRequestReminder(o.cfg, opts)
		}
		msgs = append(msgs, m)
	}
	return blockSharedContext(msgs, shared, current)
}

// sharedContextHeader opens a block of other speakers' turns.
const sharedContextHeader = "Earlier in this conversation (context, not requests for you):\n\n"

// blockSharedContext joins each run of consecutive user-role messages that
// holds another speaker's turn into one message, leaving the message being
// answered on its own.
//
// Other personas' replies reach a model as user-role text, so a persona that
// joins a long group chat would otherwise be sent dozens of user messages in a
// row with no reply of its own between them. A small model continues that
// pattern instead of answering: replayed against lfm3 on a 76-turn chat, the
// one-message-per-turn form ran to the output limit six times in six, copying
// the other speakers, and the same turns as one block followed by the current
// message drew a short in-character reply twelve times in twelve.
//
// A block only grows at its end, so earlier requests stay a prefix of later
// ones. A conversation with no other speaker is left exactly as it was.
func blockSharedContext(msgs []api.Message, shared, current map[int]bool) []api.Message {
	if len(shared) == 0 {
		return msgs
	}
	out := make([]api.Message, 0, len(msgs))
	for i := 0; i < len(msgs); {
		j, hasShared := i, false
		for j < len(msgs) && msgs[j].Role == "user" && len(msgs[j].Images) == 0 && !current[j] {
			hasShared = hasShared || shared[j]
			j++
		}
		if j-i < 2 || !hasShared {
			j = max(j, i+1)
			out = append(out, msgs[i:j]...)
			i = j
			continue
		}
		parts := make([]string, 0, j-i)
		for _, m := range msgs[i:j] {
			parts = append(parts, m.Content)
		}
		out = append(out, api.Message{Role: "user", Content: sharedContextHeader + strings.Join(parts, "\n\n")})
		i = j
	}
	return out
}

// Warmup and live requests must share the same stable identity prefix.
func (o *OllamaBrain) conversationIdentityGuidance() string {
	identity := "\n" + systemPrompt(o.cfg, "conversation-history", map[string]string{"agent_name": currentAgentName(o.cfg)})
	if o.cfg != nil && strings.TrimSpace(o.cfg.UserDisplayName) != "" {
		identity += "\nConfigured human user: " + strings.TrimSpace(o.cfg.UserDisplayName) + "; a named message speaker takes precedence."
	}
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
	handed := o.handedSkills(ctx)
	var matched []skills.Skill
	if o.skillRouter != nil {
		var err error
		matched, err = o.skillRouter.Match(ctx, input)
		if err == nil && len(matched) > 0 {
			matched, err = o.confirmSkillMatches(ctx, input, matched)
		}
		if err != nil {
			if !o.skillRouterWarned {
				fmt.Fprintf(os.Stderr, "obey-voice: semantic skill routing failed (%v); continuing with the Agent Skills catalog\n", err)
				o.skillRouterWarned = true
			}
			matched = nil
		}
	}
	matched = mergeSkills(handed, matched)
	if o.cfg == nil || !o.cfg.IsWorker {
		names := make([]string, len(matched))
		for i, skill := range matched {
			names[i] = skill.Name
		}
		o.rememberActivatedSkills(names)
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
				onEnd(ToolResult{ToolCall: call, Preview: preview, Outcome: &events.ToolOutcome{
					Status: events.ToolOutcomeSucceeded, InputSummary: call.Summary, OutputPreview: preview,
				}})
			}
		}
	}
	return activatedSkillContext(o.cfg, matched)
}
