package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type providerCheck struct {
	config.Diagnostic
	Models []string `json:"models,omitempty"`
}

func setupProvider(ctx context.Context) (any, error) {
	// Explicit check uses the same persona overlay as the eventual conversation.
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	var check providerCheck
	for _, d := range config.Diagnose(cfg, config.ModelsDir(), exec.LookPath) {
		if d.Name == "brain-provider" {
			check.Diagnostic = d
			break
		}
	}
	if check.Name == "" {
		return nil, fmt.Errorf("engine did not report a brain provider")
	}
	if strings.EqualFold(cfg.BrainProvider, "ollama") {
		check = probeOllama(ctx, cfg.OllamaHost, cfg.OllamaModel)
	}
	return check, nil
}

func probeOllama(ctx context.Context, host, model string) providerCheck {
	d := providerCheck{Diagnostic: config.Diagnostic{Name: "brain-provider", Severity: config.SeverityError,
		Remediation: "Start Ollama, install the selected model with ollama pull <model>, then check again. You can change the host and model below."}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(host, "/")+"/api/tags", nil)
	if err != nil {
		d.Detail = "Invalid Ollama address"
		return d
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		d.Detail = "Cannot reach Ollama. Check that the server is running and the address is correct."
		return d
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		d.Detail = fmt.Sprintf("Ollama returned HTTP %d", res.StatusCode)
		return d
	}
	var payload struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&payload); err != nil {
		d.Detail = "The address did not return an Ollama model list"
		return d
	}
	normalize := func(s string) string {
		if !strings.Contains(s, ":") {
			return s + ":latest"
		}
		return s
	}
	for _, m := range payload.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		if name != "" {
			d.Models = append(d.Models, name)
		}
	}
	if strings.TrimSpace(model) == "" {
		d.Detail = "Choose an installed Ollama model"
		d.Remediation = "Select a model below, save your choice, then check again."
		return d
	}
	for _, m := range payload.Models {
		if normalize(m.Name) == normalize(model) || normalize(m.Model) == normalize(model) {
			d.Severity = config.SeverityOK
			d.Detail = "Ollama is reachable and the selected model is installed. A test reply will verify generation."
			d.Remediation = ""
			return d
		}
	}
	d.Detail = "The selected model is not installed on this Ollama server"
	return d
}
