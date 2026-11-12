package brain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// cursorRunner is the slice of the cursor-agent client the brain needs.
// Print-mode Ask is the SDK's ready path; narrowing it to an interface lets
// tests inject a fake without shelling out to a real cursor-agent. *cursor.Client
// satisfies it.
type cursorRunner interface {
	AskCtx(ctx context.Context, prompt string, opts *cursor.AskOptions) (*cursor.AskResult, error)
}

// CursorBrain manages conversation with Cursor via cursor-agent-go, driving
// the local cursor-agent CLI the same way the Grok provider drives the grok CLI.
//
// Print mode is one-shot JSON: ThinkStream waits for the full result and emits
// it as a single chunk. Token-level streaming lives on the SDK's ACP path,
// which is still landing and is not wired here.
type CursorBrain struct {
	groupContext
	client          cursorRunner
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
	// sessionID is the cursor-agent session captured on the first turn and
	// reused via --resume thereafter, so subsequent turns send only the new
	// input. Empty until the first turn captures it; scoped to this
	// CursorBrain, which is constructed per conversation.
	sessionID string
	// promptTokens is the prompt size the CLI reported for the most recent
	// request. 0 until a turn reports usage.
	promptTokens int
	// seed is a compaction summary that stands in for what the dropped session
	// held, until a request starts a new session from it.
	seed *Turn
}

// SetSpeakerNames attaches a rename table for user-turn prompt attribution.
func (c *CursorBrain) SetSpeakerNames(names SpeakerNames) { c.speakerNames = names }

func (c *CursorBrain) agentName() string {
	if c.cfg != nil {
		return c.cfg.AgentName
	}
	return ""
}

// NewCursor creates a Cursor brain provider backed by the cursor-agent CLI.
func NewCursor(cfg *config.Config) (*CursorBrain, error) {
	client, err := cursor.NewClientFromPath()
	if err != nil {
		return nil, fmt.Errorf("cursor-agent CLI not found: %w", err)
	}

	systemPrompt, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	turn, err := turnInstruction(cfg)
	if err != nil {
		return nil, err
	}

	b := &CursorBrain{
		client:          client,
		cfg:             cfg,
		systemPrompt:    systemPrompt,
		turnInstruction: turn,
		personaReloader: newPromptReloader(prompts.KindPersona, cfg.Persona, systemPrompt, func(hash string) {
			fmt.Fprintf(os.Stderr, "obey-voice: persona prompt changed (hash %s)\n", hash)
		}),
		turnReloader: newPromptReloader(prompts.KindTurn, cfg.TurnPrompt, turn, nil),
	}
	b.systemPrompt = b.assembleSystem(systemPrompt)
	return b, nil
}

// Available returns true if the cursor-agent CLI can be located.
func (c *CursorBrain) Available() bool {
	_, err := cursor.LocateBinary()
	return err == nil
}

// assembleSystem builds Cursor's system prompt through the shared policy, so it
// receives the same machine grounding every other provider does. Cursor print
// mode has no native system-prompt flag, so this text is prepended to the first
// flatten prompt (and to any flatten retry) rather than passed as a CLI flag.
func (c *CursorBrain) assembleSystem(persona string) string {
	workDir := config.WorkDirFrom(c.cfg)
	return AssembleSystemPrompt(SystemPromptInput{
		Provider: providerCursor,
		Persona:  persona,
		WorkDir:  workDir,
		Cfg:      c.cfg,
	})
}

// refreshPrompts re-resolves the bound persona and turn documents each turn.
// A nil reloader is a test brain with a fixed prompt; leave it alone. The
// shared resolve helper treats nil as success-with-empty, which would wipe
// the inlined system prompt on every turn.
func (c *CursorBrain) refreshPrompts(onWarn func(string)) {
	if c.personaReloader != nil {
		if persona, _, err := c.personaReloader.resolve(c.cfg); err == nil {
			next := c.assembleSystem(persona)
			// Print mode has no --system-prompt, so a resume cannot pick up a
			// persona edit. Drop the CLI session and re-flatten with the new
			// prompt. Unchanged text keeps the session.
			if next != c.systemPrompt {
				c.mu.Lock()
				if c.sessionID != "" {
					c.dropSession()
				}
				c.mu.Unlock()
			}
			c.systemPrompt = next
		} else if onWarn != nil {
			onWarn(err.Error())
		}
	}
	if c.turnReloader != nil {
		if turn, _, err := c.turnReloader.resolve(c.cfg); err == nil {
			c.turnInstruction = turn
		} else if onWarn != nil {
			onWarn(err.Error())
		}
	}
}

