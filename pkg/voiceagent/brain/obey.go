package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// obeyRunner keeps the SDK transport replaceable without reimplementing its
// protocol. Every operation owns its client and its one-shot daemon session.
type obeyRunner interface {
	run(context.Context, string, *obey.RunOptions, func(obey.Event) error) (*obey.Result, error)
}

type obeySDKRunner struct{ cfg *config.Config }

func (r obeySDKRunner) run(ctx context.Context, prompt string, opts *obey.RunOptions, event func(obey.Event) error) (*obey.Result, error) {
	client, err := obey.NewClientFromPath(obey.WithSocket(r.cfg.ObeySocket), obey.WithWorkingDirectory(config.WorkDirFrom(r.cfg)))
	if err != nil {
		return nil, fmt.Errorf("obey CLI not available; install Obey or select another brain provider: %w", err)
	}
	defer client.Close()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.StreamPrompt(runCtx, prompt, opts)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var callbackErr error
	for stream.Next() {
		if err := event(stream.Event()); err != nil {
			callbackErr = err
			cancel()
			break
		}
	}
	// Result drains bounded cleanup even after cancellation. Never replay a run:
	// a failed response may follow completed daemon-side tool effects.
	result, err := stream.Result()
	if callbackErr != nil {
		return result, callbackErr
	}
	return result, err
}

// ObeyBrain retains Voice's transcript and bounded working history. Each turn
// replays the working view into a new SDK-owned run, so switching personas and
// compaction do not inherit a daemon session's hidden conversation state.
type ObeyBrain struct {
	groupContext
	client                                       obeyRunner
	cfg                                          *config.Config
	personaPrompt, systemPrompt, turnInstruction string
	personaReloader, turnReloader                *promptReloader
	canonical, working                           []Turn
	speakerNames                                 SpeakerNames
	snapshot                                     ContinuitySnapshot
}

func init() {
	register(config.BrainProviderSpec{Name: "obey", Description: "Obey daemon", CLIBinary: "obey"},
		func(cfg *config.Config) (Provider, error) { return NewObey(cfg) },
		func(cfg *config.Config) (BatchProvider, error) { return newObeyBatch(cfg) })
}

func validateObeyConfig(cfg *config.Config) error {
	if cfg == nil || strings.TrimSpace(cfg.ObeyProvider) == "" || strings.TrimSpace(cfg.ObeyModel) == "" {
		return fmt.Errorf("obey requires obey_provider and obey_model configured in the daemon")
	}
	if cfg.ObeyToolPolicy != "" && cfg.ObeyToolPolicy != "strict" && cfg.ObeyToolPolicy != "daemon" {
		return fmt.Errorf("unsupported obey_tool_policy %q", cfg.ObeyToolPolicy)
	}
	return nil
}

func checkObeyInstalled() error {
	client, err := obey.NewClientFromPath()
	if err != nil {
		return fmt.Errorf("obey CLI not found on PATH; install Obey or select another brain provider: %w", err)
	}
	return client.Close()
}

func NewObey(cfg *config.Config) (*ObeyBrain, error) {
	if err := checkObeyInstalled(); err != nil {
		return nil, err
	}
	if err := validateObeyConfig(cfg); err != nil {
		return nil, err
	}
	persona, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	turn, err := turnInstruction(cfg)
	if err != nil {
		return nil, err
	}
	cfg = cfg.Clone()
	b := &ObeyBrain{client: obeySDKRunner{cfg: cfg}, cfg: cfg, personaPrompt: persona, turnInstruction: turn,
		personaReloader: newPromptReloader(prompts.KindPersona, cfg.Persona, persona, nil),
		turnReloader:    newPromptReloader(prompts.KindTurn, cfg.TurnPrompt, turn, nil)}
	b.systemPrompt = b.assembleSystem(persona)
	return b, nil
}

