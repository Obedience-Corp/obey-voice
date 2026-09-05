package stt

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

const defaultPostprocessTimeout = time.Second

// PostprocessReport describes the optional model stage as configured.
type PostprocessReport struct {
	Enabled  bool
	Provider string
	Model    string
	Mode     correct.Mode
	Timeout  time.Duration
	// Prompt says where the instruction comes from: a user file path, or
	// "embedded" for the shipped default.
	Prompt     string
	KnownNames int
	// Error says why an enabled stage is not running: the server is down, the
	// model is not pulled, the mode is unknown. Speech recognition starts
	// regardless.
	Error string
}

// PostprocessFromConfig builds the model stage when stt_postprocess_enabled
// is on. Everything about it comes from its own keys: a copy of cfg gets the
// stage's provider and model, thinking off, and the stage's residency, and is
// handed to that provider's batch adapter. The conversation's brain is never
// touched, and the loaded config is never mutated.
func PostprocessFromConfig(cfg *config.Config, cw *correct.CustomWords, terms []correct.Term) (*correct.Model, PostprocessReport) {
	rep := PostprocessReport{Enabled: cfg.STTPostprocessEnabled}
	if !rep.Enabled {
		return nil, rep
	}
	mode, err := correct.ParseMode(cfg.STTPostprocessMode)
	if err != nil {
		rep.Error = err.Error()
		return nil, rep
	}
	rep.Mode = mode
	provider := strings.ToLower(strings.TrimSpace(cfg.STTPostprocessProvider))
	if provider == "" {
		provider = "ollama"
	}
	rep.Provider = provider
	model := strings.TrimSpace(cfg.STTPostprocessModel)
	if model == "" {
		model = config.BrainModelFor(cfg, provider)
	}
	rep.Model = model
	rep.Timeout = time.Duration(cfg.STTPostprocessTimeoutMS) * time.Millisecond
	if rep.Timeout <= 0 {
		rep.Timeout = defaultPostprocessTimeout
	}

	derived := *cfg
	derived.BrainProvider = provider
	config.SetBrainModel(&derived, provider, model)
	var bp brain.BatchProvider
	if provider == "ollama" {
		derived.OllamaThink = "off"
		if ka := strings.TrimSpace(cfg.STTPostprocessKeepAlive); ka != "" {
			derived.OllamaKeepAlive = ka
		}
		bp, err = brain.NewOllamaBatchLight(&derived)
	} else {
		bp, err = brain.NewBatchProvider(&derived)
	}
	if err != nil {
		rep.Error = err.Error()
		return nil, rep
	}

	known := make([]string, 0, len(terms))
	for _, t := range terms {
		known = append(known, t.Text)
	}
	rep.KnownNames = len(known)
	prompt := newPostprocessPrompt(config.PromptsDirFrom(cfg), mode, cfg, strings.Join(known, ", "))
	rep.Prompt = prompt.source()

	m := &correct.Model{T: batchTransformer{bp: bp}, Mode: mode, Prompt: prompt.text, Timeout: rep.Timeout, Known: known}
	if cw != nil && !cw.Empty() {
		m.Candidates = cw.Candidates
	}
	if w, ok := bp.(brain.Warmer); ok {
		// Load the model now, off the critical path, so the first transcript
		// that needs it does not pay the load inside its deadline.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			w.Warmup(ctx)
		}()
	}
	return m, rep
}

// batchTransformer adapts a brain batch provider to the stage's Transformer.
type batchTransformer struct {
	bp brain.BatchProvider
}

func (t batchTransformer) Transform(ctx context.Context, system, user string) (string, error) {
	res, err := t.bp.Transform(ctx, brain.BatchRequest{SystemPrompt: system, Text: user})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// postprocessPrompt resolves the instruction document on every call, so an
// edit takes effect on the next turn without a restart. The last good text
// stands in when a read fails.
type postprocessPrompt struct {
	mu    sync.Mutex
	dir   string
	mode  correct.Mode
	agent string
	env   prompts.PlaceholderEnv
	known string
	last  string
}

func newPostprocessPrompt(dir string, mode correct.Mode, cfg *config.Config, known string) *postprocessPrompt {
	return &postprocessPrompt{
		dir:   dir,
		mode:  mode,
		agent: cfg.AgentName,
		env:   prompts.PlaceholderEnv{UserName: cfg.RuntimeEnvUser, OS: cfg.RuntimeEnvOS},
		known: known,
	}
}

// text renders the current document. {known_names} is the one placeholder
// beyond the persona set, and the transcript itself is never a placeholder:
// it travels as the user message, so a transcript cannot rewrite the
// instructions.
func (p *postprocessPrompt) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	doc, err := prompts.Resolver{UserDir: p.dir}.Resolve(prompts.KindPostprocess, string(p.mode))
	if err != nil {
		return p.last
	}
	raw := doc.Assemble()
	env := p.env
	env.Now = time.Now()
	values := prompts.PlaceholderValues(p.agent, env)
	values["known_names"] = p.known
	allowed := append(prompts.PlaceholderNames(), "known_names")
	text, err := prompts.ResolvePlaceholders(raw, allowed, values)
	if err != nil {
		if p.last == "" {
			p.last = strings.ReplaceAll(raw, "{known_names}", p.known)
		}
		return p.last
	}
	p.last = text
	return text
}

// source names the document in use: the user's file when one exists.
func (p *postprocessPrompt) source() string {
	entry, err := prompts.Describe(p.dir, prompts.KindPostprocess, string(p.mode))
	if err != nil || entry.Source != prompts.SourceUser || entry.Path == "" {
		return "embedded"
	}
	return entry.Path
}
