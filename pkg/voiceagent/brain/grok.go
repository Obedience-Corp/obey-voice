package brain

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// grokRunner is the slice of the grok client the brain needs: streaming for
// voice/TUI turns and the blocking path for text turns. Narrowing it to an
// interface lets tests inject a fake without shelling out to a real grok CLI.
// *grok.GrokClient satisfies it.
type grokRunner interface {
	StreamPrompt(ctx context.Context, prompt string, opts *grok.RunOptions) (<-chan grok.Event, <-chan error)
	RunPromptCtx(ctx context.Context, prompt string, opts *grok.RunOptions) (*grok.GrokResult, error)
}

// GrokBrain manages conversation with Grok via grok-go-sdk, driving the local
// grok CLI the same way the Claude provider drives the claude CLI.
type GrokBrain struct {
	groupContext
	client          grokRunner
	cfg             *config.Config
	systemPrompt    string
	turnInstruction string
	personaReloader *promptReloader
	turnReloader    *promptReloader
	// speakerNames resolves stable speaker ids for flatten prompts (optional).
	speakerNames SpeakerNames

	// mu guards the conversation state below, as Brain.mu does.
	mu          sync.Mutex
	history     []Turn
	hostContext string
	// sessionID is the grok CLI session captured on the first turn and reused
	// via --resume thereafter, so subsequent turns send only the new input.
	// Empty until the first turn captures it; scoped to this GrokBrain, which
	// is constructed per conversation.
	sessionID string
	// seed is a compaction summary that stands in for what the dropped session
	// held, until a request starts a new session from it.
	seed *Turn
}

// SetSpeakerNames attaches a rename table for user-turn prompt attribution.
func (g *GrokBrain) SetSpeakerNames(names SpeakerNames) { g.speakerNames = names }

func (g *GrokBrain) agentName() string {
	if g.cfg != nil {
		return g.cfg.AgentName
	}
	return ""
}

// NewGrok creates a Grok brain provider backed by the grok CLI.
func NewGrok(cfg *config.Config) (*GrokBrain, error) {
	client, err := grok.NewClientFromPath()
	if err != nil {
		return nil, fmt.Errorf("grok CLI not found on PATH: %w", err)
	}

	systemPrompt, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	turn, err := turnInstruction(cfg)
	if err != nil {
		return nil, err
	}

	g := &GrokBrain{
		client:          client,
		cfg:             cfg,
		systemPrompt:    systemPrompt,
		turnInstruction: turn,
		personaReloader: newPromptReloader(prompts.KindPersona, cfg.Persona, systemPrompt, func(hash string) {
			fmt.Fprintf(os.Stderr, "obey-voice: persona prompt changed (hash %s)\n", hash)
		}),
		turnReloader: newPromptReloader(prompts.KindTurn, cfg.TurnPrompt, turn, nil),
	}
	g.systemPrompt = g.assembleSystem(systemPrompt)
	return g, nil
}

// Available returns true if the grok CLI can be located.
func (g *GrokBrain) Available() bool {
	_, err := grok.LocateBinary()
	return err == nil
}

// ThinkStream sends input to Grok and returns a channel of streaming text chunks.
// Only spoken "text" events are forwarded; "thought" (reasoning) events are
// dropped so Samantha never voices her chain of thought.
// assembleSystem builds Grok's system prompt through the shared policy, so it
// receives the same machine grounding every other provider does.
func (g *GrokBrain) assembleSystem(persona string) string {
	workDir := config.WorkDirFrom(g.cfg)
	return AssembleSystemPrompt(SystemPromptInput{
		Provider: providerGrok,
		Persona:  persona,
		WorkDir:  workDir,
		Cfg:      g.cfg,
	})
}

// refreshPrompts re-resolves the bound persona and turn documents each turn.
func (g *GrokBrain) refreshPrompts(onWarn func(string)) {
	if persona, _, err := g.personaReloader.resolve(g.cfg); err == nil {
		g.systemPrompt = g.assembleSystem(persona)
	} else if onWarn != nil {
		onWarn(err.Error())
	}
	if turn, _, err := g.turnReloader.resolve(g.cfg); err == nil {
		g.turnInstruction = turn
	} else if onWarn != nil {
		onWarn(err.Error())
	}
}

