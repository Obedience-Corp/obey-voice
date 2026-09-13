package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/ollama/ollama/api"
)

// ThinkStream sends input and returns a channel of streaming text chunks.
// Implements an agent loop: if the model returns tool calls, executes them
// and re-requests until the model produces a text response.
func (o *OllamaBrain) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	if opts.Identity != nil {
		copied := *opts.Identity
		opts.Identity = &copied
	}
	o.refreshSystemPrompt(opts.OnPromptWarn)
	skillCtx := o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	if !opts.Background && !opts.AgentContinuation {
		o.appendUserInput(input, opts)
	}
	if !opts.Background && !opts.AgentContinuation {
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
		recoveryAttempts := 0
		var continuation string

		for i := 0; i < maxToolIterations; i++ {
			var tools api.Tools
			if opts.ToolsEnabled {
				tools = sess.tools()
			}

			messages := o.buildMessagesForTurn(skillCtx, opts.AgentContinuation)
			if continuation != "" {
				messages = append(messages, api.Message{Role: "user", Content: continuation})
				tools = nil
			}
			if budgetErr := o.messagesBudgetError(messages, tools); budgetErr != nil {
				if ctx.Err() != nil || sendChunk(ctx, out, RecoveryReply) != nil {
					done <- StreamResult{Err: budgetErr}
					return
				}
				o.appendMessage(api.Message{Role: "assistant", Content: RecoveryReply}, opts.Identity)
				o.trimHistory()
				done <- StreamResult{Err: budgetErr, Recovered: true}
				return
			}
			req := o.newChatRequest(messages, tools)

			// Accumulate the full response (text + tool calls).
			var textBuf strings.Builder
			var toolCalls []api.ToolCall
			var prefillTokens, genTokens int
			var doneReason string
			strip := newLabelStripper(o.cfg.AgentName)
			markers := new(historicalAssistantMarkerFilter)
			var split textclean.ThinkFilter

			err := o.chat(ctx, req, func(resp api.ChatResponse) error {
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
					// Stream speakable content as it arrives, tools enabled or
					// not, so the TUI renders token-by-token. Any preamble a
					// tool-calling iteration emits streams too; the final answer
					// continues the same reply after tools run — unless it is
					// the same reply, which we swallow.
					emit := strip.Feed(markers.Feed(speakable))
					if restatementSkip(spoken.String(), textBuf.String()) {
						return nil
					}
					if emit != "" {
						if err := sendChunk(ctx, out, emit); err != nil {
							return err
						}
						spoken.WriteString(emit)
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
					if sendErr := sendChunk(ctx, out, emit); sendErr != nil && err == nil {
						err = sendErr
					} else if sendErr == nil {
						spoken.WriteString(emit)
					}
				}
			}
			if held := markers.Flush(); held != "" {
				emit := strip.Feed(held)
				if emit != "" && !restatementSkip(spoken.String(), textBuf.String()) {
					if sendErr := sendChunk(ctx, out, emit); sendErr != nil && err == nil {
						err = sendErr
					} else if sendErr == nil {
						spoken.WriteString(emit)
					}
				}
			}
			// Anything the label check still holds is real reply text; release
			// it whether the turn ends, errors, or loops into tools.
			if held := strip.Flush(); held != "" && !restatementSkip(spoken.String(), textBuf.String()) {
				if sendErr := sendChunk(ctx, out, held); sendErr != nil && err == nil {
					err = sendErr
				} else if sendErr == nil {
					spoken.WriteString(held)
				}
			}
			if opts.OnUsage != nil && prefillTokens+genTokens > 0 {
				opts.OnUsage(prefillTokens, genTokens)
			}
			if err != nil {
				err = fmt.Errorf("ollama stream: %w", err)
				// A dead context means shutdown or barge-in — no one is
				// listening for a recovery line. Otherwise close the loop out
				// loud: stream the recovery reply, record it so the next turn
				// has an assistant message, and surface err as detail only.
				if ctx.Err() != nil || sendChunk(ctx, out, RecoveryReply) != nil {
					done <- StreamResult{Err: err}
					return
				}
				// Keep any partial streamed text: the user already saw/heard
				// it, so the next turn's context must include it too.
				reply := RecoveryReply
				if partial := strings.TrimSpace(stripReplyConventions(o.cfg.AgentName, textBuf.String())); partial != "" {
					reply = partial + "\n\n" + RecoveryReply
				}
				o.appendMessage(api.Message{Role: "assistant", Content: reply}, opts.Identity)
				o.trimHistory()
				done <- StreamResult{Err: err, Recovered: true}
				return
			}

			if continuation != "" && len(toolCalls) > 0 {
				if err := sendChunk(ctx, out, "\n\n"+continuityFallback); err != nil {
					done <- StreamResult{Err: err}
					return
				}
				o.appendMessage(api.Message{Role: "assistant", Content: continuityFallback}, opts.Identity)
				o.trimHistory()
				done <- StreamResult{Err: fmt.Errorf("continuation returned tool calls"), Recovered: true}
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
				// Add the assistant's tool-calling message to history.
				o.appendMessage(api.Message{
					Role:      "assistant",
					Content:   stripReplyConventions(o.cfg.AgentName, textBuf.String()),
					ToolCalls: toolCalls,
				}, opts.Identity)

				// Execute each tool and add results (skills + full CLI tools).
				for _, tc := range toolCalls {
					result := sess.execute(ctx, o.workDir, tc)
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
			if integrity.Truncated && o.cfg.ContinuityRecoveryMax > 0 {
				if ctx.Err() != nil {
					done <- StreamResult{Err: ctx.Err(), Integrity: integrity}
					return
				}
				o.appendMessage(api.Message{Role: "assistant", Content: "[incomplete response] " + recorded}, opts.Identity)
				if recoveryAttempts >= min(o.cfg.ContinuityRecoveryMax, 2) {
					if err := sendChunk(ctx, out, "\n\n"+continuityFallback); err != nil {
						done <- StreamResult{Err: err}
						return
					}
					o.appendMessage(api.Message{Role: "assistant", Content: continuityFallback}, opts.Identity)
					o.trimHistory()
					done <- StreamResult{Integrity: integrity, Err: fmt.Errorf("continuation attempts exhausted"), Recovered: true}
					return
				}
				recoveryAttempts++
				o.emitContinuity(events.ContinuityRecovery{Attempt: recoveryAttempts, Strategy: "compact_then_continue"})
				if err := sendChunk(ctx, out, "\n\nContinuing.\n\n"); err != nil {
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
			if !opts.Background && !opts.AgentContinuation {
				o.ackDelegationResults(req.Messages)
			} else if opts.Background {
				o.markBackgroundDelivered(req.Messages)
			}
			o.trimHistory()
			done <- StreamResult{Integrity: integrity}
			return
		}

		if err := sendChunk(ctx, out, "I seem to be going in circles with my tools. Let me just answer directly."); err != nil {
			done <- StreamResult{Err: err}
			return
		}
		o.appendMessage(api.Message{
			Role:    "assistant",
			Content: "I seem to be going in circles with my tools. Let me just answer directly.",
		}, opts.Identity)
		o.trimHistory()
		done <- StreamResult{}
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
