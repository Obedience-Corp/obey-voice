package brain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

// OllamaBrain implements Provider using the Ollama API with tool calling.
type OllamaBrain struct {
	retiredDelegations  sync.WaitGroup
	backgroundDelivered string
	delegation          *delegation.Runtime
	delegationBorrowed  bool
	client              *api.Client
	continuityMu        sync.Mutex
	// continuitySink receives this instance's continuity events (integrity.go).
	continuitySink func(events.Event)
	infoMu         sync.Mutex
	// infoSink receives this instance's operator Info (think-off warnings).
	infoSink     func(events.Info)
	model        string
	workDir      string
	history      []api.Message
	canonical    []Turn
	working      []Turn
	cfg          *config.Config
	systemPrompt string
	// fullSystemPrompt is the assembled prompt (persona + environment + skills
	// catalog + turn instruction) sent byte-for-byte on every request, so
	// Ollama's KV prefix cache survives across turns. It is rebuilt only when
	// the persona document actually changes. Per-turn skill activations are
	// spliced onto the current user message instead.
	fullSystemPrompt string
	// stateMu guards the fields Warmup reads from its own goroutine while the
	// conversation loop writes them: fullSystemPrompt (rebuilt when the
	// persona document changes), delegation (reset when history is loaded)
	// and contextHighWater (reset with history, raised when the context
	// expands). Everything else Warmup touches is fixed at construction.
	stateMu sync.Mutex
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
	// skillCallSeq numbers synthesized activate_skill call ids for this brain.
	skillCallSeq     int
	budgetWarned     bool
	archive          continuity.Store
	contextHighWater int
	// speakerNames resolves stable speaker ids when building chat messages.
	speakerNames      SpeakerNames
	groupSpeakers     []string           // public names in imported shared history; guarded by stateMu
	groupParticipants []GroupParticipant // guarded by stateMu
	groupHistory      bool               // foreign canonical speakers; guarded by stateMu
}

// NewOllama creates an Ollama brain provider.
func NewOllama(cfg *config.Config) (*OllamaBrain, error) {
	return newOllama(context.Background(), cfg)
}

// newOllama is the context-aware constructor used when a provider is created
// on a job goroutine. All catalog and optional skill work remains bounded by
// the caller's cancellation and deadline.
func newOllama(parent context.Context, cfg *config.Config) (*OllamaBrain, error) {
	if parent == nil {
		return nil, errors.New("nil context")
	}
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
	probeCtx, cancel := context.WithTimeout(parent, 5*time.Second)
	models, err := probe.List(probeCtx)
	cancel()
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

	catalog, err := loadSkillsCatalog(parent, cfg, workDir)
	if err != nil {
		return nil, err
	}
	embedCtx, embedCancel := context.WithTimeout(parent, 10*time.Second)
	router, routerErr := newSemanticSkillRouter(embedCtx, client, cfg.OllamaEmbeddingModel, cfg.SkillsSimilarityThreshold, catalog)
	embedCancel()
	if err := parent.Err(); err != nil {
		return nil, fmt.Errorf("initialize ollama: %w", err)
	}
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

func init() {
	register(config.BrainProviderSpec{
		Name: "ollama", Description: "Local Ollama server",
	}, func(cfg *config.Config) (Provider, error) {
		return NewOllama(cfg)
	}, func(cfg *config.Config) (BatchProvider, error) {
		return newOllamaBatch(cfg)
	})
}

// SetInfoSink routes this brain's operator Info onto its owning pipeline's
// event bus. Instance-scoped: with several pipelines active, each brain's
// warnings stay on its own conversation's bus.
func (o *OllamaBrain) SetInfoSink(fn func(events.Info)) {
	o.infoMu.Lock()
	o.infoSink = fn
	o.infoMu.Unlock()
}

func (o *OllamaBrain) FreezePersonaPrompt() string {
	o.personaReloader = nil
	return shortPromptHash(o.systemPrompt)
}

func (o *OllamaBrain) emitInfo(info events.Info) {
	o.infoMu.Lock()
	sink := o.infoSink
	o.infoMu.Unlock()
	if sink != nil {
		sink(info)
	}
}
