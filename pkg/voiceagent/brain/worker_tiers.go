package brain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/types/model"
)

// LocalWorkerCapabilities is the provider-layer capability resolver used by
// config when it builds a tier plan. It performs only static checks; the
// Ollama catalog is checked at dispatch by ValidateWorkerPlan.
type LocalWorkerCapabilities struct{}

// ValidateWorkerAssignment checks the parts of an assignment that can be
// trusted without contacting a provider.
func (LocalWorkerCapabilities) ValidateWorkerAssignment(a config.WorkerAssignment) error {
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if provider == "claude" || provider == "grok" || provider == "codex" {
		if a.EndpointRef != nil {
			return errors.New("endpoint_ref is unsupported for CLI workers")
		}
		if a.Billing == config.WorkerBillingLocal {
			return fmt.Errorf("CLI provider %q cannot use local billing", provider)
		}
		if a.Billing != config.WorkerBillingSubscription && a.Billing != config.WorkerBillingMetered {
			return fmt.Errorf("CLI provider %q requires explicit subscription or metered billing", provider)
		}
		if a.AccountRef == nil || strings.TrimSpace(*a.AccountRef) != "default" {
			return fmt.Errorf("CLI provider %q requires account_ref=default", provider)
		}
		if strings.TrimSpace(a.Model) == "" {
			return errors.New("model is required")
		}
		return nil
	}
	if provider != "ollama" {
		return fmt.Errorf("provider %q is not an approved worker adapter", a.Provider)
	}
	if a.Billing != config.WorkerBillingLocal {
		return fmt.Errorf("billing %q is not approved for local dispatch", a.Billing)
	}
	if strings.TrimSpace(a.Model) == "" {
		return errors.New("model is required")
	}
	if a.AccountRef != nil && strings.TrimSpace(*a.AccountRef) != "" {
		return errors.New("account_ref is unsupported for local Ollama")
	}
	if a.EndpointRef == nil {
		return errors.New("endpoint_ref is required for local Ollama")
	}
	_, err := approvedLoopbackEndpoint(*a.EndpointRef)
	return err
}

// ValidateWorkerPlan verifies a pinned plan against the local Ollama catalog.
// The probe is read-only and never invokes model inference.
func ValidateWorkerPlan(ctx context.Context, plan *config.WorkerPlan) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if plan == nil {
		return errors.New("nil worker plan")
	}
	if err := (LocalWorkerCapabilities{}).ValidateWorkerAssignment(plan.Assignment); err != nil {
		return fmt.Errorf("worker assignment rejected: %w", err)
	}
	provider := strings.ToLower(strings.TrimSpace(plan.Assignment.Provider))
	if provider == "claude" || provider == "grok" || provider == "codex" {
		return nil
	}
	endpoint, _ := approvedLoopbackEndpoint(*plan.Assignment.EndpointRef)
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := api.NewClient(endpoint, &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("redirects are not permitted for local Ollama probes")
	}})
	models, err := client.List(probeCtx)
	if err != nil {
		return fmt.Errorf("cannot inspect local Ollama catalog: %w", err)
	}
	modelName := strings.TrimSpace(plan.Assignment.Model)
	found := false
	for _, candidate := range models.Models {
		if candidate.Name == modelName || strings.TrimSuffix(candidate.Name, ":latest") == modelName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q is not installed in local Ollama", modelName)
	}
	shown, err := client.Show(probeCtx, &api.ShowRequest{Model: modelName})
	if err != nil {
		return fmt.Errorf("cannot inspect local Ollama model %q: %w", modelName, err)
	}
	if shown == nil {
		return errors.New("local Ollama returned empty model metadata")
	}
	if strings.TrimSpace(shown.RemoteHost) != "" || strings.TrimSpace(shown.RemoteModel) != "" {
		return fmt.Errorf("model %q is an Ollama remote or cloud alias", modelName)
	}
	if !hasCapability(shown.Capabilities, model.CapabilityCompletion) {
		return fmt.Errorf("local Ollama model %q does not support completion", modelName)
	}
	if !hasCapability(shown.Capabilities, model.CapabilityTools) {
		return fmt.Errorf("local Ollama model %q does not support tools", modelName)
	}
	if strings.TrimSpace(shown.Details.Family) == "" && len(shown.ModelInfo) == 0 && len(shown.Tensors) == 0 {
		return fmt.Errorf("local Ollama returned malformed metadata for model %q", modelName)
	}
	return nil
}

