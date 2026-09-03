package brain

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	client          cursorRunner
	cfg             *config.Config
	systemPrompt    string
	turnInstruction string
	personaReloader *promptReloader
	turnReloader    *promptReloader
	history         []Turn
	// speakerNames resolves stable speaker ids for flatten prompts (optional).
	speakerNames SpeakerNames
	// sessionID is the cursor-agent session captured on the first turn and
	// reused via --resume thereafter, so subsequent turns send only the new
	// input. Empty until the first turn captures it; scoped to this
	// CursorBrain, which is constructed per conversation.
	sessionID string
	// promptTokens is the prompt size the CLI reported for the most recent
	// request. 0 until a turn reports usage.
	promptTokens int
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
			c.systemPrompt = c.assembleSystem(persona)
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
	c.refreshPrompts(streamOpts.OnPromptWarn)
	c.history = append(c.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker, At: time.Now().UTC()})

	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)

		resuming := c.sessionID != ""
		raw, err := c.askAttempt(ctx, streamOpts)
		// A stale/rejected --resume session errors before any text streams. Drop
		// the id and retry once via the flatten path so one bad CLI session file
		// cannot wedge the conversation. Guard on ctx.Err()==nil so a user
		// barge-in cancel is not mistaken for a resume failure.
		if err != nil && resuming && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "obey-voice: cursor resume failed, retrying without session: %v\n", err)
			c.dropSession()
			raw, err = c.askAttempt(ctx, streamOpts)
		}
		if err != nil {
			done <- StreamResult{Err: err}
			return
		}

		stripped := stripReplyConventions(c.agentName(), raw)
		if stripped != "" {
			if sendErr := sendChunk(ctx, out, stripped); sendErr != nil {
				done <- StreamResult{Err: sendErr}
				return
			}
		}
		response, finErr := finalizeStreamedText(ctx, out, stripped)
		if finErr != nil {
			done <- StreamResult{Err: finErr}
			return
		}
		c.history = append(c.history, Turn{Role: "samantha", Content: response, At: time.Now().UTC()})
		c.trimHistory()
		done <- StreamResult{}
	}()

	return &Stream{Chunks: out, Done: done}, nil
}

// ThinkFull sends input and waits for the complete response.
func (c *CursorBrain) ThinkFull(ctx context.Context, input string, streamOpts StreamOptions) (string, error) {
	// Same append-then-roll-back contract as Claude: the prompt comes from
	// history, and a turn that never answered must not leave its input behind.
	restore := len(c.history)
	c.history = append(c.history, Turn{Role: "user", Content: input, Speaker: streamOpts.Speaker, At: time.Now().UTC()})

	resuming := c.sessionID != ""
	response, err := c.thinkFullAttempt(ctx, streamOpts)
	if err != nil && resuming && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "obey-voice: cursor resume failed, retrying without session: %v\n", err)
		c.dropSession()
		response, err = c.thinkFullAttempt(ctx, streamOpts)
	}
	if err != nil {
		c.history = c.history[:restore]
		return "", err
	}

	c.history = append(c.history, Turn{Role: "samantha", Content: response, At: time.Now().UTC()})
	c.trimHistory()

	return response, nil
}

func (c *CursorBrain) thinkFullAttempt(ctx context.Context, streamOpts StreamOptions) (string, error) {
	raw, err := c.askAttempt(ctx, streamOpts)
	if err != nil {
		return "", err
	}
	return spokenOrFallback(cleanForVoice(stripReplyConventions(c.agentName(), raw))), nil
}