func (g *GrokBrain) ThinkStream(ctx context.Context, input string, streamOpts StreamOptions) (*Stream, error) {
	g.refreshPrompts(streamOpts.OnPromptWarn)
	g.mu.Lock()
	// A handoff from another agent is not a user turn.
	if !streamOpts.AgentContinuation {
		g.history = append(g.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker, At: time.Now().UTC()})
	}
	g.prepareCatchUp(streamOpts)
	first, fresh := g.attempts(grok.StreamingJSONOutput, streamOpts)
	g.mu.Unlock()

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)

		raw, streamedAny, err := g.streamAttempt(ctx, out, streamOpts, first)
		// A stale/rejected --resume session errors before any text streams. Drop
		// the id and retry once via the flatten path so one bad CLI session file
		// can't wedge the conversation. Guard on !streamedAny to avoid re-speaking
		// already-emitted audio, and on a live context so a user barge-in cancel
		// is not mistaken for a resume failure.
		if err != nil && first.err == nil && fresh != nil && !streamedAny && g.dropForRetry(ctx) {
			fmt.Fprintf(os.Stderr, "obey-voice: grok resume failed, retrying without session: %v\n", err)
			raw, _, err = g.streamAttempt(ctx, out, streamOpts, *fresh)
		}
		if err == nil {
			// However the CLI ends a stopped request, it leaves this
			// provider alone: the next request may already be running.
			err = ctx.Err()
		}
		if err != nil {
			done <- StreamResult{Err: err}
			return
		}

		response, finErr := finalizeStreamedText(ctx, out, stripReplyConventions(g.agentName(), raw))
		if finErr != nil {
			done <- StreamResult{Err: finErr}
			return
		}
		g.mu.Lock()
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			done <- StreamResult{Err: err}
			return
		}
		g.history = append(g.history, Turn{Role: "samantha", Content: response, At: time.Now().UTC()})
		g.hostContext, g.seed = "", nil
		g.trimHistory()
		g.mu.Unlock()
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

// grokAttempt is one CLI request as the provider's state stood when its
// request started. err is set when the request cannot be passed to the CLI;
// it is then not run.
type grokAttempt struct {
	prompt string
	opts   *grok.RunOptions
	err    error
}

// attempts is Brain.attempts for Grok. The caller holds g.mu.
func (g *GrokBrain) attempts(format grok.OutputFormat, streamOpts StreamOptions) (grokAttempt, *grokAttempt) {
	build := func() grokAttempt {
		prompt, err := argPrompt(providerGrok, g.request(streamOpts))
		return grokAttempt{prompt: prompt, opts: g.runOptions(format, liveCLITools(streamOpts)), err: err}
	}
	first := build()
	if g.sessionID == "" {
		return first, nil
	}
	session := g.sessionID
	g.sessionID = ""
	fresh := build()
	g.sessionID = session
	return first, &fresh
}

// noteSession records the CLI session a request reported, unless the request
// was stopped.
func (g *GrokBrain) noteSession(ctx context.Context, id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() == nil && id != "" {
		g.sessionID = id
	}
}

// dropForRetry is Brain.dropForRetry for Grok.
func (g *GrokBrain) dropForRetry(ctx context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	g.dropSession()
	return true
}

// streamAttempt runs a single Grok streaming turn: it sends the attempt's
// prompt, forwards spoken text to out, and captures the CLI session id for
// resume. It returns the raw assistant text, whether any chunk
// reached out, and any terminal stream error. The caller finalizes once, after
// any resume-failure retry, so a failed first attempt never emits the fallback.
func (g *GrokBrain) streamAttempt(ctx context.Context, out chan<- string, streamOpts StreamOptions, attempt grokAttempt) (string, bool, error) {
	if attempt.err != nil {
		return "", false, attempt.err
	}
	strip := newLabelStripper(g.agentName())

	events, errs := g.client.StreamPrompt(ctx, attempt.prompt, attempt.opts)

	var fullResponse strings.Builder
	streamedAny := false
	var streamErr error

	for ev := range events {
		// The CLI stamps sessionId on its events; capture it from whichever
		// arrives so the next turn can resume.
		if ev.SessionID != "" {
			g.noteSession(ctx, ev.SessionID)
		}

		switch ev.Type {
		case grok.EventText:
			if text := ev.Content(); text != "" {
				fullResponse.WriteString(text)
				if emit := strip.Feed(text); emit != "" {
					if err := sendChunk(ctx, out, emit); err != nil {
						return fullResponse.String(), streamedAny, err
					}
					streamedAny = true
				}
			}
		case grok.EventError:
			// An error event before any text is also how a rejected --resume
			// surfaces on exit 0; keeping it terminal lets the retry run.
			if streamErr == nil {
				streamErr = fmt.Errorf("grok stream: %s", strings.TrimSpace(ev.Content()))
			}
		}
	}

	// Drain errors.
	for err := range errs {
		if err != nil && streamErr == nil {
			streamErr = fmt.Errorf("grok stream: %w", err)
		}
	}
	if streamOpts.OnWorkerUsage != nil {
		// Streaming events contain no token or cost fields: report the
		// observation explicitly with nil components rather than zeroes.
		streamOpts.OnWorkerUsage(WorkerUsage{})
	}

	// Release a short reply the label check still holds — but only on success.
	// On a failed attempt nothing was emitted, so streamedAny stays false and
	// the resume retry can re-stream the reply from the top.
	if streamErr == nil {
		if held := strip.Flush(); held != "" {
			if err := sendChunk(ctx, out, held); err != nil {
				return fullResponse.String(), streamedAny, err
			}
			streamedAny = true
		}
	}

	return fullResponse.String(), streamedAny, streamErr
}