func (c *CursorBrain) ThinkStream(ctx context.Context, input string, streamOpts StreamOptions) (*Stream, error) {
	if err := streamOpts.workerBudget().AllowTool("Cursor native tools"); err != nil {
		return nil, err
	}
	c.refreshPrompts(streamOpts.OnPromptWarn)
	c.mu.Lock()
	c.prepareCatchUp(streamOpts.CatchUp, streamOpts.TransientHistory)
	// A handoff from another agent is not a user turn.
	if !streamOpts.AgentContinuation {
		c.history = append(c.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker, At: time.Now().UTC()})
	}
	first, fresh := c.attempts(streamOpts)
	c.mu.Unlock()

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)

		raw, err := c.askAttempt(ctx, streamOpts, first)
		// A stale/rejected --resume session errors before any text streams. Drop
		// the id and retry once via the flatten path so one bad CLI session file
		// cannot wedge the conversation. Guard on a live context so a user
		// barge-in cancel is not mistaken for a resume failure.
		if err != nil && fresh != nil && c.dropForRetry(ctx) {
			fmt.Fprintf(os.Stderr, "obey-voice: cursor resume failed, retrying without session: %v\n", err)
			raw, err = c.askAttempt(ctx, streamOpts, *fresh)
		}
		if err == nil {
			// A stopped request leaves this provider alone: the next request
			// may already be running.
			err = ctx.Err()
		}
		if err != nil {
			done <- StreamResult{Err: err}
			return
		}

		// Print mode returns the whole reply at once. Speak the same cleaned
		// string ThinkFull would return; finalizeStreamedText then emits the
		// fallback only when that string has no speakable content.
		stripped := stripReplyConventions(c.agentName(), raw)
		cleaned := cleanForVoice(stripped)
		if hasSpeakableContent(cleaned) {
			if sendErr := sendChunk(ctx, out, cleaned); sendErr != nil {
				done <- StreamResult{Err: sendErr}
				return
			}
		}
		response, finErr := finalizeStreamedText(ctx, out, stripped)
		if finErr != nil {
			done <- StreamResult{Err: finErr}
			return
		}
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			done <- StreamResult{Err: err}
			return
		}
		c.history = append(c.history, Turn{Role: "samantha", Content: response, At: time.Now().UTC()})
		c.hostContext, c.seed = "", nil
		c.trimHistory()
		c.mu.Unlock()
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

// ThinkFull sends input and waits for the complete response.
func (c *CursorBrain) ThinkFull(ctx context.Context, input string, streamOpts StreamOptions) (string, error) {
	if err := streamOpts.workerBudget().AllowTool("Cursor native tools"); err != nil {
		return "", err
	}
	c.refreshPrompts(streamOpts.OnPromptWarn)
	// Same append-then-roll-back contract as Claude: the prompt comes from
	// history, and a turn that never answered must not leave its input behind.
	c.mu.Lock()
	c.prepareCatchUp(streamOpts.CatchUp, streamOpts.TransientHistory)
	restore := len(c.history)
	if !streamOpts.AgentContinuation {
		c.history = append(c.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker, At: time.Now().UTC()})
	}
	first, fresh := c.attempts(streamOpts)
	c.mu.Unlock()

	response, err := c.thinkFullAttempt(ctx, streamOpts, first)
	if err != nil && fresh != nil && c.dropForRetry(ctx) {
		fmt.Fprintf(os.Stderr, "obey-voice: cursor resume failed, retrying without session: %v\n", err)
		response, err = c.thinkFullAttempt(ctx, streamOpts, *fresh)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		// The caller waits for this call, so no later request has started.
		c.history = c.history[:restore]
		return "", err
	}

	c.history = append(c.history, Turn{Role: "samantha", Content: response, At: time.Now().UTC()})
	c.hostContext, c.seed = "", nil
	c.trimHistory()

	return response, nil
}

