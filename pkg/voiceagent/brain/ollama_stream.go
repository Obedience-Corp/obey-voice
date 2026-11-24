package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain/ollama"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

// ThinkStream sends input and returns a channel of streaming text chunks.
// Implements an agent loop: if the model returns tool calls, executes them
// and re-requests until the model produces a text response.
func (o *OllamaBrain) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	o.turnStarted.Store(true)
	ctx = opts.workerBudget().Context(ctx)
	responseLimit := newResponseBudget(o.cfg, opts)
	ctx = withResponseBudget(ctx, responseLimit)
	if err := o.prepareAudioHistory(ctx, opts); err != nil {
		return nil, err
	}
	if err := o.prepareImageHistory(ctx, opts); err != nil {
		return nil, err
	}
	if opts.Identity != nil {
		copied := *opts.Identity
		opts.Identity = &copied
	}
	o.refreshSystemPrompt(opts.OnPromptWarn)
	o.stopped = ollamaStopped{}
	retrySkills := ""
	if opts.RetryInput {
		retry, ok := o.FailedInputRetry()
		if !ok || retry.Input != input {
			return nil, fmt.Errorf("failed input is no longer retryable; send a new request")
		}
		retrySkills = o.inputRetry.skillContext
		opts.ToolsEnabled, opts.RouterToolsPolicy, opts.RouteJSON = o.inputRetry.toolsEnabled, o.inputRetry.routerPolicy, o.inputRetry.routeJSON
	}
	o.inputRetry = nil
	skillCtx := retrySkills
	// activatedSkills is the skill text within skillCtx, kept apart so the
	// routing round can be sent without it.
	activatedSkills := ""
	if opts.Discussion == nil && !opts.AgentContinuation && !opts.RetryInput {
		skillCtx = o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
		activatedSkills = skillCtx
	} else if opts.Discussion != nil || opts.AgentContinuation {
		opts.ToolsEnabled = false
	}
	if !opts.RetryInput && !opts.Background && !opts.AgentContinuation && opts.Discussion == nil {
		if err := o.appendUserInput(input, opts); err != nil {
			return nil, err
		}
	}
	priorReply := o.previousReplyForRepetition(opts)
	latestMessage := input
	if opts.AgentContinuation && len(o.canonical) > 0 {
		latestMessage = o.canonical[len(o.canonical)-1].Content
	}
	if !opts.RetryInput && !opts.Background && !opts.AgentContinuation && opts.Discussion == nil {
		o.receiveDelegationResults(opts)
	}
	if opts.Background && !opts.AgentContinuation {
		skillCtx += "\nReport the newly arrived background task result briefly. Do not answer earlier requests again or start more tools."
	}
	baseSkillCtx := skillCtx
	skillCtx += WorkResultsPrompt(opts.WorkResults)
	skillCtx += WorkerFactsPrompt(o.cfg, opts.WorkerFacts)
	o.ensureContextBudget(skillCtx)
	o.maybeEmitContinuityBudget(skillCtx)

	out := make(chan string, 8)
	thinking := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(thinking)
		defer close(done)
		// Publish retained context on every exit, after the response/tool history
		// has been committed or trimmed, rather than waiting for another input.
		defer func() { o.maybeEmitContinuityBudget("") }()

		// Per-turn tool session: exposes skills through progressive disclosure.
		sess := o.turnToolSession(opts)

		stream := ollama.Stream{
			Speak: func(text string) error { return sendChunk(ctx, out, text) },
			Think: func(text string) error { return sendChunk(ctx, thinking, text) },
		}
		hadThinking := false
		hadTools := false
		routedChat := false
		routeRetried := false
		jsonChat := false
		recoveryAttempts := 0
		var continuation string
		repetitionRetry := false
		repetitionAllowed := false
		directImage, owedImage := o.directImageRequest(input, opts)
		sess.imageDelivery = directImage.requiresFile(input)
		var imageRepair string

		rounds := newToolRounds(o.cfg, opts)
		workerAvailable := opts.canHandOff()
		// With job control on, a job_control call ends the turn wherever it
		// appears; the host decides what it does.
		catchControl := o.catchesJobControl()
		endAt := ""
		if catchControl {
			endAt = jobControlToolName
		}
		inspectedLocally := false
		for i := 0; rounds.more(i); i++ {
			finalAnswer, finalInstruction := rounds.final(ctx, i)
			handoff := workerAvailable && !finalAnswer && continuation == "" && !repetitionRetry
			var tools api.Tools
			if opts.ToolsEnabled && !routedChat && !finalAnswer {
				tools = sess.tools()
				if handoff {
					tools = withBackgroundWork(tools)
				}
			}

			// The route is chosen from what the user said, not from a skill's
			// instructions. A skill body beside "Planner." reads as a task and
			// was routed to a worker; the same turn without it is conversation.
			// The reply round and the worker still get the skill.
			roundCtx := skillCtx
			if activatedSkills != "" && sess.routerPolicy == RouterToolsRouteOnly && len(tools) > 0 {
				roundCtx = strings.Replace(skillCtx, activatedSkills, "", 1)
			}
			if err := o.prepareWorkerContext(ctx, roundCtx, tools, opts); err != nil {
				done <- StreamResult{Err: err}
				return
			}
			messages := o.buildMessagesForTurn(roundCtx, opts.AgentContinuation && opts.Discussion == nil, opts)
			if handoff {
				messages[0].Content += "\n\n" + systemPrompt(o.cfg, "live-background-work", nil)
			}
			if jsonChat {
				// The JSON route request ended at this same point, so the
				// reply re-reads only this line.
				appendToNewestUser(messages, "\n"+chatRouteAnswerInstruction)
			}
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
			if continuation != "" {
				messages = append(messages, api.Message{Role: "user", Content: continuation})
				tools = nil
			}
			if repetitionRetry {
				messages = append(messages, api.Message{Role: "user", Content: repetitionRetryInstruction})
				tools = nil
			}
			if finalAnswer {
				messages = append(messages, api.Message{Role: "user", Content: finalInstruction})
			}
			if budgetErr := o.messagesBudgetError(messages, tools); budgetErr != nil {
				done <- StreamResult{Err: budgetErr, Recovered: ctx.Err() == nil}
				return
			}
			req := o.newChatRequest(messages, tools)
			if opts.Discussion != nil {
				if req.Options == nil {
					req.Options = map[string]any{}
				}
				req.Options["num_predict"] = discussion.MaxReplyTokens
				req.Think = &api.ThinkValue{Value: false}
			}
			if err := o.applyOutputBudget(req, opts.workerBudget()); err != nil {
				done <- StreamResult{Err: err}
				return
			}
			if err := responseLimit.apply(req); err != nil {
				done <- StreamResult{Err: err, Recovered: ctx.Err() == nil}
				return
			}

			routing := sess.routerPolicy == RouterToolsRouteOnly && len(tools) > 0
			// json_route decides on the reply's own messages without tools. A
			// retry after a rejected decision is the existing recovery call.
			jsonRouting := routing && opts.RouteJSON && imageRepair == "" && jsonRouteEligible(tools, messages)
			single := newSingleCallRound(sess, routing, routeRetried && imageRepair == "", activatedSkills != "")
			checkImageResult := directImage != noImageContext || (routing && !single.answering) || imageRepair != "" || ((routedChat || single.answering) && imageResultNoun.MatchString(input))
			// Hold routing, image and final-answer text until the complete reply is validated.
			prior := priorReply
			if continuation != "" || repetitionAllowed {
				prior = ""
			}
			output := stream.NewRound(ctx, ollama.RoundOptions{
				Labels:        newLabelStripper(o.cfg.AgentName),
				Markers:       new(historicalAssistantMarkerFilter),
				Input:         latestMessage,
				PreviousReply: prior,
				Discussion:    opts.Discussion,
				Hold:          checkImageResult || finalAnswer,
				EndAtTool:     endAt,
			})
			receive := output.Receiver(o.estimateTokens(skillCtx), responseLimit.observe, o.emitContinuityBudget)
			chat := o.chat
			if routing && routeRetried && imageRepair == "" {
				chat = o.chatRouteRecovery
			} else if jsonRouting {
				chat = o.chatJSONRoute
			}
			requestCtx, cancelRequest := ctx, context.CancelFunc(func() {})
			if finalAnswer {
				requestCtx, cancelRequest = rounds.answerContext(ctx)
			}
			requestCtx = withModelRequestPurpose(requestCtx, single.purpose(roundPurpose(routing, continuation != "")))
			roundStarted := time.Now()
			err := chat(requestCtx, req, receive)
			cancelRequest()
			toolCalls := output.ToolCalls
			err = sess.checkStreamRoute(ctx, routing, jsonRouting, output.Usage.Complete, toolCalls, roundStarted, err)
			err = output.FlushFilters(err)
			chatText, err := single.settle(output, o.cfg.AgentName, err)
			toolCalls = output.ToolCalls
			missingRoute := err == nil && routing && len(toolCalls) == 0 && !chatText
			var invalidImage error
			if checkImageResult && err == nil && len(toolCalls) == 0 && !single.deciding {
				invalidImage = o.imageReplyError(output.HeldText(), input, directImage, imageRepair != "")
			}

			finalReady := !finalAnswer || (output.Usage.Complete && output.Usage.DoneReason != "length" && hasSpeakableContent(cleanForVoice(output.HeldText())))
			err = output.ReleaseHeld(!missingRoute && invalidImage == nil && finalReady && !single.deciding, err)
			hadThinking = hadThinking || output.HadThinking
			opts.reportStreamUsage(output.Usage.InputTokens, output.Usage.OutputTokens)
			if single.deciding && chatText {
				routedChat = true
				skillCtx += "\n" + chatRouteAnswerInstruction
				continue
			}
			if missingRoute || invalidImage != nil {
				if routeRetried || routedChat || finalAnswer {
					err := fmt.Errorf("model did not select a route; no work was dispatched")
					if invalidImage != nil {
						err = fmt.Errorf("no image was attached: %w", invalidImage)
					}
					done <- StreamResult{Err: err, Recovered: true}
					return
				}
				routeRetried = true
				if invalidImage != nil {
					imageRepair = o.imageRepairPrompt(ctx, directImage, owedImage, invalidImage)
					if routing && !single.answering {
						skillCtx += "\n" + missingRouteInstruction
					}
					o.emitInfo(events.Info{Message: "Image reply had no attachment; retrying once before delivery"})
				}
				continue
			}
			if err == nil && output.Repeated() {
				if opts.Discussion != nil {
					done <- StreamResult{Err: fmt.Errorf("group reply repeated a prior answer"), Recovered: true}
					return
				}
				if repetitionRetry {
					done <- StreamResult{Err: fmt.Errorf("response repeated after one retry"), Recovered: ctx.Err() == nil}
					return
				}
				decision, checkErr := o.repetitionIntent(ctx, latestMessage, opts)
				if ctx.Err() != nil {
					done <- StreamResult{Err: ctx.Err()}
					return
				}
				if checkErr != nil {
					o.emitContinuity(events.Info{Message: "repetition check unavailable; preserving response: " + checkErr.Error()})
				} else if decision == "unrequested" {
					repetitionRetry = true
					o.emitContinuity(events.Info{Message: "repeated previous answer; retrying once before speech"})
					continue
				}
				repetitionAllowed = true
			}
			err = output.FinishSpeech(err)
			if err != nil && catchControl && ctx.Err() == nil && callsTool(toolCalls, jobControlToolName) {
				control := JobControl{Refusal: JobControlIncomplete, Spoken: o.spokenBeforeControl(output)}
				done <- StreamResult{Err: fmt.Errorf("ollama stream: %w", err), Recovered: true, Route: JobControlRoute, JobControl: &control}
				return
			}
			if err != nil {
				if finalAnswer {
					if cut := rounds.cutOff(ctx, requestCtx, output.HeldText()); cut != nil {
						err = cut
					}
				}
				// Preserve only genuine text already sent to the user. Report
				// failure separately, never as a synthetic assistant utterance.
				if partial := strings.TrimSpace(stripReplyConventions(o.cfg.AgentName, output.EmittedText())); partial != "" {
					o.appendMessage(api.Message{Role: "assistant", Content: partial}, opts.Identity, IncompleteDisposition(err))
					o.stopped.reply, o.stopped.sent = true, output.EmittedText()
					o.trimHistory()
				}
				o.retainFailedInput(ctx, input, opts, baseSkillCtx, err, output.EmittedText() != "" || hadThinking || hadTools || len(toolCalls) > 0)
				done <- StreamResult{Err: fmt.Errorf("ollama stream: %w", err), Recovered: ctx.Err() == nil}
				return
			}

			if opts.Discussion != nil && len(toolCalls) > 0 {
				done <- StreamResult{Err: fmt.Errorf("tools are disabled for group discussion"), Recovered: true}
				return
			}
			if !opts.ToolsEnabled && len(toolCalls) > 0 {
				// A tool-only response to an ordinary chat turn would otherwise
				// end in silence. Give the model one final text-only round.
				ordinary := !rounds.worker && !opts.Background && !opts.AgentContinuation && !opts.OmitTurnInstruction && !opts.TransientHistory
				if ordinary && i == 0 && !hasSpeakableContent(output.EmittedText()) {
					rounds.stop = "tools are disabled for this turn"
					rounds.instruction = systemPrompt(o.cfg, "tools-disabled-answer", nil)
					continue
				}
				done <- StreamResult{Err: fmt.Errorf("tools are disabled for this turn"), Recovered: true}
				return
			}
			if (continuation != "" || repetitionRetry || routedChat || finalAnswer) && len(toolCalls) > 0 {
				done <- StreamResult{Err: fmt.Errorf("response recovery returned tool calls"), Recovered: true}
				return
			}
			// A batch with a job_control call runs nothing: not the control,
			// not its siblings, not a hand-off. The turn ends at the call.
			if catchControl && callsTool(toolCalls, jobControlToolName) {
				complete := output.Usage.Complete && output.Usage.DoneReason != "length"
				control := decodeJobControl(toolCalls, offersTool(tools, jobControlToolName), complete, o.spokenBeforeControl(output))
				done <- StreamResult{Route: JobControlRoute, JobControl: &control}
				return
			}

			// If model made tool calls, execute them and loop.
			if len(toolCalls) > 0 {
				hadTools = true
				kind := LiveRouterDecision(sess.routerPolicy, toolCalls)
				// A worker has no view_image: a round that opens a picture, and
				// every round after one was opened, is handed off only when the
				// persona asks for background work itself.
				if handoff {
					if callsTool(toolCalls, viewImageToolName) || sess.viewed {
						if callsTool(toolCalls, backgroundWorkTool) {
							kind = "spawn"
						}
					} else if inspectedLocally || directWorkerHandoff(toolCalls) {
						kind = "spawn"
					}
				}
				if kind != "" {
					done <- o.finishStreamRoute(sess, toolCalls, kind, jsonRouting, inspectedLocally)
					return
				}
				if sess.routerPolicy == RouterToolsRouteOnly && hasRouteCall(toolCalls) {
					// chat has been selected. The follow-up is the answer,
					// so it needs no further routing decision. Image requests
					// still wait for claim validation before speech.
					routedChat = true
					if jsonRouting {
						// The reply reuses this round's messages; the private
						// decision stays out of history.
						jsonChat = true
						continue
					}
					skillCtx += "\n" + chatRouteAnswerInstruction
				}
				if handoff {
					inspectedLocally = onlyLocalInspection(toolCalls)
					workerAvailable = inspectedLocally
				}
				// Add the assistant's tool-calling message to history.
				o.appendMessage(api.Message{
					Role:      "assistant",
					Content:   stripReplyConventions(o.cfg.AgentName, output.RawText()),
					ToolCalls: toolCalls,
				}, opts.Identity)
				output.EndThought()

				// Execute each tool and add results (skills + full CLI tools).
				results := make([]string, 0, len(toolCalls))
				for _, tc := range toolCalls {
					result := sess.execute(ctx, o.workDir, tc)
					results = append(results, result)
					if sess.limitErr != nil {
						done <- StreamResult{Err: sess.limitErr}
						return
					}
					// read_skill is the established instruction-loading path. Preserve its
					// full body in this turn's transient skill splice, not persisted history.
					if tc.Function.Name == "read_skill" && o.cfg.ContinuityProjectionEnabled {
						skillCtx += "\n" + result
					}
					o.appendProjectedToolResult(tc, result, opts.Identity)
				}
				rounds.observe(toolCalls, results)
				// Re-budget after tools: a large result can push the user
				// query out of num_ctx. qwen3.8 on Ollama 0.32.14 then 500s
				// with "no user query found in messages" (ollama#17778).
				o.ensureContextBudget(skillCtx)
				continue // re-request with tool results
			}

			// No tool calls — the answer already streamed above; record a
			// cleaned form in history. Empty-content fallback is only for
			// tool-only or fully empty turns: thinking-only empty stays
			// empty so the operator-facing thought is not replaced by a
			// spoken recovery line.
			raw := stripReplyConventions(o.cfg.AgentName, output.RawText())
			cleaned := cleanForVoice(stripHistoricalAssistantMarkers(raw))
			if finalAnswer && (!output.Usage.Complete || output.Usage.DoneReason == "length" || !hasSpeakableContent(cleaned)) {
				done <- StreamResult{Err: rounds.stopErr("model returned an empty or incomplete final answer"), Recovered: ctx.Err() == nil}
				return
			}
			if opts.Discussion != nil && !hasSpeakableContent(cleaned) {
				done <- StreamResult{Err: fmt.Errorf("group reply was empty"), Recovered: true}
				return
			}
			var response string
			if fallbackEmptyContent(cleaned, hadTools, hadThinking) || hasSpeakableContent(cleaned) {
				response, err = finalizeStreamedText(ctx, out, stripHistoricalAssistantMarkers(raw))
				if err != nil {
					done <- StreamResult{Err: err}
					return
				}
			} else {
				response = cleaned
			}
			recorded := response
			if legacyHistoricalAssistantHeader.MatchString(raw) && hasSpeakableContent(cleaned) {
				recorded = raw
			}

			var integrity IntegrityResult
			if o.continuityIntegrityEnabled() {
				integrity = evaluateStreamIntegrity(IntegrityInput{
					DoneReason:   output.Usage.DoneReason,
					PromptTokens: output.Usage.InputTokens,
					GenTokens:    output.Usage.OutputTokens,
					NumCtx:       o.effectiveNumCtx(),
					Speakable:    response,
				})
				if integrity.Truncated {
					o.emitContinuityTruncated(integrity)
				}
			}
			if opts.Discussion != nil && (output.Usage.DoneReason == "length" || integrity.Truncated) {
				o.appendMessage(api.Message{Role: "assistant", Content: "[incomplete response] " + recorded}, opts.Identity)
				done <- StreamResult{Integrity: integrity, Err: fmt.Errorf("group reply exceeded output budget"), Recovered: true}
				return
			}
			if integrity.Truncated && o.cfg.ContinuityRecoveryMax > 0 {
				if ctx.Err() != nil {
					done <- StreamResult{Err: ctx.Err(), Integrity: integrity}
					return
				}
				o.appendMessage(api.Message{Role: "assistant", Content: "[incomplete response] " + recorded}, opts.Identity)
				if recoveryAttempts >= min(o.cfg.ContinuityRecoveryMax, 2) {
					o.trimHistory()
					done <- StreamResult{Integrity: integrity, Err: fmt.Errorf("continuation attempts exhausted"), Recovered: true}
					return
				}
				recoveryAttempts++
				o.emitContinuity(events.ContinuityRecovery{Attempt: recoveryAttempts, Strategy: "compact_then_continue"})
				if err := stream.Separate(); err != nil {
					done <- StreamResult{Err: err}
					return
				}
				if recoveryAttempts > 1 {
					o.expandContext()
				}
				if err := o.compactForContinuation(ctx); err != nil {
					if ctx.Err() != nil {
						done <- StreamResult{Err: ctx.Err()}
						return
					}
					o.emitContinuity(events.Info{Message: "continuity compact failed: " + err.Error()})
				}
				continuation = "Continue the incomplete answer to the latest user request without repeating what was already spoken. The partial answer ended with: " + continuity.Clip(response, 800)
				// Rebudget including the temporary continuation instruction.
				o.ensureContextBudget(skillCtx + continuation)
				continue
			}
			o.appendMessage(api.Message{Role: "assistant", Content: recorded}, opts.Identity)
			o.stopped.reply, o.stopped.sent = true, output.EmittedText()
			if !opts.Background && !opts.AgentContinuation && opts.Discussion == nil {
				o.ackDelegationResults(req.Messages)
			} else if opts.Background {
				o.markBackgroundDelivered(req.Messages)
			}
			o.trimHistory()
			done <- StreamResult{Integrity: integrity}
			return
		}

		done <- StreamResult{Err: fmt.Errorf("tool round limit reached without a final answer"), Recovered: ctx.Err() == nil}
	}()

	return &Stream{Chunks: out, Thinking: thinking, Done: done}, nil
}

func sendChunk(ctx context.Context, out chan<- string, chunk string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- chunk:
		return nil
	}
}
