package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// ollamaSpeakerSep marks a stable speaker id prefix on stored user messages so
// History/LoadHistory can round-trip Turn.Speaker without changing the Ollama
// wire format's role field. ASCII record separator is invalid in speech text.
const ollamaSpeakerSep = "\x1e"

// OllamaBrain implements Provider using the Ollama API with tool calling.
type OllamaBrain struct {
	client       *api.Client
	continuityMu sync.Mutex
	// continuitySink receives this instance's continuity events (integrity.go).
	continuitySink func(events.Event)
	infoMu         sync.Mutex
	// infoSink receives this instance's operator Info (think-off warnings).
	infoSink     func(events.Info)
	model        string
	workDir      string
	history      []api.Message
	cfg          *config.Config
	systemPrompt string
	// fullSystemPrompt is the assembled prompt (persona + environment + skills
	// catalog + turn instruction) sent byte-for-byte on every request, so
	// Ollama's KV prefix cache survives across turns. It is rebuilt only when
	// the persona document actually changes. Per-turn skill activations are
	// spliced onto the current user message instead.
	fullSystemPrompt string
	// turnInstruction is the per-reply voice instruction. R-P3 brought it to
	// ollama, which previously received none — so the same persona produced
	// longer, more markdown-shaped replies here than on Claude or Grok.
	turnInstruction string
	// personaReloader re-reads the persona document each turn so an edit lands
	// on the next reply. The assembled prompt is only rebuilt when the document
	// actually changed, which keeps the server's prefix cache intact on the
	// overwhelmingly common no-change path.
	personaReloader   *promptReloader
	keepAlive         *api.Duration
	skills            []skills.Skill
	skillRouter       *semanticSkillRouter
	skillRouterWarned bool
	budgetWarned      bool
	archive           continuity.Store
	contextHighWater  int
	// speakerNames resolves stable speaker ids when building chat messages.
	speakerNames SpeakerNames
}

// SetSpeakerNames attaches a rename table for user-turn prompt attribution.
func (o *OllamaBrain) SetSpeakerNames(names SpeakerNames) { o.speakerNames = names }

func encodeOllamaUser(speakerID, content string) string {
	speakerID = strings.TrimSpace(speakerID)
	if speakerID == "" {
		return content
	}
	return speakerID + ollamaSpeakerSep + content
}

func decodeOllamaUser(stored string) (speakerID, content string) {
	if i := strings.IndexByte(stored, ollamaSpeakerSep[0]); i >= 0 {
		return stored[:i], stored[i+1:]
	}
	return "", stored
}

func (o *OllamaBrain) formatStoredUser(stored string) string {
	speakerID, content := decodeOllamaUser(stored)
	if speakerID == "" {
		return content
	}
	return FormatUserLine(speakerID, content, o.speakerNames)
}

// NewOllama creates an Ollama brain provider.
func NewOllama(cfg *config.Config) (*OllamaBrain, error) {
	if cfg.OllamaModel == "" {
		return nil, fmt.Errorf("ollama_model not configured — run: obey-voice config ollama_model <model>")
	}

	base, err := url.Parse(cfg.OllamaHost)
	if err != nil {
		return nil, fmt.Errorf("invalid ollama_host %q: %w", cfg.OllamaHost, err)
	}

	client := api.NewClient(base, http.DefaultClient)

	// Verify model exists. The probe uses its own timeout-bounded client so a
	// reachable-but-hung host can't block startup; chat requests keep the
	// untimed default client since generations can run long.
	probe := api.NewClient(base, &http.Client{Timeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := probe.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to ollama at %s: %w", cfg.OllamaHost, err)
	}

	found := false
	for _, m := range models.Models {
		if m.Name == cfg.OllamaModel || strings.TrimSuffix(m.Name, ":latest") == cfg.OllamaModel {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("model %q not found in ollama — run: ollama pull %s", cfg.OllamaModel, cfg.OllamaModel)
	}

	workDir := config.WorkDirFrom(cfg)

	systemPrompt, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	// R-P3: ollama previously received no turn instruction, so the same persona
	// produced longer, more markdown-shaped replies here than on Claude/Grok.
	turn, err := turnInstruction(cfg)
	if err != nil {
		return nil, err
	}

	catalog, err := loadSkillsCatalog(context.Background(), cfg, workDir)
	if err != nil {
		return nil, err
	}
	embedCtx, embedCancel := context.WithTimeout(context.Background(), 10*time.Second)
	router, routerErr := newSemanticSkillRouter(embedCtx, client, cfg.OllamaEmbeddingModel, cfg.SkillsSimilarityThreshold, catalog)
	embedCancel()
	if routerErr != nil {
		fmt.Fprintf(os.Stderr, "obey-voice: semantic skill routing unavailable (%v); continuing with the Agent Skills catalog\n", routerErr)
	}

	var keepAlive *api.Duration
	if s := strings.TrimSpace(cfg.OllamaKeepAlive); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			keepAlive = &api.Duration{Duration: d}
		} else {
			fmt.Fprintf(os.Stderr, "obey-voice: invalid ollama_keep_alive %q (%v); using the server default\n", s, err)
		}
	}

	return &OllamaBrain{
		client:           client,
		model:            cfg.OllamaModel,
		workDir:          workDir,
		cfg:              cfg,
		systemPrompt:     systemPrompt,
		fullSystemPrompt: assembleSystemPrompt(systemPrompt, workDir, catalog, turn, cfg),
		turnInstruction:  turn,
		personaReloader: newPromptReloader(prompts.KindPersona, cfg.Persona, systemPrompt, func(hash string) {
			fmt.Fprintf(os.Stderr, "obey-voice: persona prompt changed (hash %s)\n", hash)
		}),
		keepAlive:         keepAlive,
		skills:            catalog,
		skillRouter:       router,
		skillRouterWarned: routerErr != nil,
	}, nil
}