// NewPlannedWorker validates a pinned plan and constructs its provider using
// the assignment's provider, model, and endpoint verbatim.
func NewPlannedWorker(ctx context.Context, base *config.Config, plan *config.WorkerPlan) (Provider, error) {
	if base == nil {
		return nil, errors.New("nil config")
	}
	if err := ValidateWorkerPlan(ctx, plan); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(plan.Assignment.Provider))
	if provider == "claude" || provider == "grok" || provider == "codex" {
		cfg := base.Clone()
		cfg.VoiceWorkerProvider = provider
		cfg.VoiceWorkerModel = strings.TrimSpace(plan.Assignment.Model)
		config.SetBrainModel(cfg, provider, cfg.VoiceWorkerModel)
		workerCfg := config.WorkerBrainConfig(cfg)
		workerCfg.DelegationEnabled = false
		return NewProvider(workerCfg)
	}
	cfg := base.Clone()
	a := plan.Assignment
	cfg.VoiceWorkerProvider = strings.TrimSpace(strings.ToLower(a.Provider))
	cfg.VoiceWorkerModel = strings.TrimSpace(a.Model)
	cfg.VoiceWorkerOllamaHost = strings.TrimSpace(*a.EndpointRef)
	config.SetBrainModel(cfg, cfg.VoiceWorkerProvider, cfg.VoiceWorkerModel)
	// The embedding model is a separate mutable setting and is not part of a
	// worker plan. Keep the worker's skill catalog, but never probe or use that
	// secondary model while admitting a pinned job.
	workerCfg := config.WorkerBrainConfig(cfg)
	workerCfg.OllamaEmbeddingModel = ""
	workerCfg.DelegationEnabled = false
	return newOllama(ctx, workerCfg)
}

func hasCapability(capabilities []model.Capability, wanted model.Capability) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func approvedLoopbackEndpoint(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("endpoint_ref %q must be a concrete URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("endpoint_ref %q must use http or https", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("endpoint_ref must not contain credentials, query, or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("endpoint_ref %q must not contain a URL path", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("endpoint_ref host %q is not an approved loopback address", u.Hostname())
	}
	return u, nil
}

func (LocalWorkerCapabilities) WorkerCapabilities(a config.WorkerAssignment) config.WorkerCapabilities {
	return WorkerCapabilitiesFor(a.Provider)
}

// WorkerCapabilitiesFor describes enforcing adapter paths, not model pricing.
func WorkerCapabilitiesFor(provider string) config.WorkerCapabilities {
	if !strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		return CLIWorkerCapabilities(provider)
	}
	return config.WorkerCapabilities{
		Provider: "ollama", Adapter: "obey-voice Ollama worker", Version: "ollama-go/v0.32.3; worker-limits/v1",
		RequestBoundary:       supported("brain.chat", "every attempt acquires the job-shared request quota"),
		OutputTokenCeiling:    supported("Ollama num_predict", "remaining generation allowance on every request; missing usage blocks continuation"),
		ToolCallMediation:     supported("toolSession.execute", "acquires quota before each tool; strict policy refuses unmediated shell/network/delegation"),
		RetryLimit:            supported("brain.chat", "compatibility retries consume both retry and request quotas; zero forbids retries"),
		RuntimeLimit:          supported("job table/context", "pinned task deadline bounds local scheduling; provider and tool contexts cancel"),
		Usage:                 supported("Ollama response metrics", "full and stream paths report observed counts; missing fields remain unknown"),
		AuthoritativeBilling:  unsupported("local assignment only; no provider billing authority"),
		ChargeBound:           unsupported("no metered upper-bound certification; strict local tools exclude external execution"),
		ProcessExit:           unsupported("Ollama HTTP cancellation does not observe server process exit"),
		DescendantExit:        unsupported("arbitrary tool descendants are not observed"),
		CancellationRequested: supported("context", "cancellation sent to HTTP/tool calls; not proof of remote termination"),
		ResumablePreemption:   unsupported("running model requests cannot be safely preempted/resumed"),
	}
}
