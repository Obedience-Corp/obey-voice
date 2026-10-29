package decision

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"unicode/utf8"
)

type Tier string

const (
	Simple  Tier = "simple"
	Medium  Tier = "medium"
	Complex Tier = "complex"
)

type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type WorkInput struct {
	Text         string    `json:"latest_user_message"`
	Conversation []Message `json:"recent_conversation"`
	EnabledTiers []Tier    `json:"enabled_worker_tiers"`
}

type WorkProposal struct {
	Accepted   bool     `json:"accepted"`
	Tier       Tier     `json:"tier,omitempty"`
	ReasonCode string   `json:"reason_code,omitempty"`
	Reason     string   `json:"reason"`
	Evidence   Response `json:"evidence"`
}

//go:embed work_questions.json
var workQuestionsJSON []byte

//go:embed route_questions.json
var routeQuestionsJSON []byte

const (
	PolicyIntentAndTier = "intent_and_tier"
	PolicyRouteChoice   = "route_choice"
)

// WorkRequest returns a fresh question map so a provider cannot mutate the
// policy's schema for later turns. The question version is frozen for replay.
func WorkRequest(in WorkInput) Request {
	return questionRequest(in, workQuestionsJSON)
}

func RouteWorkRequest(in WorkInput) Request { return questionRequest(in, routeQuestionsJSON) }

func questionRequest(in WorkInput, raw []byte) Request {
	questions := make(map[string]Question)
	if err := json.Unmarshal(raw, &questions); err != nil {
		panic("invalid embedded worker decision questions: " + err.Error())
	}
	if in.Conversation == nil {
		in.Conversation = []Message{}
	}
	return Request{State: in, Questions: questions}
}

type WorkPolicy struct {
	Evaluator Evaluator
	// Empty preserves the initial two-question experiment for replay.
	Mode string
}

// Decide proposes work only. It never calls a fallback, chooses an assignment,
// executes a task, or substitutes a different tier when one is unavailable.
func (p WorkPolicy) Decide(ctx context.Context, in WorkInput) (WorkProposal, error) {
	if err := ctx.Err(); err != nil {
		return WorkProposal{}, err
	}
	if p.Evaluator == nil {
		return WorkProposal{Reason: "off"}, nil
	}
	if !eligibleWork(in) {
		return WorkProposal{Reason: "ineligible"}, nil
	}
	request := WorkRequest(in)
	if p.Mode == PolicyRouteChoice {
		request = RouteWorkRequest(in)
	} else if p.Mode != "" && p.Mode != PolicyIntentAndTier {
		return WorkProposal{}, fmt.Errorf("unknown decision work policy")
	}
	response, err := p.Evaluator.Evaluate(ctx, request)
	if err != nil {
		return WorkProposal{}, err
	}
	if err := ctx.Err(); err != nil {
		return WorkProposal{}, err
	}
	if p.Mode == PolicyRouteChoice && response.Structured == nil {
		return InterpretRouteWork(response, in.EnabledTiers)
	}
	return InterpretWork(response, in.EnabledTiers)
}

// InterpretRouteWork uses the frozen route-choice-v2 policy. Confidence is the
// provider's TypeSafe score, not a claim of observed routing accuracy.
func InterpretRouteWork(response Response, enabled []Tier) (WorkProposal, error) {
	route := response.Answers["route"]
	if !validProbability(route.Confidence) {
		return WorkProposal{}, fmt.Errorf("missing route confidence")
	}
	// Reuse the validated four-way distribution and margin checks without
	// fabricating model evidence in the returned proposal.
	one := 1.0
	checked := Response{Answers: map[string]Answer{"work": {Noul: &one}, "tier": route}}
	result, err := InterpretWork(checked, enabled)
	result.Evidence = response
	if err == nil && *route.Confidence < .90 {
		result.Accepted, result.Reason = false, "abstain"
	}
	return result, err
}

func eligibleWork(in WorkInput) bool {
	if len(in.EnabledTiers) == 0 || in.Text == "" || utf8.RuneCountInString(in.Text) > 4000 || len(in.Conversation) > 8 {
		return false
	}
	for _, message := range in.Conversation {
		if utf8.RuneCountInString(message.Text) > 1000 {
			return false
		}
	}
	return true
}

func validProbability(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 1
}

func InterpretWork(response Response, enabled []Tier) (WorkProposal, error) {
	result := WorkProposal{Reason: "abstain", Evidence: response}
	if response.Structured != nil {
		route := response.Structured
		if route.Action != "work" {
			return result, nil
		}
		if route.Tier != Simple && route.Tier != Medium && route.Tier != Complex {
			return result, fmt.Errorf("invalid structured work tier")
		}
		return acceptTier(result, route.Tier, enabled), nil
	}
	intent, tier := response.Answers["work"], response.Answers["tier"]
	if !validProbability(intent.Noul) || !validProbability(tier.Confidence) {
		return result, fmt.Errorf("missing or invalid work decision probabilities")
	}
	options := []string{"none", "simple", "medium", "complex"}
	if !slices.Contains(options, tier.Choice) || len(tier.Probabilities) != len(options) {
		return result, fmt.Errorf("invalid work tier options")
	}
	sum, other := 0.0, 0.0
	for _, key := range options {
		p, ok := tier.Probabilities[key]
		if !ok || !validProbability(&p) {
			return result, fmt.Errorf("invalid work tier distribution")
		}
		sum += p
		if key != tier.Choice {
			other = max(other, p)
		}
	}
	if math.Abs(sum-1) > .02 {
		return result, fmt.Errorf("work tier distribution does not sum to one")
	}
	if *intent.Noul < .90 || *tier.Confidence < .85 || tier.Choice == "none" || tier.Probabilities[tier.Choice]-other < .20 {
		return result, nil
	}
	return acceptTier(result, Tier(tier.Choice), enabled), nil
}

func acceptTier(result WorkProposal, tier Tier, enabled []Tier) WorkProposal {
	result.Tier = tier
	if !slices.Contains(enabled, result.Tier) {
		result.Reason = "unavailable_tier"
		return result
	}
	result.Accepted, result.Reason = true, "accepted"
	result.ReasonCode = map[Tier]string{Simple: "bounded_action", Medium: "dependent_steps", Complex: "broad_scope"}[result.Tier]
	return result
}