// loadSkillsCatalog returns the Agent Skills catalog when skills_enabled is
// true; otherwise an empty catalog. Discovery follows the cross-client
// .agents/skills convention (project, nearest workspace ancestor, then user)
// and then checks the configured Obey Voice skills_dir. .claude/skills is
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
	catalog, err := skills.Loader{Dirs: dirs, Disabled: cfg.SkillsDisabled}.Catalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading skills: %w", err)
	}
	return catalog, nil
}

// ThinkStream sends input and returns a channel of streaming text chunks.
// Implements an agent loop: if the model returns tool calls, executes them
// and re-requests until the model produces a text response.
func (o *OllamaBrain) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	o.refreshSystemPrompt(opts.OnPromptWarn)
	skillCtx := o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	o.history = append(o.history, api.Message{Role: "user", Content: encodeOllamaUser(opts.Speaker, input)})
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
			commandTimeout: time.Duration(config.ClampToolCommandTimeout(o.cfg.ToolCommandTimeout)) * time.Second,
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

			messages := o.buildMessages(skillCtx)
			if continuation != "" {
				messages = append(messages, api.Message{Role: "user", Content: continuation})
				tools = nil
			}
			if budgetErr := o.messagesBudgetError(messages, tools); budgetErr != nil {
				if ctx.Err() != nil || sendChunk(ctx, out, RecoveryReply) != nil {
					done <- StreamResult{Err: budgetErr}
					return
				}
				o.history = append(o.history, api.Message{Role: "assistant", Content: RecoveryReply})
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
					emit := strip.Feed(speakable)
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
				emit := strip.Feed(heldSpeak)
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
				o.history = append(o.history, api.Message{Role: "assistant", Content: reply})
				o.trimHistory()
				done <- StreamResult{Err: err, Recovered: true}
				return
			}

			if continuation != "" && len(toolCalls) > 0 {
				if err := sendChunk(ctx, out, "\n\n"+continuityFallback); err != nil {
					done <- StreamResult{Err: err}
					return
				}
				o.history = append(o.history, api.Message{Role: "assistant", Content: continuityFallback})
				o.trimHistory()
				done <- StreamResult{Err: fmt.Errorf("continuation returned tool calls"), Recovered: true}
				return
			}

			// If model made tool calls, execute them and loop.
			if len(toolCalls) > 0 {
				hadTools = true
				// Add the assistant's tool-calling message to history.
				o.history = append(o.history, api.Message{
					Role:      "assistant",
					Content:   stripReplyConventions(o.cfg.AgentName, textBuf.String()),
					ToolCalls: toolCalls,
				})

				// Execute each tool and add results (skills + full CLI tools).
				for _, tc := range toolCalls {
					result := sess.execute(ctx, o.workDir, tc)
					// read_skill is the established instruction-loading path. Preserve its
					// full body in this turn's transient skill splice, not persisted history.
					if tc.Function.Name == "read_skill" && o.cfg.ContinuityProjectionEnabled {
						skillCtx += "\n" + result
					}
					o.history = append(o.history, o.projectedToolResult(tc, result))
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
			cleaned := cleanForVoice(raw)
			var response string
			if fallbackEmptyContent(cleaned, hadTools, hadThinking) || hasSpeakableContent(cleaned) {
				response, err = finalizeStreamedText(ctx, out, raw)
				if err != nil {
					done <- StreamResult{Err: err}
					return
				}
			} else {
				response = cleaned
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
				o.history = append(o.history, api.Message{Role: "assistant", Content: "[incomplete response] " + response})
				if recoveryAttempts >= min(o.cfg.ContinuityRecoveryMax, 2) {
					if err := sendChunk(ctx, out, "\n\n"+continuityFallback); err != nil {
						done <- StreamResult{Err: err}
						return
					}
					o.history = append(o.history, api.Message{Role: "assistant", Content: continuityFallback})
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
			o.history = append(o.history, api.Message{Role: "assistant", Content: response})
			o.trimHistory()
			done <- StreamResult{Integrity: integrity}
			return
		}

		if err := sendChunk(ctx, out, "I seem to be going in circles with my tools. Let me just answer directly."); err != nil {
			done <- StreamResult{Err: err}
			return
		}
		o.history = append(o.history, api.Message{
			Role:    "assistant",
			Content: "I seem to be going in circles with my tools. Let me just answer directly.",
		})
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

// ThinkFull sends input and waits for the complete response.
func (o *OllamaBrain) ThinkFull(ctx context.Context, input string, opts StreamOptions) (string, error) {
	skillCtx := o.routeSkillContext(ctx, input, opts.OnToolStart, opts.OnToolEnd)
	// A failed turn rolls back to this snapshot rather than truncating: the tool
	// loop appends assistant/tool turns and the context budget can trim the
	// front, so length alone cannot describe the pre-call history. Unlike
	// ThinkStream — which records a recovery reply and keeps the turn — a failed
	// ThinkFull answers nothing, so its input must not stay behind.
	restore := append([]api.Message(nil), o.history...)
	o.history = append(o.history, api.Message{Role: "user", Content: encodeOllamaUser(opts.Speaker, input)})
	o.ensureContextBudget(skillCtx)

	sess := &toolSession{
		catalog:        o.skills,
		commandTimeout: time.Duration(config.ClampToolCommandTimeout(o.cfg.ToolCommandTimeout)) * time.Second,
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
			return "", fmt.Errorf("ollama error: %w", err)
		}

		if doneReason == "length" {
			o.history = restore
			return "", fmt.Errorf("ollama full response truncated")
		}

		// Tool calls — execute and loop (skills stack on full CLI tools).
		if len(response.ToolCalls) > 0 {
			hadTools = true
			hadThinking = hadThinking || messageHadThinking(response)
			o.history = append(o.history, api.Message{
				Role:      "assistant",
				Content:   stripReplyConventions(o.cfg.AgentName, response.Content),
				ToolCalls: response.ToolCalls,
			})

			for _, tc := range response.ToolCalls {
				result := sess.execute(ctx, o.workDir, tc)
				// read_skill is the established instruction-loading path. Preserve its
				// full body in this turn's transient skill splice, not persisted history.
				if tc.Function.Name == "read_skill" && o.cfg.ContinuityProjectionEnabled {
					skillCtx += "\n" + result
				}
				o.history = append(o.history, o.projectedToolResult(tc, result))
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
		o.history = append(o.history, api.Message{Role: "assistant", Content: text})
		o.trimHistory()
		return text, nil
	}

	return "I seem to be going in circles with my tools. Let me just answer directly.", nil
}

// restatementSkip reports whether soFar is still a restatement of already —
// text this turn already streamed, usually before a tool call. Local models
// often emit the spoken line, fire a phantom tool, then emit the same line
// again. Forwarding the second copy is how Veronica said one sentence twice.
// soFar is compared as speakable text: think tags in a restated Content
// must not hide that it is the same reply.
func restatementSkip(already, soFar string) bool {
	already = strings.TrimSpace(already)
	soFar = strings.TrimSpace(textclean.StripThinkBlocks(soFar))
	if already == "" || soFar == "" {
		return false
	}
	return soFar == already || strings.HasPrefix(already, soFar)
}

// fallbackEmptyContent is the empty-Content table: recover when there is
// nothing speakable and the turn either used tools or never thought.
func fallbackEmptyContent(cleaned string, hadTools, hadThinking bool) bool {
	return !hasSpeakableContent(cleaned) && (hadTools || !hadThinking)
}

func messageHadThinking(msg api.Message) bool {
	if strings.TrimSpace(msg.Thinking) != "" {
		return true
	}
	var f textclean.ThinkFilter
	_, inner := f.FeedSplit(msg.Content)
	_, held := f.FlushSplit()
	return inner != "" || held != ""
}

// newChatRequest builds a streaming chat request carrying the session's
// context policy: an explicit num_ctx so the server never silently truncates
// at its own default (top truncation eats the system prompt — and the
// persona — first), keep_alive so the model stays resident between voice
// turns (a reload is a multi-second mid-conversation stall), num_batch so
// prompt reading is not throttled by a window-derived guess, and think so
// qwen3-family models put CoT in Message.Thinking instead of spoken Content.
func (o *OllamaBrain) newChatRequest(messages []api.Message, tools api.Tools) *api.ChatRequest {
	stream := true
	req := &api.ChatRequest{
		Model:     o.model,
		Messages:  messages,
		Tools:     tools,
		Stream:    &stream,
		KeepAlive: o.keepAlive,
		Think:     o.resolvedThink(),
		Options:   o.requestOptions(),
	}
	return req
}

// requestOptions carries the two settings that decide how the prompt is read.
// They travel together because sending num_ctx alone is what produced the
// symptom this exists to fix: Ollama derives the prompt micro-batch mainly from
// the context window — 512 at or below 4096, 1024 above, 2048 above 32768,
// downgraded toward 512 when the model's predicted VRAM will not fit
// (server/sched.go automaticGenerationBatch at v0.32.14) — so raising the
// window to hold a conversation silently changed how the prompt is read. It
// weighs the model's memory footprint; it never weighs its architecture, which
// is the part that matters below.
//
// num_batch buys prefix REUSE, not prompt-reading speed. llama.cpp keeps its
// last reusable context checkpoint roughly one micro-batch back from the end of
// the prompt, so the server re-reads about num_batch tokens and restores the
// rest. Measured on qwen3.8-uncensored:27b at a ~1.2k-token conversational
// prompt, cached share tracks it almost exactly: 14 % skipped at batch 1024,
// 89 % at 128, 97 % at 32 — while raw prompt-reading throughput *falls* the
// whole way, 239 to 94 tok/s.
//
// So smaller is not simply faster, and anyone tuning this should know which
// direction they are trading in: a smaller batch skips more of an unchanged
// conversational prompt (4.4 s to 0.8 s here) and reads a genuinely new one
// more slowly (5.2 s to 5.4 s cold at 128, 7.8 s at 32). It is a win because
// voice turns keep the persona and history fixed and change only the last
// sentence; it is not a free one.
//
// Sending num_batch explicitly also turns off Ollama's automatic choice, which
// is the point: the useful batch depends on the model and on how much of the
// prompt repeats, and the context window knows neither.
// See docs/benchmarks/B2-qwen-ttft-mechanism-2026-08-28.md.
func (o *OllamaBrain) requestOptions() map[string]any {
	opts := map[string]any{}
	if o.cfg != nil && o.cfg.OllamaNumCtx > 0 {
		opts["num_ctx"] = o.effectiveNumCtx()
	}
	if o.cfg != nil && o.cfg.OllamaNumBatch > 0 {
		opts["num_batch"] = o.cfg.OllamaNumBatch
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

// Think levels ollama_think accepts beyond the boolean forms.
const (
	thinkLow    = "low"
	thinkMedium = "medium"
	thinkHigh   = "high"
	thinkMax    = "max"
)

// ThinkLevels are the canonical ollama_think values a picker should offer, in
// increasing order of effort. The parser also accepts off/0/no and on/1/yes as
// aliases of the two booleans; those are understood, not offered.
//
// It is the source of truth for the ollama_think setting: a level the parser
// gains is invisible to Settings until it is listed here.
func ThinkLevels() []string {
	return []string{"auto", "false", "true", thinkLow, thinkMedium, thinkHigh, thinkMax}
}

// ThinkLevelAccepted reports whether ollamaThinkValue understands value. It
// exists so the settings schema can offer only values the parser honors —
// an unknown value silently disables thinking, which would be invisible in a
// picker.
func ThinkLevelAccepted(value string) bool {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "auto", "false", "off", "0", "no", "true", "on", "1", "yes",
		thinkLow, thinkMedium, thinkHigh, thinkMax:
		return true
	default:
		return false
	}
}

// PreferOllamaThink reports whether model is in the qwen3 family that dumps
// untagged chain-of-thought into Content unless the API Think field is on.
func PreferOllamaThink(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "qwen3")
}

// resolveOllamaThink maps config ollama_think onto the API Think field.
// Empty and "auto" leave thinking off (voice must not pay a think pass on
// every turn). PreferOllamaThink is not applied here — forcing Think:true
// for qwen3 made greetings take a minute on CPU. Explicit true/low/… still
// enable it. Keys off raw, not viper.
func resolveOllamaThink(raw, _ string) *api.ThinkValue {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" || s == "auto" {
		return &api.ThinkValue{Value: false}
	}
	return ollamaThinkValue(raw)
}

func (o *OllamaBrain) resolvedThink() *api.ThinkValue {
	raw := ""
	if o.cfg != nil {
		raw = o.cfg.OllamaThink
	}
	v := resolveOllamaThink(raw, o.model)
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "false" || s == "off" || s == "0" || s == "no" {
		o.maybeWarnThinkOff(v)
	}
	return v
}

var thinkOffOnce sync.Once

// SetInfoSink routes this brain's operator Info onto its owning pipeline's
// event bus. Instance-scoped: with several pipelines active, each brain's
// warnings stay on its own conversation's bus.
func (o *OllamaBrain) SetInfoSink(fn func(events.Info)) {
	o.infoMu.Lock()
	o.infoSink = fn
	o.infoMu.Unlock()
}

func (o *OllamaBrain) emitInfo(info events.Info) {
	o.infoMu.Lock()
	sink := o.infoSink
	o.infoMu.Unlock()
	if sink != nil {
		sink(info)
	}
}

func (o *OllamaBrain) maybeWarnThinkOff(think *api.ThinkValue) {
	if !PreferOllamaThink(o.model) || think == nil || think.Value != false {
		return
	}
	thinkOffOnce.Do(func() {
		msg := fmt.Sprintf("thinking model %q with ollama_think=false; untagged CoT can be spoken", o.model)
		fmt.Fprintf(os.Stderr, "obey-voice: %s\n", msg)
		o.emitInfo(events.Info{Message: msg})
	})
}

// ollamaThinkValue maps an explicit ollama_think value onto the Ollama API
// Think field. It must not receive "auto" — that is resolveOllamaThink's job.
// Unknown values fall back to false so a typo never re-enables thinking.
func ollamaThinkValue(raw string) *api.ThinkValue {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "auto" {
		fmt.Fprintf(os.Stderr, "obey-voice: ollamaThinkValue received auto; using false (call resolveOllamaThink)\n")
		return &api.ThinkValue{Value: false}
	}
	switch s {
	case "", "false", "off", "0", "no":
		return &api.ThinkValue{Value: false}
	case "true", "on", "1", "yes":
		return &api.ThinkValue{Value: true}
	case thinkLow, thinkMedium, thinkHigh, thinkMax:
		return &api.ThinkValue{Value: s}
	default:
		fmt.Fprintf(os.Stderr, "obey-voice: invalid ollama_think %q; using false (speakable content only)\n", raw)
		return &api.ThinkValue{Value: false}
	}
}

// chat issues a chat request, retrying once without tools if the model reports
// it doesn't support them — so a non-tool model degrades to plain chat instead
// of failing the turn. The degradation is logged so "tools silently vanished"
// is never invisible.
func (o *OllamaBrain) chat(ctx context.Context, req *api.ChatRequest, fn api.ChatResponseFunc) error {
	err := o.client.Chat(ctx, req, fn)
	if err != nil && req.Tools != nil && modelRejectedTools(err) {
		fmt.Fprintf(os.Stderr, "obey-voice: ollama model %q rejected tools (%v); continuing without tool calling — file read/write will not run\n", o.model, err)
		req.Tools = nil
		err = o.client.Chat(ctx, req, fn)
	}
	if err != nil && thinkEnabled(req.Think) && modelRejectedThink(err) {
		fmt.Fprintf(os.Stderr, "obey-voice: ollama model %q rejected think (%v); continuing with think=false\n", o.model, err)
		req.Think = ollamaThinkValue("false")
		return o.client.Chat(ctx, req, fn)
	}
	return err
}

func modelRejectedTools(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "does not support tools")
}

func modelRejectedThink(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not support think") || strings.Contains(msg, "does not support thinking")
}

func thinkEnabled(v *api.ThinkValue) bool {
	if v == nil || v.Value == nil {
		return false
	}
	switch x := v.Value.(type) {
	case bool:
		return x
	case string:
		return x != ""
	default:
		return false
	}
}

// defaultWarmupNumCtx is used when ollama_num_ctx is unset (0). Omitting
// num_ctx lets Ollama size the runner to the model's advertised window —
// 262144 on qwen3.8 / kimi — and a supervisor restart loop pins those
// runners until keep_alive expires. That is not a leak; it is a reload.
const defaultWarmupNumCtx = 8192

func (o *OllamaBrain) warmupNumCtx() int {
	if o.cfg != nil && o.cfg.OllamaNumCtx > 0 {
		return o.effectiveNumCtx()
	}
	return defaultWarmupNumCtx
}

// Warmup preloads the model into memory with a minimal request so the user's
// first real turn doesn't pay the cold-start (model-load) cost. Best-effort:
// it caps generation, sends no tools, and ignores all errors so it can never
// block or disrupt startup.
//
// Always send an explicit num_ctx. A "hi" with no options inherits the
// model default (262k on current qwen/kimi tags) and is what filled RAM
// when serve was being respawned.
func (o *OllamaBrain) Warmup(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	stream := false
	// Build on the same options a real turn sends, rather than assembling a
	// second map by hand. Ollama reloads the model whenever a request's options
	// differ from the loaded runner's, so any drift between these two sets buys
	// a model load that the user's first sentence pays for.
	opts := o.requestOptions()
	if opts == nil {
		opts = map[string]any{}
	}
	opts["num_predict"] = 1
	// The one deliberate difference: with ollama_num_ctx unset a chat request
	// defers to the server, but loading the model that way sizes the runner to
	// the tag's advertised window (262144 on qwen3.8) and pins the RAM. Warmup
	// floors it instead. The two then disagree and the first real turn reloads
	// — pinned by TestWarmupMatchesChatOptions/window_unset_diverges, which is
	// where to start if that reload is ever worth closing.
	opts["num_ctx"] = o.warmupNumCtx()
	fmt.Fprintf(os.Stderr, "obey-voice: warmup model=%s num_ctx=%v num_batch=%v\n", o.model, opts["num_ctx"], opts["num_batch"])
	req := &api.ChatRequest{
		Model:     o.model,
		Messages:  []api.Message{{Role: "user", Content: "hi"}},
		Stream:    &stream,
		Think:     o.resolvedThink(),
		KeepAlive: o.keepAlive,
		Options:   opts,
	}
	_ = o.client.Chat(ctx, req, func(api.ChatResponse) error { return nil })
}

// ClearHistory wipes conversation history.
func (o *OllamaBrain) ClearHistory() {
	o.archive = continuity.Store{}
	o.history = nil
}

// SessionInfo reports the in-process conversation for /session display:
// samantha owns messages[], so the size shown is the estimated next request.
func (o *OllamaBrain) SessionInfo() SessionState {
	return SessionState{
		Kind:         SessionKindChat,
		PromptTokens: o.estimateTokens(""),
		Turns:        len(o.history),
	}
}

// History returns conversation history as Turn slices for session persistence.
func (o *OllamaBrain) History() []Turn {
	turns := make([]Turn, len(o.history))
	for i, m := range o.history {
		content, speaker := m.Content, ""
		if m.Role == "user" {
			speaker, content = decodeOllamaUser(m.Content)
		}
		turns[i] = Turn{
			Role:       m.Role,
			Content:    content,
			Speaker:    speaker,
			ToolName:   m.ToolName,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			args, err := json.Marshal(tc.Function.Arguments)
			if err != nil {
				args = []byte("{}")
			}
			turns[i].ToolCalls = append(turns[i].ToolCalls, TurnToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: string(args),
			})
		}
	}
	return turns
}

// LoadHistory restores conversation history from a saved session. Sessions
// saved by the prompt-based providers use role "samantha" for replies; map it
// to ollama's native "assistant".
func (o *OllamaBrain) LoadHistory(turns []Turn) {
	// Restore starts a distinct session; never recall another session's archive.
	o.archive = continuity.Store{}
	o.contextHighWater = 0
	o.loadHistory(turns)
}

func (o *OllamaBrain) loadHistory(turns []Turn) {
	history := make([]api.Message, 0, len(turns))
	for _, t := range turns {
		role := t.Role
		if role == "samantha" {
			role = "assistant"
		}
		content := t.Content
		if role == "user" && strings.TrimSpace(t.Speaker) != "" {
			content = encodeOllamaUser(t.Speaker, t.Content)
		}
		m := api.Message{
			Role:       role,
			Content:    content,
			ToolName:   t.ToolName,
			ToolCallID: t.ToolCallID,
		}
		for _, tc := range t.ToolCalls {
			args := api.NewToolCallFunctionArguments()
			if strings.TrimSpace(tc.Arguments) != "" {
				_ = json.Unmarshal([]byte(tc.Arguments), &args)
			}
			m.ToolCalls = append(m.ToolCalls, api.ToolCall{
				ID: tc.ID,
				Function: api.ToolCallFunction{
					Name:      tc.Name,
					Arguments: args,
				},
			})
		}
		if m.Role == "tool" && o.cfg != nil && o.cfg.ContinuityProjectionEnabled && !strings.HasPrefix(m.Content, "[tool:") {
			m.Content = o.AddContinuityNote(m.Content, continuity.Provenance{ToolName: m.ToolName})
		}
		history = append(history, m)
	}
	o.history = sanitizeOllamaHistory(history)
}

// refreshSystemPrompt re-resolves the persona document and rebuilds the
// assembled system prompt only if it changed.
//
// The rebuild is conditional for a concrete reason: buildMessages sends
// fullSystemPrompt as the leading message, and ollama caches the prefix of a
// conversation server-side. Emitting a different system prompt invalidates that
// cache and re-processes the whole transcript. So an unchanged persona keeps the
// exact same string, and a genuinely edited one pays the invalidation once —
// which is correct, because the user did change what the model should see.
func (o *OllamaBrain) refreshSystemPrompt(onWarn func(message string)) {
	persona, changed, err := o.personaReloader.resolve(o.cfg)
	if err != nil && onWarn != nil {
		onWarn(err.Error())
	}
	if !changed {
		return
	}
	o.systemPrompt = persona
	o.fullSystemPrompt = assembleSystemPrompt(persona, o.workDir, o.skills, o.turnInstruction, o.cfg)
}

// assembleSystemPrompt builds the session-stable system prompt: persona
// prompt, environment grounding, and the Tier-1 skills catalog. It runs once
// per brain — the result must stay byte-identical across a session's requests.
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

// buildMessages assembles one chat request: the frozen system prompt, the
// full retained history (trimHistory owns the bound), and — when skills
// activated for this turn — their context spliced onto the latest user
// message. Splicing at the tail keeps every earlier token byte-identical
// across requests, so the server's prefix cache re-processes only the new
// turn instead of the whole transcript.
func (o *OllamaBrain) buildMessages(skillCtx string) []api.Message {
	msgs := make([]api.Message, 0, len(o.history)+1)
	msgs = append(msgs, api.Message{Role: "system", Content: o.fullSystemPrompt})
	recall := o.recallNotes()
	lastUser := lastUserIndex(o.history)
	for i, m := range o.history {
		if i == lastUser && recall != "" {
			msgs = append(msgs, recallMessages(recall)...)
		}
		if m.Role == "user" {
			m.Content = o.formatStoredUser(m.Content)
		}
		msgs = append(msgs, m)
	}
	if skillCtx != "" {
		for i := len(msgs) - 1; i > 0; i-- {
			if msgs[i].Role == "user" {
				msgs[i].Content += "\n" + skillCtx
				break
			}
		}
	}
	return msgs
}

const toolResultTruncationMarker = "... (truncated to fit context)"

func toolResultMessage(tc api.ToolCall, result string) api.Message {
	return api.Message{
		Role:       "tool",
		Content:    result,
		ToolName:   tc.Function.Name,
		ToolCallID: tc.ID,
	}
}

// lastUserIndex is the most recent user turn, or -1 if the history has none.
func lastUserIndex(history []api.Message) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return i
		}
	}
	return -1
}

