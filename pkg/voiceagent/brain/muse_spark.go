package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// museRunner keeps process ownership and cancellation in the SDK.
type museRunner interface {
	StreamPrompt(context.Context, string, *muse.RunOptions) (<-chan muse.Event, <-chan error)
	RunPromptCtx(context.Context, string, *muse.RunOptions) (*muse.Result, error)
}

// MuseSparkBrain replays the host-owned conversation into independent Muse exec
// calls. No native session can retain a cleared, compacted, or transient turn.
type MuseSparkBrain struct {
	groupContext
	client                                       museRunner
	cfg                                          *config.Config
	personaPrompt, systemPrompt, turnInstruction string
	personaReloader, turnReloader                *promptReloader
	canonical, working                           []Turn
	snapshot                                     ContinuitySnapshot
	speakerNames                                 SpeakerNames
}

func NewMuseSpark(cfg *config.Config) (*MuseSparkBrain, error) {
	client, err := muse.NewClientFromPath()
	if err != nil {
		return nil, fmt.Errorf("muse CLI not found: %w", err)
	}
	client.WorkingDir = config.WorkDirFrom(cfg)
	persona, err := personaSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	turn, err := turnInstruction(cfg)
	if err != nil {
		return nil, err
	}
	b := &MuseSparkBrain{client: client, cfg: cfg, personaPrompt: persona, turnInstruction: turn,
		personaReloader: newPromptReloader(prompts.KindPersona, cfg.Persona, persona, nil),
		turnReloader:    newPromptReloader(prompts.KindTurn, cfg.TurnPrompt, turn, nil)}
	b.systemPrompt = b.assembleSystem(persona)
	return b, nil
}

func (b *MuseSparkBrain) assembleSystem(persona string) string {
	return AssembleSystemPrompt(SystemPromptInput{Provider: "muse-spark", Persona: persona, WorkDir: config.WorkDirFrom(b.cfg), Cfg: b.cfg})
}
func (b *MuseSparkBrain) refreshPrompts(warn func(string)) {
	if b.personaReloader != nil {
		if text, _, err := b.personaReloader.resolve(b.cfg); err == nil {
			b.personaPrompt = text
			b.systemPrompt = b.assembleSystem(text)
		} else if warn != nil {
			warn(err.Error())
		}
	}
	if b.turnReloader != nil {
		if text, _, err := b.turnReloader.resolve(b.cfg); err == nil {
			b.turnInstruction = text
		} else if warn != nil {
			warn(err.Error())
		}
	}
}
func (b *MuseSparkBrain) SetSpeakerNames(names SpeakerNames) { b.speakerNames = names }
func (b *MuseSparkBrain) agentName() string {
	if b.cfg != nil {
		return b.cfg.AgentName
	}
	return ""
}

func museRunOptions(cfg *config.Config, tools bool) *muse.RunOptions {
	o := &muse.RunOptions{Provider: "meta", Workspace: config.WorkDirFrom(cfg), Worktree: "off",
		NoSessionLog: true, NoForeignPersonalContext: true, DisableRequestUserInput: true, DisableReminders: true,
		// Headless calls cannot present approvals. Keep Muse's sandbox and permission
		// profile; requests requiring approval fail rather than hanging on a dialog.
		ApprovalMode: "never"}
	if cfg != nil {
		o.Model = strings.TrimSpace(cfg.MuseSparkModel)
		if o.Model == "default" {
			o.Model = ""
		}
	}
	if !tools {
		o.Preset = "empty"
		o.DisableWrite, o.DisableShell, o.DisableWebTools = true, true, true
	}
	return o
}

func (b *MuseSparkBrain) buildPrompt(input string, o StreamOptions) string {
	parts := []string{b.systemPrompt}
	for _, t := range b.working {
		parts = append(parts, historyReplayRecord(t, b.speakerNames))
	}
	if group := b.groupContext.prompt(b.cfg, b.agentName(), b.canonical); group != "" {
		parts = append(parts, group)
	}
	if app := ApplicationRequestContext(b.cfg, "muse-spark", input); app != "" {
		parts = append(parts, app)
	}
	if o.AgentContinuation {
		parts = append(parts, input)
	} else {
		parts = append(parts, promptUserLine(Turn{Role: "user", Content: input, Speaker: o.Speaker}, b.agentName(), b.speakerNames))
	}
	parts = append(parts, WorkResultsPrompt(o.WorkResults))
	if !o.OmitTurnInstruction {
		parts = append(parts, b.turnInstruction)
	}
	return joinPromptParts(parts...)
}

func (b *MuseSparkBrain) commit(input, response string, o StreamOptions) {
	if o.TransientHistory {
		return
	}
	if !o.AgentContinuation {
		if o.OriginalInput != "" {
			input = o.OriginalInput
		}
		b.appendTurn(Turn{Role: "user", Content: input, Speaker: o.Speaker, At: time.Now().UTC()})
	}
	identity := TurnIdentity{}
	if o.Identity != nil {
		identity = *o.Identity
	}
	b.appendTurn(attributedHostTurn(response, identity))
}