// ThinkFull sends input and waits for the complete response.
func (g *GrokBrain) ThinkFull(ctx context.Context, input string, streamOpts StreamOptions) (string, error) {
	// Same append-then-roll-back contract as Claude: the prompt comes from
	// history, and a turn that never answered must not leave its input behind.
	g.mu.Lock()
	restore := len(g.history)
	if !streamOpts.AgentContinuation {
		g.history = append(g.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker, At: time.Now().UTC()})
	}
	g.prepareCatchUp(streamOpts)
	first, fresh := g.attempts(grok.JSONOutput, streamOpts)
	g.mu.Unlock()

	response, err := g.thinkFullAttempt(ctx, streamOpts, first)
	// Same stale-session guard as ThinkStream: a rejected --resume fails the
	// whole call, so drop the id and retry once via the flatten path. The live
	// context check keeps a user cancellation from being treated as a resume
	// failure.
	if err != nil && first.err == nil && fresh != nil && g.dropForRetry(ctx) {
		fmt.Fprintf(os.Stderr, "obey-voice: grok resume failed, retrying without session: %v\n", err)
		response, err = g.thinkFullAttempt(ctx, streamOpts, *fresh)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		// The caller waits for this call, so no later request has started.
		g.history = g.history[:restore]
		return "", err
	}

	g.history = append(g.history, Turn{Role: "samantha", Content: response, At: time.Now().UTC()})
	g.hostContext, g.seed = "", nil
	g.trimHistory()

	return response, nil
}

// thinkFullAttempt runs a single blocking Grok turn and captures the session
// id for resume. JSON output is required here (not plain text): the SDK's
// decodeOutput only populates GrokResult.SessionID and IsError when it parses
// a JSON result — plain output returns Text alone.
func (g *GrokBrain) thinkFullAttempt(ctx context.Context, streamOpts StreamOptions, attempt grokAttempt) (string, error) {
	if attempt.err != nil {
		return "", attempt.err
	}
	result, err := g.client.RunPromptCtx(ctx, attempt.prompt, attempt.opts)
	if err != nil {
		return "", fmt.Errorf("grok error: %w", err)
	}
	g.noteSession(ctx, result.SessionID)
	// The CLI can report failure with exit 0 and isError on the result (some
	// session rejections included). Never speak the error text, and let a
	// rejected --resume fall through to the flatten retry.
	if result.IsError {
		if streamOpts.OnWorkerUsage != nil {
			streamOpts.OnWorkerUsage(WorkerUsage{ChargeMicroUSD: chargeMicroUSD(result.CostUSD)})
		}
		return "", resultError("grok", result.Subtype, result.Text)
	}
	if streamOpts.OnWorkerUsage != nil {
		streamOpts.OnWorkerUsage(WorkerUsage{ChargeMicroUSD: chargeMicroUSD(result.CostUSD)})
	}

	// Strip the label and think block, clean, then fall back, so the fallback
	// is spoken verbatim.
	return spokenOrFallback(cleanForVoice(stripReplyConventions(g.agentName(), result.Text))), nil
}

// dropSession clears the CLI resume id. Call whenever the session is
// intentionally abandoned (clear/load history, or a rejected --resume).
func (g *GrokBrain) dropSession() {
	g.sessionID = ""
}

// prepareCatchUp is Brain.prepareCatchUp for Grok, with the catch-up held to
// what fits beside the rest of the request in one argument. The caller holds
// g.mu and has recorded the request.
func (g *GrokBrain) prepareCatchUp(opts StreamOptions) {
	if opts.CatchUp == nil || g.sessionID == "" {
		return
	}
	// A request too large even without its catch-up fails, and keeps the session.
	budget := argCatchUpBudget(g.request(opts))
	if budget >= 0 && catchUpOverflows(providerGrok, opts.CatchUp, true, opts.TransientHistory, g.speakerNames, budget) {
		g.dropSession()
	}
}

// Fresh is a sibling with no history and no session to resume: the same CLI,
// prompts and reloaders, a conversation of its own. One per worker job.
func (g *GrokBrain) Fresh() Provider {
	return &GrokBrain{
		client:          g.client,
		cfg:             g.cfg,
		systemPrompt:    g.systemPrompt,
		turnInstruction: g.turnInstruction,
		personaReloader: g.personaReloader,
		turnReloader:    g.turnReloader,
		speakerNames:    g.speakerNames,
	}
}