func (b *ObeyBrain) assembleSystem(persona string) string {
	return AssembleSystemPrompt(SystemPromptInput{Provider: "obey", Persona: persona, WorkDir: config.WorkDirFrom(b.cfg), Cfg: b.cfg})
}
func (b *ObeyBrain) refreshPrompts(warn func(string)) {
	if b.personaReloader != nil {
		if p, _, err := b.personaReloader.resolve(b.cfg); err == nil {
			b.personaPrompt = p
			b.systemPrompt = b.assembleSystem(p)
		} else if warn != nil {
			warn(err.Error())
		}
	}
	if b.turnReloader != nil {
		if p, _, err := b.turnReloader.resolve(b.cfg); err == nil {
			b.turnInstruction = p
		} else if warn != nil {
			warn(err.Error())
		}
	}
}
func (b *ObeyBrain) ThinkStream(ctx context.Context, input string, opts StreamOptions) (*Stream, error) {
	b.refreshPrompts(opts.OnPromptWarn)
	if _, err := b.runOptions(liveCLITools(opts)); err != nil {
		return nil, err
	}
	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		_, err := b.think(ctx, input, opts, func(s string) error { return sendChunk(ctx, out, s) })
		done <- StreamResult{Err: err}
	}()
	return &Stream{Chunks: out, Done: done}, nil
}
func (b *ObeyBrain) ThinkFull(ctx context.Context, input string, opts StreamOptions) (string, error) {
	b.refreshPrompts(opts.OnPromptWarn)
	return b.think(ctx, input, opts, func(string) error { return nil })
}

func (b *ObeyBrain) runOptions(tools bool) (*obey.RunOptions, error) {
	if !tools && b.cfg.ObeyToolPolicy != "daemon" {
		return nil, fmt.Errorf("obey: cannot enforce tools off: set obey_tool_policy to daemon to use daemon-managed permissions, or select another brain provider")
	}
	mode := "discussion"
	if tools {
		mode = "supervised"
	}
	return &obey.RunOptions{SessionOptions: obey.SessionOptions{
		Provider: strings.TrimSpace(b.cfg.ObeyProvider), Model: strings.TrimSpace(b.cfg.ObeyModel),
		Camp: strings.TrimSpace(b.cfg.ObeyCamp), WorkingDirectory: config.WorkDirFrom(b.cfg), Mode: mode,
	}}, nil
}

