// Package decision contains optional, bounded decision policies. Providers do
// not select personas, execute tools, or grant worker authority.
package decision

import (
	"context"
	"encoding/json"
)

type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Answer struct {
	Choice        string             `json:"choice,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
}

type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	// Raw is for explicit local replay capture. Product diagnostics should log
	// only the decision point, model, disposition and elapsed time.
	Raw json.RawMessage `json:"-"`
	// Structured is set only by the OpenAI adapter after strict validation.
	// It is a categorical model answer, not a calibrated probability.
	Structured *StructuredRoute `json:"-"`
}

type StructuredRoute struct {
	Action string `json:"action"`
	Tier   Tier   `json:"tier"`
}

type Evaluator interface {
	Evaluate(context.Context, Request) (Response, error)
}
