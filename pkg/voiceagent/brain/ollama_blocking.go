package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// ThinkFull sends input and waits for the complete response.
func (o *OllamaBrain) ThinkFull(ctx context.Context, input string, opts StreamOptions) (answer string, resultErr error) {
	o.refreshSystemPrompt(opts.OnPromptWarn)
	// Registered before rollback defers so the final budget reflects retained
	// history even after a failed or transient request.
	defer func() { o.maybeEmitContinuityBudget("") }()
	ctx = opts.workerBudget().Context(ctx)
	responseLimit := newResponseBudget(o.cfg, opts)
	ctx = withResponseBudget(ctx, responseLimit)
	if err := o.prepareAudioHistory(ctx, opts); err != nil {
		return "", err
	}
	if err := o.prepareImageHistory(ctx, opts.Attachments); err != nil {
		return "", err
	}
	priorGroupReply := ""
	if opts.Discussion != nil {
		priorGroupReply = o.previousReplyForRepetition(opts)
	}
	skillCtx := ""
	if opts.Discussion == nil && !opts.AgentContinuation {
		skillCtx = o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	} else {

		opts.ToolsEnabled = false
	}
	// A failed turn rolls back to this snapshot rather than truncating: the tool
	// loop appends assistant/tool turns and the context budget can trim the
	// front, so length alone cannot describe the pre-call history. Unlike
	// ThinkStream — which records a recovery reply and keeps the turn — a failed
	// ThinkFull answers nothing, so its input must not stay behind.
	restore := append([]api.Message(nil), o.history...)
	restoreCanonical := deepCopyTurns(o.canonical)
	restoreWorking := deepCopyTurns(o.working)
	defer func() {
		if resultErr != nil {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
		}
	}()
	if opts.TransientHistory {
		defer func() {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
		}()
	}
	if !opts.Background && !opts.AgentContinuation && opts.Discussion == nil {
		if err := o.appendUserInput(input, opts); err != nil {
			return "", err
		}
	}
	if !opts.TransientHistory && !opts.AgentContinuation && opts.Discussion == nil {
		o.receiveDelegationResults(opts)
	}
	if opts.Background && !opts.AgentContinuation {
		skillCtx += "\nReport the newly arrived background task result briefly. Do not answer earlier requests again or start more tools."
	}
	skillCtx += WorkResultsPrompt(opts.WorkResults)
	skillCtx += WorkerFactsPrompt(opts.WorkerFacts)
	o.ensureContextBudget(skillCtx)
	o.maybeEmitContinuityBudget(skillCtx)

	sess := &toolSession{
		budget:         opts.workerBudget(),
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
	routedChat := false
	imageResultRetry := false
	routeRetried := false
	continuations := 0
	for i := 0; i <= maxToolIterations; i++ {
		finalAnswer := i == maxToolIterations
		var tools api.Tools
		if opts.ToolsEnabled && !routedChat && !finalAnswer {
			tools = sess.tools()
		}

		if err := o.prepareWorkerContext(ctx, skillCtx, tools, opts); err != nil {
			return "", err
		}
		if err := o.contextBudgetError(skillCtx, tools); err != nil {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
			return "", err
		}
		messages := o.buildMessagesForTurn(skillCtx, opts.AgentContinuation && opts.Discussion == nil)
		if imageResultRetry {
			messages[0].Content += "\n\n" + systemPrompt(o.cfg, "image-result-repair", nil)
		}
		if opts.Discussion != nil {
			messages[0].Content += opts.Discussion.Prompt()
			// A compacted working projection can contain only assistant summaries.
			// Reuse the actual initiating request as a transient model input, without
			// recording a second human turn or inventing an agent handoff.
			hasUser := false
			for _, m := range messages {
				hasUser = hasUser || m.Role == "user"
			}
			if !hasUser {
				messages = append(messages, api.Message{Role: "user", Content: opts.Discussion.Original})
			}
		}
		if opts.OmitTurnInstruction {
			messages = o.compactionMessages(skillCtx)
		}
		if finalAnswer {
			messages = append(messages, api.Message{Role: "user", Content: toolLimitAnswerInstruction})
		}
		if err := o.messagesBudgetError(messages, tools); err != nil {
			return "", err
		}
		req := o.newChatRequest(messages, tools)
		stream := false
		req.Stream = &stream
		if opts.Discussion != nil {
			if req.Options == nil {
				req.Options = map[string]any{}
			}
			req.Options["num_predict"] = discussion.MaxReplyTokens
			req.Think = &api.ThinkValue{Value: false}
		}
		if err := o.applyOutputBudget(req, opts.workerBudget()); err != nil {
			return "", err
		}
		if err := responseLimit.apply(req); err != nil {
			return "", err
		}

		chat := o.chat
		if sess.routerPolicy == RouterToolsRouteOnly && len(tools) > 0 && routeRetried && !imageResultRetry {
			chat = o.chatRouteRecovery
		}
		response, responseComplete, err := o.chatFullWithContinuation(ctx, req, opts, responseLimit, &continuations, chat)
		if err != nil {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
			return "", fmt.Errorf("ollama error: %w", err)
		}

		var thoughtFilter textclean.ThinkFilter
		speakable, _ := thoughtFilter.FeedSplit(response.Content)
		tail, _ := thoughtFilter.FlushSplit()
		if _, err := textclean.StripToolMarkup(speakable + tail); err != nil {
			return "", fmt.Errorf("ollama reply: %w", err)
		}
		routing := sess.routerPolicy == RouterToolsRouteOnly && len(tools) > 0
		missingRoute := routing && len(response.ToolCalls) == 0
		checkImageResult := routing || imageResultRetry || (routedChat && imageResultNoun.MatchString(input))
		missingImageResult := checkImageResult && len(response.ToolCalls) == 0 && o.unsupportedImageResult(response.Content, input)
		if missingRoute || missingImageResult {
			if routeRetried || routedChat {
				if missingImageResult {
					return "", fmt.Errorf("no image was attached: the model returned an image claim without a file or tool call")
				}
				return "", fmt.Errorf("model did not select a route; no work was dispatched")
			}
			routeRetried = true
			imageResultRetry = missingImageResult
			if missingImageResult {
				skillCtx += "\n" + missingRouteInstruction
			}
			continue
		}
		// Tool calls — execute and loop (skills stack on full CLI tools).
		if (routedChat || finalAnswer) && len(response.ToolCalls) > 0 {
			return "", fmt.Errorf("tools-free answer returned unexpected tool calls")
		}
		if opts.Discussion != nil && len(response.ToolCalls) > 0 {
			return "", fmt.Errorf("tools are disabled for group discussion")
		}
		if len(response.ToolCalls) > 0 {
			hadTools = true
			hadThinking = hadThinking || messageHadThinking(response)
			if sess.routerPolicy == RouterToolsRouteOnly {
				// The live classifier's tool preamble is planning, not an
				// assistant reply to replay into later conversation context.
				response.Content = ""
				if hasRouteCall(response.ToolCalls) {
					routedChat = true
					skillCtx += "\n" + chatRouteAnswerInstruction
				}
			}
			o.appendMessage(api.Message{
				Role:      "assistant",
				Content:   stripReplyConventions(o.cfg.AgentName, response.Content),
				ToolCalls: response.ToolCalls,
			}, opts.Identity)

			for _, tc := range response.ToolCalls {
				result := sess.execute(ctx, o.workDir, tc)
				if sess.limitErr != nil {
					return "", sess.limitErr
				}
				// read_skill is the established instruction-loading path. Preserve its
				// full body in this turn's transient skill splice, not persisted history.
				if tc.Function.Name == "read_skill" && o.cfg.ContinuityProjectionEnabled {
					skillCtx += "\n" + result
				}
				o.appendProjectedToolResult(tc, result, opts.Identity)
			}
			o.ensureContextBudget(skillCtx)
			continue
		}

		hadThinking = hadThinking || messageHadThinking(response)
		// Same empty-content table as ThinkStream: thinking-only empty stays
		// empty so /compact is not replaced by the recovery line.
		raw := stripReplyConventions(o.cfg.AgentName, response.Content)
		text := cleanForVoice(stripHistoricalAssistantMarkers(raw))
		if finalAnswer && (!responseComplete || !hasSpeakableContent(text)) {
			return "", fmt.Errorf("tool round limit reached: model returned no final answer")
		}
		if opts.Discussion != nil && !hasSpeakableContent(text) {
			return "", fmt.Errorf("group reply was empty")
		}
		if fallbackEmptyContent(text, hadTools, hadThinking) {
			text = spokenOrFallback(text)
		}
		if opts.Discussion != nil && priorGroupReply != "" && strings.HasPrefix(normalizeReplyPrefix(text), priorGroupReply) {
			return "", fmt.Errorf("group reply repeated a prior answer")
		}
		recorded := text
		if legacyHistoricalAssistantHeader.MatchString(raw) && hasSpeakableContent(text) {
			recorded = raw
		}
		if opts.Discussion != nil && opts.Discussion.BeforeOutput != nil {
			if err := opts.Discussion.BeforeOutput(ctx); err != nil {
				return "", err
			}
		}
		o.appendMessage(api.Message{Role: "assistant", Content: recorded}, opts.Identity)
		if !opts.Background && !opts.AgentContinuation && opts.Discussion == nil && !opts.TransientHistory {
			o.ackDelegationResults(req.Messages)
		} else if opts.Background && !opts.TransientHistory {
			o.markBackgroundDelivered(req.Messages)
		}
		o.trimHistory()
		return text, nil
	}

	return "", fmt.Errorf("tool round limit reached without a final answer")
}
