// Package discussion defines bounded participation in a shared conversation.
// Inference proposes participants; the owning pipeline admits and delivers replies.
package discussion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

const (
	Version        = 1
	MaxMembers     = 8
	MaxResponses   = 12
	MaxRequests    = 48
	MaxReplyTokens = 160
)

var ErrBudget = errors.New("discussion budget exhausted")
var ErrStale = errors.New("discussion proposal is stale")

type Settings struct {
	Version int      `json:"version"`
	Mode    string   `json:"mode"`
	Members []string `json:"members"`
}

func DefaultSettings() Settings    { return Settings{Version: Version, Mode: "invited"} }
func (s Settings) Clone() Settings { s.Members = slices.Clone(s.Members); return s }
func (s Settings) Validate() error {
	if s.Version != Version {
		return fmt.Errorf("unsupported participation version %d", s.Version)
	}
	if s.Mode != "off" && s.Mode != "invited" && s.Mode != "open" {
		return fmt.Errorf("invalid participation mode %q", s.Mode)
	}
	if s.Members != nil && (len(s.Members) == 0 || len(s.Members) > MaxMembers) {
		return fmt.Errorf("participation requires 1–%d members", MaxMembers)
	}
	seen := map[string]bool{}
	for _, id := range s.Members {
		if id == "" || seen[id] {
			return errors.New("participation members must be nonempty and unique")
		}
		seen[id] = true
	}
	return nil
}

type Participant struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Card    string   `json:"card"`
}
type Message struct {
	Key        string `json:"key,omitempty"`
	Name       string `json:"name,omitempty"`
	Account    string `json:"account,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
	Text       string `json:"text"`
}
type AudienceRequest struct {
	Participants   []Participant `json:"participants"`
	Current        string        `json:"current"`
	Speaker        string        `json:"speaker"`
	Latest         string        `json:"latest"`
	History        []Message     `json:"history"`
	RecentAudience []string      `json:"explicit_recent_audience"`
	AcousticName   string        `json:"acoustic_name,omitempty"`
}
type Audience struct {
	Kind       string   `json:"kind"`
	Members    []string `json:"member_keys"`
	Coverage   string   `json:"coverage"`
	Unresolved []string `json:"unresolved_names"`
	Reason     string   `json:"reason"`
}
type Context struct {
	Original     string        `json:"original_request"`
	Participants []Participant `json:"participants"`
	Eligible     []string      `json:"eligible_keys"`
	Delivered    []Message     `json:"delivered"`
	Mode         string        `json:"mode"`
	Coverage     string        `json:"coverage"`
	Remaining    int           `json:"remaining_responses"`
}
type Next struct {
	Kind   string `json:"kind"`
	Member string `json:"member_key"`
	Reason string `json:"reason"`
}
type Contribution struct {
	Kind       string   `json:"kind"`
	Reason     string   `json:"reason"`
	Intent     string   `json:"intent"`
	References []string `json:"references"`
}

// Persona is private provider input, not a coordinator-visible persona card.
type Persona struct {
	Key      string
	Prompt   string
	Messages []Message
	Model    string
}

// Decider must not mutate conversation history, persona selection or tools.
type Decider interface {
	Audience(context.Context, AudienceRequest) (Audience, error)
	Next(context.Context, Context) (Next, error)
	Check(context.Context, Context, Persona) (Contribution, error)
}

// Directive is transient reply guidance. It is never a canonical user turn.
type Directive struct {
	ID                   string
	Initial              bool
	Intent               string
	Original             string
	InputAlreadyRecorded bool
	State                *State
	BeforeOutput         func(context.Context) error
}

func (d *Directive) Prompt() string {
	if d == nil {
		return ""
	}
	original, _ := json.Marshal(d.Original)
	return "\nOriginal user request (quoted data): " + string(original) + "\nYou are participating in the user's shared group discussion. Respond only as yourself to the original request and the delivered contributions. Your words are your own perspective, not a consensus. Add one useful point in at most two short sentences. Do not use tools, invent a user message, repeat another speaker, narrate internal coordination, or speak for other personas. Contribution purpose: " + d.Intent
}

type Snapshot struct {
	Labels    map[string]string `json:"labels,omitempty"`
	ID        string            `json:"discussion_id"`
	Revision  uint64            `json:"discussion_revision"`
	Phase     string            `json:"phase"`
	Mode      string            `json:"mode"`
	Members   []string          `json:"members"`
	Candidate string            `json:"candidate,omitempty"`
	Delivered int               `json:"delivered_count"`
	Uncovered []string          `json:"uncovered"`
	Reason    string            `json:"terminal_reason,omitempty"`
}