// ensureContextBudget permanently drops the oldest history (user-turn
// aligned) when the estimated prompt would overflow ollama_num_ctx. An
// explicit trim beats the server silently truncating from the top, which
// eats the system prompt — and with it the persona — first. The system
// prompt itself is never trimmed: if it alone exceeds the budget, the
// num_ctx setting is what has to change.
//
// The in-flight user query is pinned. Mid-loop tool results are shrunk
// instead of dropped, because Ollama 0.32.14 + qwen3.8 500s when
// truncation leaves no plain user message (ollama#17778).
func (o *OllamaBrain) ensureContextBudget(skillCtx string) {
	if o.cfg == nil {
		return
	}
	budget := o.effectiveNumCtx()
	if budget <= 0 {
		return
	}
	// Drop/archive older exchanges before expanding a pressured active turn.
	limit := budget * 9 / 10
	if o.estimateTokens(skillCtx) <= limit {
		return
	}
	trimmed := o.dropOldestBeforeLastUser(skillCtx, limit)
	for o.estimateTokens(skillCtx) > o.effectiveNumCtx()*85/100 && o.expandContext() {
	}
	budget = o.effectiveNumCtx()
	limit = budget * 9 / 10
	if o.estimateTokens(skillCtx) > limit {
		trimmed = o.shrinkToolsToTokenLimit(skillCtx, limit) || trimmed
	}
	if trimmed && !o.budgetWarned {
		o.budgetWarned = true
		fmt.Fprintf(os.Stderr, "obey-voice: prompt near ollama_num_ctx=%d; dropping oldest history or shrinking tool results to stay under budget\n", budget)
	}
}