func (b *ObeyBrain) think(ctx context.Context, input string, opts StreamOptions, emit func(string) error) (string, error) {
	options, err := b.runOptions(liveCLITools(opts))
	if err != nil {
		return "", err
	}
	if err = opts.workerBudget().AllowTool("Obey daemon tools"); err != nil {
		return "", err
	}
	var raw strings.Builder
	strip := newLabelStripper(b.cfg.AgentName)
	feed := func(text string) error {
		raw.WriteString(text)
		if s := strip.Feed(text); s != "" {
			return emit(s)
		}
		return nil
	}
	calls := make(map[string]ToolCall)
	result, err := b.client.run(ctx, b.buildPrompt(input, opts), options, func(ev obey.Event) error {
		// Never send reasoning, tool output, diagnostics or approval text to TTS.
		if ev.Delta != nil && ev.Delta.ContentKind == "assistant" {
			return feed(ev.Delta.Text)
		}
		if call := ev.ToolCall; call != nil {
			tool := ToolCall{ID: call.CallID, Name: call.Name, Kind: KindFor(call.Name), Summary: call.Name}
			calls[call.CallID] = tool
			if opts.OnToolStart != nil {
				opts.OnToolStart(tool)
			}
		}
		if result := ev.ToolResult; result != nil && opts.OnToolEnd != nil {
			tool, ok := calls[result.CallID]
			if !ok {
				tool = ToolCall{ID: result.CallID, Kind: KindOther}
			}
			outcome := &events.ToolOutcome{Status: events.ToolOutcomeUnknown}
			if result.Success != nil {
				outcome.Status = events.ToolOutcomeFailed
				if *result.Success {
					outcome.Status = events.ToolOutcomeSucceeded
				}
			}
			if result.Error != "" {
				outcome.Status = events.ToolOutcomeFailed
			}
			opts.OnToolEnd(ToolResult{ToolCall: tool, Err: result.Error, Outcome: outcome})
			delete(calls, result.CallID)
		}
		if ev.Approval != nil {
			return fmt.Errorf("obey: requires approval for %s; resolve daemon permissions before retrying", ev.Approval.ToolName)
		}
		return nil
	})
	if result != nil {
		if result.Cleanup == obey.CleanupStopFailed && opts.OnPromptWarn != nil {
			opts.OnPromptWarn("Obey could not stop its owned session " + result.Session.SessionID + "; inspect it in the daemon")
		}
		reportObeyUsage(result.Usage, opts)
	}
	if err != nil {
		return "", fmt.Errorf("obey: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("obey completed without a result")
	}
	// v1 has no live deltas. v2 final text repeats deltas; emit only a missing
	// suffix, and reject a conflicting final rather than speaking it twice.
	final := result.Text
	if raw.Len() == 0 {
		if err = feed(final); err != nil {
			return "", err
		}
	} else if strings.HasPrefix(final, raw.String()) {
		if err = feed(strings.TrimPrefix(final, raw.String())); err != nil {
			return "", err
		}
	} else if final != "" {
		return "", fmt.Errorf("obey completion disagrees with streamed text")
	}
	if s := strip.Flush(); s != "" {
		if err = emit(s); err != nil {
			return "", err
		}
	}
	response := cleanForVoice(stripReplyConventions(b.cfg.AgentName, raw.String()))
	if strings.TrimSpace(response) == "" {
		return "", fmt.Errorf("obey completed without assistant text")
	}
	if !opts.TransientHistory {
		if !opts.AgentContinuation {
			t := Turn{Role: "user", Content: input, Speaker: opts.Speaker, At: time.Now().UTC()}
			if opts.OriginalInput != "" {
				t.Content = opts.OriginalInput
				t.RoutedInput = input
			}
			b.appendTurn(t)
		}
		reply := Turn{Role: "assistant", Content: response, At: time.Now().UTC(), PersonaID: b.cfg.ActivePersona, PersonaDisplayName: b.cfg.AgentName}
		if i := opts.Identity; i != nil {
			reply.PersonaID = i.PersonaID
			reply.PersonaDisplayName = i.PersonaDisplayName
			reply.PromptHash = i.PromptHash
			reply.ResponseID = i.ResponseID
		}
		b.appendTurn(reply)
	}
	return response, nil
}

func reportObeyUsage(usage obey.Usage, opts StreamOptions) {
	// SDK zeroes can mean missing fields. Only complete accounting establishes
	// exact totals; partial/missing usage stays unknown to budget enforcement.
	if usage.Complete == nil || !*usage.Complete {
		if opts.OnWorkerUsage != nil {
			opts.OnWorkerUsage(WorkerUsage{})
		}
		return
	}
	in, out := int(usage.InputTokens), int(usage.OutputTokens)
	if opts.OnUsage != nil {
		opts.OnUsage(in, out)
	}
	if opts.OnWorkerUsage != nil {
		opts.OnWorkerUsage(WorkerUsage{InputTokens: &in, OutputTokens: &out})
	}
}

func (b *ObeyBrain) buildPrompt(input string, opts StreamOptions) string {
	parts := []string{b.systemPrompt}
	if !opts.AgentContinuation {
		parts = append(parts, ApplicationRequestContext(b.cfg, "obey", input))
	}
	parts = append(parts, b.groupContext.prompt(b.cfg, b.cfg.AgentName, b.working))
	if len(b.working) > 0 {
		parts = append(parts, systemPrompt(b.cfg, "daemon-history", nil))
		for _, t := range b.working {
			parts = append(parts, cliHistoryRecord(t, b.speakerNames))
		}
	}
	parts = append(parts, WorkResultsPrompt(opts.WorkResults))
	if opts.AgentContinuation {
		parts = append(parts, systemPrompt(b.cfg, "daemon-continuation", nil), input)
	} else {
		parts = append(parts, promptUserLine(Turn{Role: "user", Content: input, Speaker: opts.Speaker}, b.cfg.AgentName, b.speakerNames))
	}
	if !opts.OmitTurnInstruction {
		parts = append(parts, b.turnInstruction)
	}
	return joinPromptParts(parts...) + harnessRoutingContext(b.cfg, opts) + currentRequestContext(b.cfg, opts)
}