// runOptions builds the grok run options shared by the streaming and blocking
// paths. Tool execution is gated by voice_tools_enabled: disabled removes all
// built-in tools; enabled uses bypassPermissions — which the grok SDK gates
// behind AllowDangerousMode — so hands-free tool calls don't stall on prompts.
func (g *GrokBrain) runOptions(format grok.OutputFormat, toolsEnabled bool) *grok.RunOptions {
	opts := &grok.RunOptions{
		Format:               format,
		SystemPromptOverride: g.systemPrompt,
		// Same reason as Claude: the grok CLI runs its own tools, so the
		// working directory must reach the subprocess (--cwd), not just the
		// prompt text.
		WorkingDirectory: config.WorkDirFrom(g.cfg),
		// Empty until the first turn captures a session; once set, the SDK
		// emits --resume so the CLI owns history server-side. The system
		// prompt is still passed on resume (harmless; dropping flattened
		// history is the win).
		ResumeID: g.sessionID,
	}
	if toolsEnabled {
		opts.PermissionMode = grok.PermissionBypassPermissions
		opts.AllowDangerousMode = true
	} else {
		// The Grok CLI also uses an empty --tools value to disable every
		// built-in. Keep one empty element so the SDK emits the flag.
		opts.AllowedTools = []string{""}
	}
	if g.cfg.GrokModel != "" {
		opts.Model = g.cfg.GrokModel
	}
	return opts
}

// request renders the whole next request, its conversation record held to
// budget. The caller holds g.mu.
func (g *GrokBrain) request(opts StreamOptions) func(budget int) string {
	return func(budget int) string {
		return g.buildPrompt(opts, budget) + harnessRoutingContext(g.cfg, opts) + currentRequestContext(g.cfg, opts)
	}
}

// buildPrompt renders the next turn for the CLI, as Brain.buildPrompt does.
func (g *GrokBrain) buildPrompt(opts StreamOptions, budget int) string {
	return cliSessionPrompt{
		cfg: g.cfg, provider: providerGrok, history: g.history, live: g.sessionID != "",
		hostContext: g.hostContext, seed: g.seed, names: g.speakerNames, group: &g.groupContext,
		turnInstruction: g.turnInstruction, budget: budget,
	}.render(opts)
}

func (g *GrokBrain) trimHistory() {
	// High/low water trim: drop nothing until MaxHistory*2, then cut to
	// MaxHistory, so the retained prefix stays stable between trims instead
	// of sliding every turn.
	if len(g.history) > g.cfg.MaxHistory*2 {
		g.history = g.history[len(g.history)-g.cfg.MaxHistory:]
	}
}

// History returns the conversation history.
func (g *GrokBrain) History() []Turn {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.history
}

// ClearHistory wipes conversation history. The CLI session id is dropped too
// so a fresh conversation starts a fresh CLI session instead of resuming the
// old one. The on-disk CLI session is left behind — same ghost policy as
// Claude: harmless and never referenced again.
func (g *GrokBrain) ClearHistory() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hostContext, g.seed = "", nil
	g.history = nil
	g.dropSession()
}

// SessionInfo reports the live CLI session for /session display.
func (g *GrokBrain) SessionInfo() SessionState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return SessionState{Kind: SessionKindHarness, ID: g.sessionID, Turns: len(g.history)}
}

// LoadHistory restores conversation history from a saved samantha session. No
// live CLI session exists for persisted history, so the id is cleared: the
// first turn after load runs the flatten path, then captures a new session id
// and resumes from there.
func (g *GrokBrain) LoadHistory(turns []Turn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadHistory(turns)
}

func (g *GrokBrain) loadHistory(turns []Turn) {
	g.hostContext, g.seed = "", nil
	g.history = normalizePromptHistory(turns)
	g.dropSession()
}

// ReseedContinuity is Brain.ReseedContinuity for Grok. turns must lead with
// the compaction summary, as the compaction seed does; the next session
// starts from it.
func (g *GrokBrain) ReseedContinuity(turns []Turn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadHistory(turns)
	if len(turns) > 0 {
		seed := turns[0]
		g.seed = &seed
	}
}

func init() {
	register(config.BrainProviderSpec{
		Name: "grok", Description: "Grok CLI", CLIBinary: "grok",
	}, func(cfg *config.Config) (Provider, error) {
		return NewGrok(cfg)
	}, func(cfg *config.Config) (BatchProvider, error) {
		return newGrokBatch(cfg)
	})
}