func (o *OllamaBrain) dropOldestBeforeLastUser(skillCtx string, limit int) bool {
	trimmed := false
	for len(o.history) > 2 && o.estimateTokens(skillCtx) > limit {
		lastUser := lastUserIndex(o.history)
		if lastUser < 0 {
			break
		}
		start := historyWindowStart(o.history, len(o.history)-2)
		if start <= 0 || start >= len(o.history) || start > lastUser {
			break
		}
		o.archiveMessages(o.history[:start])
		o.history = append([]api.Message(nil), o.history[start:]...)
		trimmed = true
	}
	return trimmed
}

// shrinkToolsToTokenLimit assigns remaining prompt bytes to tool bodies,
// newest first, so the request fits limit tokens. Older results shrink
// first; a tool may become empty if the budget is that tight.
func (o *OllamaBrain) shrinkToolsToTokenLimit(skillCtx string, limit int) bool {
	if o.estimateTokens(skillCtx) <= limit {
		return false
	}
	remaining := limit*4 - (o.promptBytes(skillCtx) - toolContentBytes(o.history))
	return shrinkToolResults(o.history, remaining)
}

func shrinkToolResults(history []api.Message, remainingBytes int) bool {
	if remainingBytes < 0 {
		remainingBytes = 0
	}
	var tools []int
	for i, m := range history {
		if m.Role == "tool" {
			tools = append(tools, i)
		}
	}
	keep := make([]int, len(history))
	budget := remainingBytes
	for t := len(tools) - 1; t >= 0; t-- {
		i := tools[t]
		n := min(len(history[i].Content), budget)
		keep[i] = n
		budget -= n
	}
	changed := false
	for _, i := range tools {
		cur := history[i].Content
		want := keep[i]
		if want >= len(cur) {
			continue
		}
		history[i].Content = truncateToBytes(cur, want)
		changed = true
	}
	return changed
}

func truncateToBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	marker := toolResultTruncationMarker
	if n <= len(marker) {
		return marker[:n]
	}
	end := n - len(marker)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + marker
}

func toolContentBytes(history []api.Message) int {
	n := 0
	for _, m := range history {
		if m.Role == "tool" {
			n += len(m.Content)
		}
	}
	return n
}

// estimateTokens coarsely sizes the next request (≈4 bytes per token plus
// per-message overhead). It only needs to be accurate enough to trim before
// the server truncates.
func (o *OllamaBrain) estimateTokens(skillCtx string) int {
	return o.promptBytes(skillCtx) / 4
}

func (o *OllamaBrain) promptBytes(skillCtx string) int {
	n := len(o.fullSystemPrompt) + len(skillCtx)
	if recall := o.recallNotes(); recall != "" {
		n += len(recall) + 256
	}
	for _, m := range o.history {
		n += len(m.Content) + 16
		if len(m.ToolCalls) > 0 {
			encoded, _ := json.Marshal(m.ToolCalls)
			n += len(encoded)
		}
	}
	return n
}

func (o *OllamaBrain) routeSkillContext(ctx context.Context, input string, onStart, onEnd func(name, detail string)) string {
	matched, err := o.skillRouter.Match(ctx, input)
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
		if onStart != nil {
			onStart("activate_skill", strings.Join(names, ", "))
		}
		if onEnd != nil {
			onEnd("activate_skill", fmt.Sprintf("injected %d skill(s): %s", len(names), strings.Join(names, ", ")))
		}
	}
	return ActivatedSkillContext(matched)
}

