package voiceagent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func bindDecisions(p *pipeline.Pipeline, cfg *config.Config) func() {
	manager := decision.NewManager(&p.Decisions)
	p.DecisionStatus = manager.Status
	p.TestDecisions = manager.Test
	p.ConfigureDecisions = func(cfg *config.Config) {
		// Off must not inspect credentials, create directories, probe a server or
		// install anything. Both independent points off have the same behavior.
		if cfg == nil || !cfg.DecisionEnabled || (!cfg.DecisionVoiceWork && !cfg.DecisionTextWork) {
			manager.Configure(decision.Options{}, nil)
			return
		}
		snapshot := cfg.Clone()
		budget := snapshot.DecisionBudgetMS
		if budget == 0 {
			budget = 750
		}
		mode := snapshot.DecisionPolicy
		if mode == "" {
			mode = decision.PolicyRouteChoice
		}
		options := decision.Options{Policy: decision.WorkPolicy{Mode: mode}, Budget: time.Duration(budget) * time.Millisecond, VoiceWork: snapshot.DecisionVoiceWork, TextWork: snapshot.DecisionTextWork}
		manager.Configure(options, func(ctx context.Context) (decision.Evaluator, io.Closer, error) {
			provider, closer, err := prepareDecisions(ctx, snapshot)
			if err != nil && ctx.Err() == nil && p.Events != nil {
				p.Events.Emit(events.Info{Message: "Optional decisions unavailable: " + err.Error()})
			}
			return provider, closer, err
		})
	}
	p.ConfigureDecisions(cfg)
	return func() { _ = manager.Close() }
}

func prepareDecisions(ctx context.Context, cfg *config.Config) (decision.Evaluator, io.Closer, error) {
	if cfg.DecisionBudgetMS != 0 && (cfg.DecisionBudgetMS < 25 || cfg.DecisionBudgetMS > 3000) {
		return nil, nil, fmt.Errorf("decision deadline must be between 25 and 3000 ms")
	}
	if cfg.DecisionPolicy != "" && cfg.DecisionPolicy != decision.PolicyRouteChoice && cfg.DecisionPolicy != decision.PolicyIntentAndTier {
		return nil, nil, fmt.Errorf("unknown decision policy")
	}
	provider := strings.TrimSpace(cfg.DecisionProvider)
	if provider == "" {
		provider = "local"
	}
	if provider == "local" {
		endpoint := cfg.DecisionEndpoint
		var owned io.Closer
		switch cfg.DecisionRuntime {
		case "", "managed":
			// Preparation may verify/load installed assets, but never install or
			// repair them. Settings and explicit model commands own setup.
			server, err := decider.StartFor(ctx, config.ModelsDirFrom(cfg), cfg.DecisionLocalModel, cfg.DecisionDevice)
			if err != nil {
				return nil, nil, err
			}
			endpoint, owned = server.Endpoint(), server
		case "external":
		default:
			return nil, nil, fmt.Errorf("unknown local decision runtime")
		}
		p, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: endpoint})
		return p, owned, err
	}
	if provider != "jev" && provider != "openai" {
		return nil, nil, fmt.Errorf("unknown decision provider")
	}
	key := cfg.DecisionAPIKey
	if key == "" {
		key = os.Getenv("DECISION_API_KEY")
	}
	if provider == "openai" {
		p, err := decision.NewOpenAI(decision.OpenAIConfig{Model: cfg.DecisionModel, APIKey: key})
		return p, nil, err
	}
	p, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "jev", Model: cfg.DecisionModel, APIKey: key})
	return p, nil, err
}