func (c *CursorBrain) thinkFullAttempt(ctx context.Context, streamOpts StreamOptions, attempt cursorAttempt) (string, error) {
	raw, err := c.askAttempt(ctx, streamOpts, attempt)
	if err != nil {
		return "", err
	}
	return spokenOrFallback(cleanForVoice(stripReplyConventions(c.agentName(), raw))), nil
}

// cursorAttempt is one CLI request as the provider's state stood when its
// request started.
type cursorAttempt struct {
	prompt string
	opts   *cursor.AskOptions
}

// attempts is Brain.attempts for Cursor. The caller holds c.mu.
func (c *CursorBrain) attempts(streamOpts StreamOptions) (cursorAttempt, *cursorAttempt) {
	build := func() cursorAttempt {
		return cursorAttempt{
			prompt: c.buildPrompt(streamOpts) + harnessRoutingContext(c.cfg, streamOpts) + currentRequestContext(c.cfg, streamOpts),
			opts:   c.askOptions(liveCLITools(streamOpts)),
		}
	}
	first := build()
	if c.sessionID == "" {
		return first, nil
	}
	session := c.sessionID
	c.sessionID = ""
	fresh := build()
	c.sessionID = session
	return first, &fresh
}

// dropForRetry is Brain.dropForRetry for Cursor.
func (c *CursorBrain) dropForRetry(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	c.dropSession()
	return true
}

// askAttempt runs a single print-mode Cursor turn and captures the session id
// for resume. The SDK's Classify already turns result.IsError into a Go error,
// so a rejected --resume falls through to the flatten retry.
func (c *CursorBrain) askAttempt(ctx context.Context, streamOpts StreamOptions, attempt cursorAttempt) (string, error) {
	result, err := c.client.AskCtx(ctx, attempt.prompt, attempt.opts)
	if err != nil {
		return "", wrapCursorAskError(err)
	}
	if result == nil {
		return "", fmt.Errorf("cursor error: empty result")
	}
	if result.IsError {
		return "", resultError("cursor", result.Subtype, result.Result)
	}
	// Stamp session and usage only on a successful turn, matching Grok.
	// AskCtx can return a parsed JSON result together with a Classify error;
	// capturing that id would start a resume after a failed first turn.
	// A request stopped meanwhile leaves the provider alone.
	promptTokens := result.Usage.InputTokens + result.Usage.CacheReadTokens + result.Usage.CacheWriteTokens
	c.mu.Lock()
	if ctx.Err() == nil {
		if result.SessionID != "" {
			c.sessionID = result.SessionID
		}
		c.promptTokens = promptTokens
	}
	c.mu.Unlock()
	if streamOpts.OnUsage != nil {
		streamOpts.OnUsage(promptTokens, result.Usage.OutputTokens)
	}
	return result.Result, nil
}

// dropSession clears the CLI resume id. Call whenever the session is
// intentionally abandoned (clear/load history, or a rejected --resume).
func (c *CursorBrain) dropSession() {
	c.sessionID = ""
	c.promptTokens = 0
}

// prepareCatchUp is Brain.prepareCatchUp for Cursor. The caller holds c.mu.
func (c *CursorBrain) prepareCatchUp(catchUp *SessionCatchUp, summary bool) {
	if catchUpOverflows(providerCursor, catchUp, c.sessionID != "", summary, c.speakerNames, maxCatchUpBytes) {
		c.dropSession()
	}
}

// Fresh gives each background job its own history and CLI session. The SDK
// client is safe to share; mutable conversation state is never copied.
func (c *CursorBrain) Fresh() Provider {
	return &CursorBrain{
		client: c.client, cfg: c.cfg.Clone(),
		systemPrompt: c.systemPrompt, turnInstruction: c.turnInstruction,
		personaReloader: c.personaReloader, turnReloader: c.turnReloader,
		speakerNames: c.speakerNames,
	}
}

