package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const exitSkip = 2

type knobs struct {
	sustainedWorker  bool
	liveProvider     string
	liveModel        string
	workerProvider   string
	workerModel      string
	workerOllamaHost string
	ollamaHost       string
	gate             bool
}

// skipError is a missing dependency, not a failed gate: exit 2.
type skipError struct{ reason string }

func (e skipError) Error() string { return "skip: " + e.reason }

func (k knobs) workerIsStub() bool { return strings.EqualFold(k.workerProvider, "stub") }

// withScenario fills every knob the command line left empty from the
// scenario's brain / worker blocks, so the probe and resolveConfig see one
// effective set of values: a scenario that pins a worker model or host is
// probed for that model on that host, not for the inherited live pair.
func (k knobs) withScenario(scn *Scenario) knobs {
	if scn == nil {
		return k
	}
	k.sustainedWorker = scn.Worker.SustainedLoad
	k.liveProvider = firstNonEmpty(k.liveProvider, scn.Brain.Provider)
	k.liveModel = firstNonEmpty(k.liveModel, scn.Brain.Model)
	k.ollamaHost = firstNonEmpty(k.ollamaHost, scn.Brain.Host)
	if !k.workerIsStub() {
		k.workerProvider = firstNonEmpty(k.workerProvider, scn.Worker.Provider)
	}
	k.workerModel = firstNonEmpty(k.workerModel, scn.Worker.Model)
	k.workerOllamaHost = firstNonEmpty(k.workerOllamaHost, scn.Worker.Host)
	return k
}

// withFixtureConfig applies an explicitly supplied isolated config beneath
// command-line and scenario values. It is used before probing so the probe
// and the eventual runner resolve the same endpoint and model.
func (k knobs) withFixtureConfig(base *config.Config, scn *Scenario) knobs {
	k = k.withScenario(scn)
	if base != nil {
		k.liveProvider = firstNonEmpty(k.liveProvider, base.BrainProvider)
		k.liveModel = firstNonEmpty(k.liveModel, base.OllamaModel)
		k.ollamaHost = firstNonEmpty(k.ollamaHost, base.OllamaHost)
		k.workerProvider = firstNonEmpty(k.workerProvider, base.VoiceWorkerProvider)
		k.workerModel = firstNonEmpty(k.workerModel, base.VoiceWorkerModel)
		k.workerOllamaHost = firstNonEmpty(k.workerOllamaHost, base.VoiceWorkerOllamaHost)
	}
	return k
}

func (k knobs) withDefaults() knobs {
	k.liveProvider = firstNonEmpty(k.liveProvider, "ollama")
	k.liveModel = firstNonEmpty(k.liveModel, "qwen3:8b")
	k.workerProvider = firstNonEmpty(k.workerProvider, "ollama")
	k.ollamaHost = firstNonEmpty(k.ollamaHost, "http://localhost:11434")
	return k
}

func (k knobs) resolvedWorkerModel() string {
	if m := strings.TrimSpace(k.workerModel); m != "" {
		return m
	}
	return strings.TrimSpace(k.liveModel)
}

func (k knobs) resolvedWorkerHost() string {
	if h := strings.TrimSpace(k.workerOllamaHost); h != "" {
		return h
	}
	return strings.TrimSpace(k.ollamaHost)
}

// skipReason covers what can be checked without a network round trip.
func skipReason(k knobs) string {
	if k.gate && k.workerIsStub() {
		return "gate refuses stub worker; pass -worker-provider=ollama or unset CONVOFLOW_WORKER_PROVIDER"
	}
	if strings.TrimSpace(k.liveModel) == "" && (strings.EqualFold(k.liveProvider, "ollama") || k.liveProvider == "") {
		return "live_model is empty"
	}
	if strings.EqualFold(k.workerProvider, "grok") {
		if _, err := exec.LookPath("grok"); err != nil {
			return "grok CLI not on PATH; use worker_provider=ollama for the local gate"
		}
	}
	if strings.EqualFold(k.workerProvider, "claude") {
		if _, err := exec.LookPath("claude"); err != nil {
			return "claude CLI not on PATH; use worker_provider=ollama for the local gate"
		}
	}
	return ""
}

// probeOllama runs before voiceagent.New: the daemon must answer and every
// Ollama model the run needs must already be pulled. Pass the effective
// knobs (withScenario) — the same values resolveConfig builds from — or a
// scenario override reaches a target the probe never looked at. A stub
// worker has no model to check. Distinct local models are allowed
// (NewWorkerProvider warns).
func probeOllama(ctx context.Context, k knobs) string {
	tags := map[string][]string{}
	fetch := func(host string) ([]string, string) {
		if names, ok := tags[host]; ok {
			return names, ""
		}
		names, err := ollamaTags(ctx, host)
		if err != nil {
			return nil, fmt.Sprintf("ollama not reachable at %s: %v", host, err)
		}
		tags[host] = names
		return names, ""
	}
	if strings.EqualFold(k.liveProvider, "ollama") || k.liveProvider == "" {
		names, reason := fetch(strings.TrimSpace(k.ollamaHost))
		if reason != "" {
			return reason
		}
		if !hasModel(names, k.liveModel) {
			return fmt.Sprintf("live model %q is not pulled on %s (ollama pull %s)", k.liveModel, k.ollamaHost, k.liveModel)
		}
	}
	if strings.EqualFold(k.workerProvider, "ollama") {
		host := k.resolvedWorkerHost()
		model := k.resolvedWorkerModel()
		names, reason := fetch(host)
		if reason != "" {
			return reason
		}
		if !hasModel(names, model) {
			return fmt.Sprintf("worker model %q is not pulled on %s (ollama pull %s)", model, host, model)
		}
	}
	return ""
}

func ollamaTags(ctx context.Context, host string) ([]string, error) {
	url := strings.TrimRight(host, "/") + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// hasModel matches the way Ollama names tags: an untagged request means
// :latest, and comparison is case-insensitive.
func hasModel(names []string, want string) bool {
	want = normalizeModel(want)
	if want == "" {
		return false
	}
	for _, n := range names {
		if normalizeModel(n) == want {
			return true
		}
	}
	return false
}

func normalizeModel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ":latest")
}