// askAttempt runs a single print-mode Cursor turn and captures the session id
// for resume. The SDK's Classify already turns result.IsError into a Go error,
// so a rejected --resume falls through to the flatten retry.
func (c *CursorBrain) askAttempt(ctx context.Context, streamOpts StreamOptions) (string, error) {
	prompt := c.buildPrompt(streamOpts.OmitTurnInstruction)

	result, err := c.client.AskCtx(ctx, prompt, c.askOptions(streamOpts.ToolsEnabled))
	if result != nil && result.SessionID != "" {
		c.sessionID = result.SessionID
	}
	if result != nil {
		c.promptTokens = result.Usage.InputTokens + result.Usage.CacheReadTokens + result.Usage.CacheWriteTokens
		if streamOpts.OnUsage != nil {
			streamOpts.OnUsage(c.promptTokens, result.Usage.OutputTokens)
		}
	}
	if err != nil {
		return "", fmt.Errorf("cursor error: %w", err)
	}
	if result == nil {
		return "", fmt.Errorf("cursor error: empty result")
	}
	if result.IsError {
		return "", resultError("cursor", result.Subtype, result.Result)
	}
	return result.Result, nil
}

// dropSession clears the CLI resume id. Call whenever the session is
// intentionally abandoned (clear/load history, or a rejected --resume).
func (c *CursorBrain) dropSession() {
	c.sessionID = ""
}

// askOptions builds the print-mode options shared by the streaming and
// blocking paths. Tool execution is gated by voice_tools_enabled: disabled
// uses ask mode (no edits); enabled uses agent mode. Trust is always set so
// a first run in an untrusted workspace cannot stall on a human prompt.
// Force/yolo is never set — that lives behind the SDK's dangerous subpackage.
func (c *CursorBrain) askOptions(toolsEnabled bool) *cursor.AskOptions {
	opts := &cursor.AskOptions{
		Trust:            true,
		WorkingDirectory: config.WorkDirFrom(c.cfg),
		Resume:           c.sessionID,
	}
	if toolsEnabled {
		opts.Mode = cursor.ModeAgent
	} else {
		opts.Mode = cursor.ModeAsk
	}
	if c.cfg != nil && c.cfg.CursorModel != "" {
		opts.Model = c.cfg.CursorModel
	}
	return opts
}

// buildPrompt renders the next turn for the CLI. omitTurnInstruction leaves off
// the per-turn instruction so a meta turn's own instruction is the last thing
// the model reads. The system prompt is inlined on the flatten path because
// print mode has no --system-prompt flag.
func (c *CursorBrain) buildPrompt(omitTurnInstruction bool) string {
	var parts []string
	agentName := c.agentName()

	if c.sessionID == "" {
		if c.systemPrompt != "" {
			parts = append(parts, c.systemPrompt, "")
		}
		recent := c.history
		if len(recent) > 6 {
			recent = recent[len(recent)-6:]
		}

		if len(recent) > 1 {
			parts = append(parts, "Recent conversation:")
			for _, t := range recent[:len(recent)-1] {
				parts = append(parts, promptUserLine(t, agentName, c.speakerNames))
			}
			parts = append(parts, "")
		}
	}

	last := c.history[len(c.history)-1]
	parts = append(parts, promptUserLine(last, agentName, c.speakerNames))
	if !omitTurnInstruction {
		parts = append(parts, "")
		parts = append(parts, c.turnInstruction)
	}

	return strings.Join(parts, "\n")
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
	return c.history
}

// ClearHistory wipes conversation history. The CLI session id is dropped too
// so a fresh conversation starts a fresh CLI session instead of resuming the
// old one.
func (c *CursorBrain) ClearHistory() {
	c.history = nil
	c.dropSession()
}

// SessionInfo reports the live CLI session for /session display.
func (c *CursorBrain) SessionInfo() SessionState {
	return SessionState{Kind: SessionKindHarness, ID: c.sessionID, PromptTokens: c.promptTokens, Turns: len(c.history)}
}

// LoadHistory restores conversation history from a saved samantha session. No
// live CLI session exists for persisted history, so the id is cleared: the
// first turn after load runs the flatten path, then captures a new session id
// and resumes from there.
func (c *CursorBrain) LoadHistory(turns []Turn) {
	c.history = normalizePromptHistory(turns)
	c.dropSession()
}