// askOptions builds the print-mode options shared by the streaming and
// blocking paths. Tool execution is gated by voice_tools_enabled: disabled
// uses ask mode (no edits); enabled leaves --mode unset so the CLI runs
// full agent (print mode rejects --mode agent). Trust is always set so
// a first run in an untrusted workspace cannot stall on a human prompt.
//
// Print-mode --mode accepts only plan and ask (cursor-agent 2026.09.02).
// Full agent is the CLI default, so tools-on omits --mode rather than
// passing --mode agent, which exits 1 with empty stdout. Force/yolo stay
// off: voice_tools_enabled is not a dangerous-mode opt-in.
func (c *CursorBrain) askOptions(toolsEnabled bool) *cursor.AskOptions {
	opts := &cursor.AskOptions{
		Trust:            true,
		WorkingDirectory: config.WorkDirFrom(c.cfg),
		Resume:           c.sessionID,
	}
	if !toolsEnabled {
		opts.Mode = cursor.ModeAsk
	}
	if c.cfg != nil && c.cfg.CursorModel != "" {
		opts.Model = c.cfg.CursorModel
	}
	return opts
}

func wrapCursorAskError(err error) error {
	var sdkErr *cursor.Error
	if errors.As(err, &sdkErr) {
		if detail := firstNonEmptyLine(sdkErr.Stderr); detail != "" {
			return fmt.Errorf("cursor error: %w (%s)", err, detail)
		}
	}
	return fmt.Errorf("cursor error: %w", err)
}

func firstNonEmptyLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// buildPrompt renders the next turn for the CLI, as Brain.buildPrompt does.
// The system prompt is inlined into a fresh session's prompt because print
// mode has no --system-prompt flag.
func (c *CursorBrain) buildPrompt(opts StreamOptions) string {
	return cliSessionPrompt{
		cfg: c.cfg, provider: providerCursor, history: c.history, live: c.sessionID != "", system: c.systemPrompt,
		hostContext: c.hostContext, seed: c.seed, names: c.speakerNames, group: &c.groupContext,
		turnInstruction: c.turnInstruction, budget: maxCatchUpBytes,
	}.render(opts)
}

func (c *CursorBrain) trimHistory() {
	if c.cfg == nil {
		return
	}
	if len(c.history) > c.cfg.MaxHistory*2 {
		c.history = c.history[len(c.history)-c.cfg.MaxHistory:]
	}
}

// History returns the conversation history.
func (c *CursorBrain) History() []Turn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.history
}

// ClearHistory wipes conversation history. The CLI session id is dropped too
// so a fresh conversation starts a fresh CLI session instead of resuming the
// old one.
func (c *CursorBrain) ClearHistory() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hostContext, c.seed = "", nil
	c.history = nil
	c.dropSession()
}

// SessionInfo reports the live CLI session for /session display.
func (c *CursorBrain) SessionInfo() SessionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SessionState{Kind: SessionKindHarness, ID: c.sessionID, PromptTokens: c.promptTokens, Turns: len(c.history)}
}

// LoadHistory restores conversation history from a saved samantha session. No
// live CLI session exists for persisted history, so the id is cleared: the
// first turn after load runs the flatten path, then captures a new session id
// and resumes from there.
func (c *CursorBrain) LoadHistory(turns []Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadHistory(turns)
}

func (c *CursorBrain) loadHistory(turns []Turn) {
	c.hostContext, c.seed = "", nil
	c.history = normalizePromptHistory(turns)
	c.dropSession()
}

// ReseedContinuity is Brain.ReseedContinuity for Cursor.
func (c *CursorBrain) ReseedContinuity(turns []Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadHistory(turns)
	if len(turns) > 0 {
		seed := turns[0]
		c.seed = &seed
	}
}

func init() {
	register(config.BrainProviderSpec{
		Name: "cursor", Description: "Cursor Agent CLI", CLIBinary: "cursor-agent",
	}, func(cfg *config.Config) (Provider, error) {
		return NewCursor(cfg)
	}, func(cfg *config.Config) (BatchProvider, error) {
		return newCursorBatch(cfg)
	})
}
