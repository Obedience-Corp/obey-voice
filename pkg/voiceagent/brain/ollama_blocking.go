package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain/ollama"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// ThinkFull sends input and waits for the complete response.
func (o *OllamaBrain) ThinkFull(ctx context.Context, input string, opts StreamOptions) (answer string, resultErr error) {
	o.turnStarted.Store(true)
	if opts.TransientHistory {
		ctx = context.WithValue(ctx, disableOutageRetryKey{}, true)
	} else {
		o.inputRetry = nil
	}
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
	if err := o.prepareImageHistory(ctx, opts); err != nil {
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
	ctl := opts.Control
	defer func() {
		if resultErr != nil {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
			if ctl != nil {
				ctl.restore()
			}
		}
	}()
	if opts.TransientHistory {
		defer func() {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
		}()
	}
	// A worker continued after a correction has no new input of its own: the
	// corrections are taken from the control below.
	continuing := ctl != nil && input == ""
	if !opts.Background && !opts.AgentContinuation && opts.Discussion == nil && !continuing {
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
	skillCtx += WorkerFactsPrompt(o.cfg, opts.WorkerFacts)
	o.ensureContextBudget(skillCtx)
	o.maybeEmitContinuityBudget(skillCtx)

	sess := o.turnToolSession(opts)

	hadThinking := false
	hadTools := false
	routedChat := false
	directImage, owedImage := o.directImageRequest(input, opts)
	sess.imageDelivery = directImage.requiresFile(input)
	var imageRepair string
	routeRetried := false
	continuations := 0
	rounds := newToolRounds(o.cfg, opts)
	for i := 0; rounds.more(i); i++ {
		corrected := ctl != nil && ctl.waiting()
		if corrected {
			rounds.redirect()
		}
		finalAnswer, finalInstruction := rounds.final(ctx, i)
		if corrected {
			o.takeCorrections(ctl, finalAnswer, opts)
		}
		var tools api.Tools
		if opts.ToolsEnabled && !routedChat && !finalAnswer {
			tools = sess.tools()
		}

		if err := o.prepareWorkerContext(ctx, skillCtx, tools, opts); err != nil {
			return "", err
		}
		if ctl != nil && !finalAnswer {
			// Compaction is a model request of its own: a correction may have
			// arrived during it, and it may have spent the last request.
			if ctl.waiting() {
				continue
			}
			if finalAnswer, finalInstruction = rounds.final(ctx, i); finalAnswer {
				tools = nil
			}
		}
		if err := o.contextBudgetError(skillCtx, tools); err != nil {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
			return "", err
		}
		messages := o.buildMessagesForTurn(skillCtx, opts.AgentContinuation && opts.Discussion == nil, opts)
		if imageRepair != "" {
			if directImage != noImageContext {
				appendToNewestUser(messages, "\n\n"+imageRepair)
			} else {
				messages[0].Content += "\n\n" + imageRepair
			}
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
			messages = append(messages, api.Message{Role: "user", Content: finalInstruction})
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
		if sess.routerPolicy == RouterToolsRouteOnly && len(tools) > 0 && routeRetried && imageRepair == "" {
			chat = o.chatRouteRecovery
		}
		requestCtx, cancelRequest := ctx, context.CancelFunc(func() {})
		if finalAnswer {
			requestCtx, cancelRequest = rounds.answerContext(ctx)
		}
		purpose := roundPurpose(sess.routerPolicy == RouterToolsRouteOnly && len(tools) > 0, false)
		if opts.OmitTurnInstruction {
			purpose = PurposeCompaction
		}
		requestCtx = withModelRequestPurpose(requestCtx, purpose)
		if ctl != nil {
			final := finalAnswer
			requestCtx = withRequestControl(requestCtx, requestControl{
				dispatched: ctl.dispatched,
				// A correction cannot change the answer round of a limit.
				obsolete: func() bool { return !final && ctl.waiting() },
			})
		}
		response, responseComplete, err := o.chatFullWithContinuation(requestCtx, req, opts, responseLimit, &continuations, chat)
		cancelRequest()
		if errors.Is(err, errRequestObsolete) {
			continue
		}
		if err != nil {
			o.history = restore
			o.canonical = restoreCanonical
			o.working = restoreWorking
			if finalAnswer {
				if cut := rounds.cutOff(ctx, requestCtx, response.Content); cut != nil {
					return "", cut
				}
			}
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
		checkImageResult := directImage != noImageContext || routing || imageRepair != "" || (routedChat && imageResultNoun.MatchString(input))
		var invalidImage error
		if checkImageResult && len(response.ToolCalls) == 0 {
			invalidImage = o.imageReplyError(speakable+tail, input, directImage, imageRepair != "")
		}
		if missingRoute || invalidImage != nil {
			if routeRetried || routedChat || finalAnswer {
				if invalidImage != nil {
					return "", fmt.Errorf("no image was attached: %w", invalidImage)
				}
				return "", fmt.Errorf("model did not select a route; no work was dispatched")
			}
			routeRetried = true
			if invalidImage != nil {
				imageRepair = o.imageRepairPrompt(ctx, directImage, owedImage, invalidImage)
				if routing {
					skillCtx += "\n" + missingRouteInstruction
				}
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
			if !opts.ToolsEnabled {
				return "", fmt.Errorf("tools are disabled for this turn")
			}
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

			results := make([]string, 0, len(response.ToolCalls))
			for k, tc := range response.ToolCalls {
				if ctl != nil && ctl.waiting() {
					// Every call still gets a result, so the exchange stays paired.
					for _, skipped := range response.ToolCalls[k:] {
						o.appendProjectedToolResult(skipped, toolNotRunCorrection, opts.Identity)
					}
					break
				}
				result := sess.execute(ctx, o.workDir, tc)
				results = append(results, result)
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
			if len(results) > 0 {
				rounds.observe(response.ToolCalls[:len(results)], results)
			}
			if opts.OnCheckpoint != nil {
				opts.OnCheckpoint()
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
			return "", rounds.stopErr("model returned no final answer")
		}
		if opts.Discussion != nil && !hasSpeakableContent(text) {
			return "", fmt.Errorf("group reply was empty")
		}
		if fallbackEmptyContent(text, hadTools, hadThinking) {
			text = spokenOrFallback(text)
		}
		if opts.Discussion != nil && priorGroupReply != "" && strings.HasPrefix(ollama.NormalizeReplyPrefix(text), priorGroupReply) {
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
		var forced ForcedAnswer
		if finalAnswer {
			forced = rounds.forced
		}
		if ctl != nil {
			// The answer was written before any correction still waiting. It
			// stands only when a limit leaves no request to apply one.
			limit := forced
			if !finalAnswer {
				limit = rounds.limitReached(ctx)
			}
			if limit == ForcedByRepeat {
				limit = ""
			}
			ctl.setLimit(limit)
			if limit != "" {
				if ctl.limitReached() {
					forced = limit
				}
			} else if ctl.waiting() {
				o.appendMessage(api.Message{Role: "assistant", Content: recorded}, opts.Identity)
				continue
			}
		}
		o.appendMessage(api.Message{Role: "assistant", Content: recorded}, opts.Identity)
		if opts.OnCheckpoint != nil {
			opts.OnCheckpoint()
		}
		if !opts.Background && !opts.AgentContinuation && opts.Discussion == nil && !opts.TransientHistory {
			o.ackDelegationResults(req.Messages)
		} else if opts.Background && !opts.TransientHistory {
			o.markBackgroundDelivered(req.Messages)
		}
		// A transient request's history is rolled back; trimming it would
		// archive turns that are about to return. A worker that may still be
		// continued keeps its task and corrections; compaction bounds it.
		if !opts.TransientHistory && ctl == nil {
			o.trimHistory()
		}
		if forced != "" && opts.OnForcedAnswer != nil {
			opts.OnForcedAnswer(forced)
		}
		return text, nil
	}

	return "", fmt.Errorf("tool round limit reached without a final answer")
}

// takeCorrections appends the control's waiting corrections to history as
// user turns, in order. In an answer round a limit imposed, they cannot apply.
func (o *OllamaBrain) takeCorrections(ctl *WorkerControl, finalAnswer bool, opts StreamOptions) {
	if finalAnswer {
		ctl.limitReached()
		return
	}
	for _, text := range ctl.take() {
		o.appendMessage(api.Message{Role: "user", Content: text}, nil)
	}
	if opts.OnCheckpoint != nil {
		opts.OnCheckpoint()
	}
}
