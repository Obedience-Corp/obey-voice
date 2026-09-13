package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
)

// ThinkFull sends input and waits for the complete response.
func (o *OllamaBrain) ThinkFull(ctx context.Context, input string, opts StreamOptions) (string, error) {
	skillCtx := o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	// A failed turn rolls back to this snapshot rather than truncating: the tool
	// loop appends assistant/tool turns and the context budget can trim the
	// front, so length alone cannot describe the pre-call history. Unlike
	// ThinkStream — which records a recovery reply and keeps the turn — a failed
	// ThinkFull answers nothing, so its input must not stay behind.
	restore := append([]api.Message(nil), o.history...)
	restoreCanonical := deepCopyTurns(o.canonical)
	if opts.TransientHistory {
		defer func() { o.history = restore; o.canonical = restoreCanonical }()
	}
	if !opts.Background {
		o.appendUserInput(input, opts)
	}
	if !opts.TransientHistory {
		o.receiveDelegationResults(opts)
	}
	if opts.Background {
		skillCtx += "\nReport the newly arrived background task result briefly. Do not answer earlier requests again or start more tools."
	}
	skillCtx += WorkResultsPrompt(opts.WorkResults)
	skillCtx += WorkerFactsPrompt(opts.WorkerFacts)
	o.ensureContextBudget(skillCtx)

	sess := &toolSession{
		catalog:        o.skills,
		routerPolicy:   opts.effectiveRouterPolicy(),
		workerTiers:    opts.WorkerTiers,
		delegation:     o.delegationForTurn(opts),
		commandTimeout: time.Duration(config.ClampToolCommandTimeout(o.cfg.ToolCommandTimeout)) * time.Second,
		boundary:       NewBoundary(o.cfg.WorkerBoundary),
		onStart:        opts.OnToolStart,
		onEnd:          opts.OnToolEnd,
	}

	hadThinking := false
	hadTools := false
	for i := 0; i < maxToolIterations; i++ {
		var tools api.Tools
		if opts.ToolsEnabled {
			tools = sess.tools()
		}

		if err := o.contextBudgetError(skillCtx, tools); err != nil {
			o.history = restore
			o.canonical = restoreCanonical
			return "", err
		}
		messages := o.buildMessages(skillCtx)
		if opts.OmitTurnInstruction {
			messages = o.compactionMessages(skillCtx)
		}
		req := o.newChatRequest(messages, tools)
		stream := false
		req.Stream = &stream

		var response api.Message
		var doneReason string
		err := o.chat(ctx, req, func(resp api.ChatResponse) error {
			response = resp.Message
			doneReason = resp.DoneReason
			return nil
		})
		if err != nil {
			o.history = restore
			o.canonical = restoreCanonical
			return "", fmt.Errorf("ollama error: %w", err)
		}

		if doneReason == "length" {
			o.history = restore
			o.canonical = restoreCanonical
			return "", fmt.Errorf("ollama full response truncated")
		}

		// Tool calls — execute and loop (skills stack on full CLI tools).
		if len(response.ToolCalls) > 0 {
			hadTools = true
			hadThinking = hadThinking || messageHadThinking(response)
			o.appendMessage(api.Message{
				Role:      "assistant",
				Content:   stripReplyConventions(o.cfg.AgentName, response.Content),
				ToolCalls: response.ToolCalls,
			}, opts.Identity)

			for _, tc := range response.ToolCalls {
				result := sess.execute(ctx, o.workDir, tc)
				// read_skill is the established instruction-loading path. Preserve its
				// full body in this turn's transient skill splice, not persisted history.
				if tc.Function.Name == "read_skill" && o.cfg.ContinuityProjectionEnabled {
					skillCtx += "\n" + result
				}
				o.appendProjectedToolResult(tc, result)
			}
			o.ensureContextBudget(skillCtx)
			continue
		}

		hadThinking = hadThinking || messageHadThinking(response)
		// Same empty-content table as ThinkStream: thinking-only empty stays
		// empty so /compact is not replaced by the recovery line.
		text := cleanForVoice(stripReplyConventions(o.cfg.AgentName, response.Content))
		if fallbackEmptyContent(text, hadTools, hadThinking) {
			text = spokenOrFallback(text)
		}
		o.appendMessage(api.Message{Role: "assistant", Content: text}, opts.Identity)
		if !opts.Background && !opts.TransientHistory {
			o.ackDelegationResults(req.Messages)
		} else if !opts.TransientHistory {
			o.markBackgroundDelivered(req.Messages)
		}
		o.trimHistory()
		return text, nil
	}

	return "I seem to be going in circles with my tools. Let me just answer directly.", nil
}