// trimHistory bounds retained history with a high/low water mark instead of
// a per-turn sliding window: nothing is dropped until the count exceeds
// MaxHistory*2, then it cuts down to MaxHistory (user-turn aligned). Between
// trims the retained prefix is byte-stable, so the server's KV prefix cache
// keeps hitting; a slide-per-turn window shifted the prefix every turn and
// forced a full re-prefill for the rest of the conversation.
func (o *OllamaBrain) trimHistory() {
	high, low := o.cfg.MaxHistory*2, o.cfg.MaxHistory
	if o.cfg.ContinuityArchiveEnabled {
		high = min(high, 12)
		low = min(low, 10)
	}
	if len(o.history) <= high {
		return
	}
	if start := historyWindowStart(o.history, low); start > 0 {
		o.archiveMessages(o.history[:start])
		o.history = append([]api.Message(nil), o.history[start:]...)
	}
}

// historyWindowStart returns the index where a history window of at most max
// messages begins. A window must not open in the middle of a tool-call group:
// stranded tool results (or a tool-call assistant without its user) make
// Ollama 0.32.14 500 with "no user query found in messages" (ollama#17778).
//
// If the tail would open on a tool result, the window advances to the next
// user. If it would open on an assistant tool-call, it walks back to the
// user that started that exchange — slightly over max, rather than dropping
// the query the in-flight tools belong to.
func historyWindowStart(history []api.Message, max int) int {
	if max <= 0 {
		return len(history)
	}
	start := 0
	if len(history) > max {
		start = len(history) - max
	}
	if start == 0 {
		return start
	}
	if history[start].Role == "assistant" && len(history[start].ToolCalls) > 0 {
		for i := start - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				return i
			}
		}
	}
	if history[start].Role != "tool" {
		return start
	}
	for i := start + 1; i < len(history); i++ {
		if history[i].Role == "user" {
			return i
		}
	}
	return len(history)
}

// sanitizeOllamaHistory repairs a session that was saved before ToolCalls
// round-tripped: empty assistant + tool results with no user, which Ollama
// rejects as "no user query found in messages". Compact seeds (a non-empty
// assistant summary) stay.
func sanitizeOllamaHistory(in []api.Message) []api.Message {
	i := 0
	for i < len(in) {
		m := in[i]
		if m.Role == "user" {
			break
		}
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" && len(m.ToolCalls) == 0 {
			break
		}
		i++
	}
	in = in[i:]
	return in
}

func init() {
	register(config.BrainProviderSpec{
		Name: "ollama", Description: "Local Ollama server",
	}, func(cfg *config.Config) (Provider, error) {
		return NewOllama(cfg)
	}, func(cfg *config.Config) (BatchProvider, error) {
		return newOllamaBatch(cfg)
	})
}
