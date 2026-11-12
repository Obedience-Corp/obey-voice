// Package decision contains optional, bounded decision policies. Providers
// answer fixed questions about one turn (worker routing, who is addressed);
// the host validates every answer and acts on it. Providers never execute
// tools, grant worker authority, or choose anything outside the host's roster.
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

	// addressee is set only by AddresseeRequest and never leaves the process.
	addressee *addresseeSpec
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
	// StructuredAddressee is set only by the OpenAI adapter for the addressee question.
	StructuredAddressee *Addressee `json:"-"`
}

type StructuredRoute struct {
	Action string `json:"action"`
	Tier   Tier   `json:"tier"`
}

type Evaluator interface {
	Evaluate(context.Context, Request) (Response, error)
}
