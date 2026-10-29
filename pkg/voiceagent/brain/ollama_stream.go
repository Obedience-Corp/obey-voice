package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// ThinkStream sends input and returns a channel of streaming text chunks.
// Implements an agent loop: if the model returns tool calls, executes them
// and re-requests until the model produces a text response.
func (o *OllamaBrain) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	ctx = opts.workerBudget().Context(ctx)
	if err := o.prepareImageHistory(ctx, opts.Attachments); err != nil {
		return nil, err
	}
	if opts.Identity != nil {
		copied := *opts.Identity
		opts.Identity = &copied
	}
	o.refreshSystemPrompt(opts.OnPromptWarn)
	skillCtx := ""
	if opts.Discussion == nil && !opts.AgentContinuation {
		skillCtx = o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	} else {

		opts.ToolsEnabled = false
	}
	if !opts.Background && !opts.AgentContinuation && opts.Discussion == nil {
		if err := o.appendUserInput(input, opts); err != nil {
			return nil, err
		}
	}
	priorReply := o.previousReplyForRepetition(opts)
	latestMessage := input
	if opts.AgentContinuation && len(o.canonical) > 0 {
		latestMessage = o.canonical[len(o.canonical)-1].Content
	}
	if !opts.Background && !opts.AgentContinuation && opts.Discussion == nil {
		o.receiveDelegationResults(opts)
	}
	if opts.Background && !opts.AgentContinuation {
		skillCtx += "\nReport the newly arrived background task result briefly. Do not answer earlier requests again or start more tools."
	}
	skillCtx += WorkResultsPrompt(opts.WorkResults)
	skillCtx += WorkerFactsPrompt(opts.WorkerFacts)
	o.ensureContextBudget(skillCtx)
	o.maybeEmitContinuityBudget(skillCtx)

	out := make(chan string, 8)
	thinking := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(thinking)
		defer close(done)

		// Per-turn tool session: exposes skills through progressive disclosure.
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

		// spoken is every delta already forwarded this turn. After a tool
		// loop the model often restates that text; forwarding it again is
		// how Veronica said the same sentence twice (2026-08-28).
		var spoken strings.Builder
		hadThinking := false
		hadTools := false
		routedChat := false
		recoveryAttempts := 0
		var continuation string
		repetitionRetry := false
		repetitionAllowed := false

		for i := 0; i <= maxToolIterations; i++ {
			finalAnswer := i == maxToolIterations
			var tools api.Tools
			if opts.ToolsEnabled && !routedChat && !finalAnswer {
				tools = sess.tools()
			}

			messages := o.buildMessagesForTurn(skillCtx, opts.AgentContinuation && opts.Discussion == nil)
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
				messages = append(messages, api.Message{Role: "user", Content: toolLimitAnswerInstruction})
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

			// Accumulate the full response (text + tool calls).
			var textBuf strings.Builder
			var toolCalls []api.ToolCall
			var prefillTokens, genTokens int
			var doneReason string
			responseComplete := false
			strip := newLabelStripper(o.cfg.AgentName)
			markers := new(historicalAssistantMarkerFilter)
			var split textclean.ThinkFilter
			gate := replyPrefixGate{}
			if spoken.Len() == 0 && continuation == "" && !repetitionAllowed {
				gate.prior = priorReply
			}
			var emitted strings.Builder
			outputChecked := false
			forward := func(text string) error {
				if opts.Discussion != nil {
					if err := opts.Discussion.State.Context().Err(); err != nil {
						return err
					}
				}
				if text == "" {
					return nil
				}
				if !outputChecked && opts.Discussion != nil && opts.Discussion.BeforeOutput != nil {
					if err := opts.Discussion.BeforeOutput(ctx); err != nil {
						return err
					}
					outputChecked = true
				}
				if err := sendChunk(ctx, out, text); err != nil {
					return err
				}
				spoken.WriteString(text)
				emitted.WriteString(text)
				return nil
			}
			// Until a live routing response finishes, its content might be
			// untagged planning followed by a tool call. It cannot be taken
			// back once spoken. The final tool-limit answer is also held until
			// completion so a rejected or truncated recovery cannot reach speech.
			// Ordinary worker/tool-free responses retain incremental streaming.
			routing := sess.routerPolicy == RouterToolsRouteOnly && len(tools) > 0
			var routeText []string
			emitText := func(text string) error {
				if routing || finalAnswer {
					routeText = append(routeText, text)
					return nil
				}
				return forward(gate.feed(text))
			}

			err := o.chat(ctx, req, func(resp api.ChatResponse) error {
				responseComplete = responseComplete || resp.Done
				fieldThink := resp.Message.Thinking
				if fieldThink != "" {
					hadThinking = true
					if err := sendChunk(ctx, thinking, fieldThink); err != nil {
						return err
					}
				}
				if resp.Message.Content != "" {
					textBuf.WriteString(resp.Message.Content)
					speakable, inner := split.FeedSplit(resp.Message.Content)
					if inner != "" && fieldThink == "" {
						hadThinking = true
						if err := sendChunk(ctx, thinking, inner); err != nil {
							return err
						}
					}
					// emitText holds live routing content until its role is
					// known; ordinary replies and worker preambles stream.
					emit := strip.Feed(markers.Feed(speakable))
					if restatementSkip(spoken.String(), textBuf.String()) {
						return nil
					}
					if emit != "" {
						if err := emitText(emit); err != nil {
							return err
						}
					}
				}
				if len(resp.Message.ToolCalls) > 0 {
					toolCalls = append(toolCalls, resp.Message.ToolCalls...)
				}
				if resp.Done {
					prefillTokens, genTokens = resp.Metrics.PromptEvalCount, resp.Metrics.EvalCount
					doneReason = resp.DoneReason
				}
				return nil
			})
			if err == nil && routing && !responseComplete {
				err = fmt.Errorf("routing response ended before completion")
			}
			heldSpeak, heldThink := split.FlushSplit()
			if heldThink != "" {
				hadThinking = true
				if sendErr := sendChunk(ctx, thinking, heldThink); sendErr != nil && err == nil {
					err = sendErr
				}
			}
			if heldSpeak != "" {
				emit := strip.Feed(markers.Feed(heldSpeak))
				if emit != "" && !restatementSkip(spoken.String(), textBuf.String()) {
					if sendErr := emitText(emit); sendErr != nil && err == nil {
						err = sendErr
					}
				}
			}
			if held := markers.Flush(); held != "" {
				emit := strip.Feed(held)
				if emit != "" && !restatementSkip(spoken.String(), textBuf.String()) {
					if sendErr := emitText(emit); sendErr != nil && err == nil {
						err = sendErr
					}
				}
			}
			// Anything the label check still holds is real reply text; release
			// it whether the turn ends, errors, or loops into tools.
			if held := strip.Flush(); held != "" && !restatementSkip(spoken.String(), textBuf.String()) {
				if sendErr := emitText(held); sendErr != nil && err == nil {
					err = sendErr
				}
			}
			if routing || finalAnswer {
				if len(toolCalls) > 0 {
					// Tool preambles belong to the thinking surface,
					// never the speech stream or canonical reply history.
					textBuf.Reset()
					if text := strings.Join(routeText, ""); text != "" {
						hadThinking = true
						if sendErr := sendChunk(ctx, thinking, text); sendErr != nil && err == nil {
							err = sendErr
						}
					}
				} else if err == nil && (!finalAnswer || (responseComplete && doneReason != "length" && hasSpeakableContent(cleanForVoice(strings.Join(routeText, ""))))) {
					for _, text := range routeText {
						if sendErr := forward(gate.feed(text)); sendErr != nil {
							err = sendErr
							break
						}
					}
				}
			}
			if opts.OnUsage != nil {
				opts.OnUsage(prefillTokens, genTokens)
			}
			if opts.OnWorkerUsage != nil {
				u := WorkerUsage{}
				if prefillTokens > 0 {
					in := prefillTokens
					u.InputTokens = &in
				}
				if genTokens > 0 {
					out := genTokens
					u.OutputTokens = &out
				}
				opts.OnWorkerUsage(u)
			}
			if err == nil && gate.repeat {
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
			if err == nil {
				err = forward(gate.release())
			}
			if err != nil {
				// Preserve only genuine text already sent to the user. Report
				// failure separately, never as a synthetic assistant utterance.
				if partial := strings.TrimSpace(stripReplyConventions(o.cfg.AgentName, emitted.String())); partial != "" {
					o.appendMessage(api.Message{Role: "assistant", Content: partial}, opts.Identity)
					o.trimHistory()
				}
				done <- StreamResult{Err: fmt.Errorf("ollama stream: %w", err), Recovered: ctx.Err() == nil}
				return
			}

			if opts.Discussion != nil && len(toolCalls) > 0 {
				done <- StreamResult{Err: fmt.Errorf("tools are disabled for group discussion"), Recovered: true}
				return
			}
			if (continuation != "" || repetitionRetry || routedChat || finalAnswer) && len(toolCalls) > 0 {
				done <- StreamResult{Err: fmt.Errorf("response recovery returned tool calls"), Recovered: true}
				return
			}

			// If model made tool calls, execute them and loop.
			if len(toolCalls) > 0 {
				hadTools = true
				if kind := LiveRouterDecision(sess.routerPolicy, toolCalls); kind != "" {
					tier, reason := routeMetadata(toolCalls)
					if sess.onStart != nil || sess.onEnd != nil {
						decision := sess.describe("route", kind, "")
						if sess.onStart != nil {
							sess.onStart(decision)
						}
						if sess.onEnd != nil {
							sess.onEnd(ToolResult{ToolCall: decision, Preview: kind})
						}
					}
					// Drop this turn from the talking window. Work belongs on
					// the worker session, not the live KV cache.
					if n := len(o.history); n > 0 {
						o.history = o.history[:n-1]
						if len(o.canonical) > 0 {
							o.canonical = o.canonical[:len(o.canonical)-1]
						}
						if len(o.working) > 0 {
							o.working = o.working[:len(o.working)-1]
						}
					}
					done <- StreamResult{Route: kind, RouteTier: tier, RouteReasonCode: reason}
					return
				}
				if sess.routerPolicy == RouterToolsRouteOnly && hasRouteCall(toolCalls) {
					// chat has been selected. The follow-up is the answer,
					// so it needs neither another routing decision nor buffering.
					routedChat = true
					skillCtx += "\n" + chatRouteAnswerInstruction
				}
				// Add the assistant's tool-calling message to history.
				o.appendMessage(api.Message{
					Role:      "assistant",
					Content:   stripReplyConventions(o.cfg.AgentName, textBuf.String()),
					ToolCalls: toolCalls,
				}, opts.Identity)

				// Execute each tool and add results (skills + full CLI tools).
				for _, tc := range toolCalls {
					result := sess.execute(ctx, o.workDir, tc)
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
			raw := stripReplyConventions(o.cfg.AgentName, textBuf.String())
			cleaned := cleanForVoice(stripHistoricalAssistantMarkers(raw))
			if finalAnswer && (!responseComplete || doneReason == "length" || !hasSpeakableContent(cleaned)) {
				done <- StreamResult{Err: fmt.Errorf("tool round limit reached: model returned an empty or incomplete final answer"), Recovered: ctx.Err() == nil}
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
					DoneReason:   doneReason,
					PromptTokens: prefillTokens,
					GenTokens:    genTokens,
					NumCtx:       o.effectiveNumCtx(),
					Speakable:    response,
				})
				if integrity.Truncated {
					o.emitContinuityTruncated(integrity)
				}
			}
			if opts.Discussion != nil && (doneReason == "length" || integrity.Truncated) {
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
				if err := sendChunk(ctx, out, "\n\n"); err != nil {
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