func (b *MuseSparkBrain) ThinkFull(ctx context.Context, input string, o StreamOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if liveCLITools(o) {
		if err := o.workerBudget().AllowTool("Muse native tools"); err != nil {
			return "", err
		}
	}
	b.refreshPrompts(o.OnPromptWarn)
	result, err := b.client.RunPromptCtx(ctx, b.buildPrompt(input, o), museRunOptions(b.cfg, liveCLITools(o)))
	if err != nil {
		return "", fmt.Errorf("muse-spark: %w", err)
	}
	if result == nil {
		return "", fmt.Errorf("muse-spark: empty result")
	}
	if result.Terminal != "completed" {
		return "", fmt.Errorf("muse-spark: turn %s", result.Terminal)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	response := spokenOrFallback(cleanForVoice(stripReplyConventions(b.agentName(), result.Text)))
	b.commit(input, response, o)
	if o.OnWorkerUsage != nil {
		o.OnWorkerUsage(WorkerUsage{})
	}
	return response, nil
}

func (b *MuseSparkBrain) ThinkStream(ctx context.Context, input string, o StreamOptions) (*Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if liveCLITools(o) {
		if err := o.workerBudget().AllowTool("Muse native tools"); err != nil {
			return nil, err
		}
	}
	b.refreshPrompts(o.OnPromptWarn)
	prompt := b.buildPrompt(input, o)
	out := make(chan string, 8)
	done := make(chan StreamResult, 1)
	go func() {
		defer close(out)
		defer close(done)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		events, errs := b.client.StreamPrompt(ctx, prompt, museRunOptions(b.cfg, liveCLITools(o)))
		strip := newLabelStripper(b.agentName())
		var raw strings.Builder
		terminal, finalText := "", ""
		var streamErr error
		for event := range events {
			switch {
			case event.Type == "run.output.delta":
				var p struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(event.Payload, &p); err != nil {
					streamErr = fmt.Errorf("muse-spark output: %w", err)
					cancel()
					continue
				}
				raw.WriteString(p.Text)
				if streamErr == nil {
					if emit := strip.Feed(p.Text); emit != "" {
						if err := sendChunk(ctx, out, emit); err != nil {
							streamErr = err
							cancel()
						}
					}
				}
			case strings.HasPrefix(event.Type, "run.terminal."):
				var p struct {
					Terminal string `json:"terminal"`
					Text     string `json:"text"`
				}
				if err := json.Unmarshal(event.Payload, &p); err != nil {
					streamErr = fmt.Errorf("muse-spark terminal: %w", err)
					cancel()
					continue
				}
				terminal, finalText = p.Terminal, p.Text
				if event.Type != "run.terminal."+terminal {
					streamErr = fmt.Errorf("muse-spark: inconsistent terminal")
				}
			}
		}
		for err := range errs {
			if err != nil && streamErr == nil {
				streamErr = fmt.Errorf("muse-spark stream: %w", err)
			}
		}
		if streamErr == nil {
			streamErr = ctx.Err()
		}
		if streamErr == nil && terminal != "completed" {
			streamErr = fmt.Errorf("muse-spark: turn ended %q", terminal)
		}
		if streamErr != nil {
			done <- StreamResult{Err: streamErr}
			return
		}
		// Some providers emit only terminal text. Never repeat a reply that already
		// arrived as deltas, and never forward tool, reasoning, or task error events.
		if raw.Len() == 0 {
			raw.WriteString(finalText)
			if err := sendChunk(ctx, out, strip.Feed(finalText)); err != nil {
				done <- StreamResult{Err: err}
				return
			}
		}
		if err := sendChunk(ctx, out, strip.Flush()); err != nil {
			done <- StreamResult{Err: err}
			return
		}
		response, err := finalizeStreamedText(ctx, out, stripReplyConventions(b.agentName(), raw.String()))
		if err != nil {
			done <- StreamResult{Err: err}
			return
		}
		b.commit(input, response, o)
		if o.OnWorkerUsage != nil {
			o.OnWorkerUsage(WorkerUsage{})
		}
		done <- StreamResult{}
	}()
	return &Stream{Chunks: out, Done: done}, nil
}

type museSparkBatch struct {
	client museRunner
	cfg    *config.Config
}

func (b *museSparkBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}
	result, err := b.client.RunPromptCtx(ctx, AssembleBatchPrompt(req), museRunOptions(b.cfg, false))
	if err != nil {
		return BatchResult{}, fmt.Errorf("muse-spark batch: %w", err)
	}
	if result == nil || result.Terminal != "completed" {
		return BatchResult{}, fmt.Errorf("muse-spark batch: no completed result")
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return BatchResult{}, fmt.Errorf("muse-spark batch: empty response")
	}
	return BatchResult{Text: text, Provider: "muse-spark", Model: modelIDOrDefault(b.cfg.MuseSparkModel)}, nil
}
func init() {
	register(config.BrainProviderSpec{Name: "muse-spark", Description: "Muse Spark CLI", CLIBinary: "muse"},
		func(cfg *config.Config) (Provider, error) { return NewMuseSpark(cfg) },
		func(cfg *config.Config) (BatchProvider, error) {
			client, err := muse.NewClientFromPath()
			if err != nil {
				return nil, fmt.Errorf("muse CLI not found: %w", err)
			}
			client.WorkingDir = config.WorkDirFrom(cfg)
			return &museSparkBatch{client: client, cfg: cfg.Clone()}, nil
		})
}

func (b *MuseSparkBrain) FreezePersonaPrompt() string {
	b.personaReloader = nil
	return shortPromptHash(b.personaPrompt)
}

var _ FreshProvider = (*MuseSparkBrain)(nil)
var _ IdentityHistoryAppender = (*MuseSparkBrain)(nil)
var _ SessionReporter = (*MuseSparkBrain)(nil)
