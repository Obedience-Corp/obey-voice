package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

// Embeddings retrieve candidates; only a task-specific confirmation may turn
// their bodies into instructions. This private call never changes history.
func (o *OllamaBrain) confirmSkillMatches(ctx context.Context, input string, candidates []skills.Skill) ([]skills.Skill, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if budget := workerBudgetFromContext(ctx); budget != nil {
		snapshot := budget.Snapshot()
		if snapshot.Limits.MaxRequests > 0 && snapshot.Limits.MaxRequests-snapshot.Requests < 2 {
			return nil, nil
		}
		if snapshot.Limits.MaxOutputTokens > 0 && budget.RemainingOutput() <= 128 {
			return nil, nil
		}
	}
	if o.client == nil {
		return nil, errors.New("skill selection client unavailable")
	}
	if len(input) > 8192 {
		return nil, errors.New("skill selection input exceeds 8 KiB")
	}
	names := make([]string, len(candidates))
	type candidate struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	descriptions := make([]candidate, len(candidates))
	for i, skill := range candidates {
		names[i] = skill.Name
		descriptions[i] = candidate{skill.Name, skill.Description}
	}
	payload, err := json.Marshal(struct {
		Request    string      `json:"request"`
		Candidates []candidate `json:"candidates"`
	}{input, descriptions})
	if err != nil {
		return nil, err
	}
	schema, err := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"relevant": map[string]any{"type": "boolean"}, "names": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": names}, "maxItems": len(names), "uniqueItems": true}}, "required": []string{"relevant", "names"}, "additionalProperties": false})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, config.PersonaRoutingTimeoutOf(o.cfg))
	defer cancel()
	ctxSize := 4096
	if o.cfg != nil && o.cfg.OllamaNumCtx > 0 {
		ctxSize = min(ctxSize, o.cfg.OllamaNumCtx)
	}
	stream := false
	req := &api.ChatRequest{Model: o.model, KeepAlive: o.keepAlive, Messages: []api.Message{{Role: "system", Content: systemPrompt(o.cfg, "skill-selection", nil)}, {Role: "user", Content: string(payload)}}, Stream: &stream, Format: schema, Think: &api.ThinkValue{Value: false}, Options: map[string]any{"temperature": 0, "seed": 42, "num_predict": 128, "num_ctx": ctxSize}}
	estimatedTokens := (len(req.Messages[0].Content)+len(payload)+len(schema)+3)/4 + 128
	if estimatedTokens > ctxSize*9/10 {
		return nil, errors.New("skill selection exceeds safe context budget")
	}
	var raw strings.Builder
	err = o.chat(ctx, req, func(response api.ChatResponse) error {
		if len(response.Message.ToolCalls) > 0 || response.DoneReason == "length" {
			return errors.New("invalid private skill selection response")
		}
		if raw.Len()+len(response.Message.Content) > 4096 {
			return errors.New("skill selection output exceeds 4 KiB")
		}
		raw.WriteString(response.Message.Content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("confirm skill relevance: %w", err)
	}
	var result struct {
		Relevant *bool     `json:"relevant"`
		Names    *[]string `json:"names"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode skill selection: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF || result.Names == nil || result.Relevant == nil {
		return nil, errors.New("invalid skill selection object")
	}
	if !*result.Relevant {
		return nil, nil
	}
	if len(*result.Names) > len(candidates) {
		return nil, errors.New("too many selected skills")
	}
	selected := make(map[string]bool, len(*result.Names))
	for _, name := range *result.Names {
		if selected[name] {
			return nil, errors.New("duplicate selected skill")
		}
		found := false
		for _, candidate := range candidates {
			if candidate.Name == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown selected skill %q", name)
		}
		selected[name] = true
	}
	var matched []skills.Skill
	for _, candidate := range candidates {
		if selected[candidate.Name] {
			matched = append(matched, candidate)
		}
	}
	return matched, nil
}
