package decision

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/admission"
)

// ErrInvalidOutput marks a provider answer that could not be decoded or that
// breaks the question's contract, as opposed to a transport failure.
var ErrInvalidOutput = errors.New("invalid decision output")

// The addressee types mirror brain.AddressRequest and brain.Addressee field for
// field; the brain package already depends on this one.
type AddresseeParticipant struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
}

type AddresseeMessage struct {
	Name    string `json:"name"`
	Account string `json:"account"`
	Text    string `json:"text"`
}

type AddresseeInput struct {
	Participants []AddresseeParticipant `json:"participants"`
	Current      string                 `json:"current_responder"`
	Speaker      string                 `json:"speaker"`
	Account      string                 `json:"account"`
	Conversation []AddresseeMessage     `json:"conversation,omitempty"`
	Text         string                 `json:"text"`
	AcousticName string                 `json:"acoustic_name,omitempty"`
}

type Addressee struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Bare bool   `json:"bare"`
}

type addresseeSpec struct {
	names    []string
	guidance string
}

const (
	addresseeTarget = "target"
	addresseeBare   = "bare"

	addresseeNone        = "none"
	addresseeAmbiguous   = "ambiguous"
	addresseeUnavailable = "unavailable"
	personPrefix         = "person_"
)

// AddresseeRequest builds the addressee question. guidance is the private
// addressee instruction text for providers that accept free-form instructions.
func AddresseeRequest(in AddresseeInput, guidance string) Request {
	criteria := map[string]string{
		addresseeNone:        "No change of listener. A mention, quotation, hypothetical, or discussion ABOUT a person rather than talking TO them.",
		addresseeAmbiguous:   "Several simultaneous listeners, or a name shared by several participants, with no unique immediate addressee.",
		addresseeUnavailable: "The immediate listener is a named person who is absent from the participants.",
	}
	for i, p := range in.Participants {
		who := p.Name
		if len(p.Aliases) > 0 {
			who += " (aliases: " + strings.Join(p.Aliases, ", ") + ")"
		}
		criteria[personKey(i)] = fmt.Sprintf("Talking directly TO %s, or asking this person to take the next turn. When asked to consult someone else, this person is the immediate listener, not that later person.", who)
	}
	return Request{
		State: in,
		Questions: map[string]Question{
			addresseeTarget: {Type: "choice", Instructions: "Who is the immediate listener of the latest message in this conversation?", Criteria: criteria},
			addresseeBare:   {Type: "noul", Instructions: "Is the user's latest message only a greeting, name, or request to switch listeners, without a substantive question or task?"},
		},
		addressee: &addresseeSpec{names: rosterNames(in.Participants), guidance: guidance},
	}
}

func personKey(i int) string { return fmt.Sprintf("%s%d", personPrefix, i) }

func rosterNames(participants []AddresseeParticipant) []string {
	names := []string{""}
	for _, p := range participants {
		for _, name := range append([]string{p.Name}, p.Aliases...) {
			name = strings.TrimSpace(name)
			if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) }) {
				names = append(names, name)
			}
		}
	}
	return names
}

func eligibleAddressee(in AddresseeInput) bool {
	if len(in.Participants) == 0 || in.Text == "" || utf8.RuneCountInString(in.Text) > 4000 || len(in.Conversation) > 8 {
		return false
	}
	for _, m := range in.Conversation {
		if utf8.RuneCountInString(m.Text) > 1000 {
			return false
		}
	}
	return true
}

// NormalizeAddressee applies the addressee contract to a decoded answer: the
// kind decides, a name must come from the roster, and anything else is invalid.
func NormalizeAddressee(result Addressee, in AddresseeInput) (Addressee, error) {
	result.Name = strings.TrimSpace(result.Name)
	switch result.Kind {
	case addresseeNone:
		result.Name, result.Bare = "", false
	case "address":
		if result.Name == "" {
			return Addressee{}, fmt.Errorf("%w: empty addressee", ErrInvalidOutput)
		}
		names := rosterNames(in.Participants)
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, result.Name) }) {
			return Addressee{Kind: addresseeNone}, nil
		}
	case addresseeAmbiguous:
		result.Bare = false
	default:
		return Addressee{}, fmt.Errorf("%w: addressee kind %q", ErrInvalidOutput, result.Kind)
	}
	if in.Account == "agent" {
		result.Bare = false
	}
	return result, nil
}

// InterpretAddressee turns a provider response into an addressee. The boolean
// is false when the model abstained (low confidence or margin); the caller then
// treats the question as unanswered rather than as "nobody".
func InterpretAddressee(in AddresseeInput, response Response) (Addressee, bool, error) {
	if response.StructuredAddressee != nil {
		result, err := NormalizeAddressee(*response.StructuredAddressee, in)
		return result, err == nil, err
	}
	target, ok := response.Answers[addresseeTarget]
	if !ok || !validProbability(target.Confidence) {
		return Addressee{}, false, fmt.Errorf("%w: missing target confidence", ErrInvalidOutput)
	}
	options := []string{addresseeNone, addresseeAmbiguous, addresseeUnavailable}
	for i := range in.Participants {
		options = append(options, personKey(i))
	}
	if !slices.Contains(options, target.Choice) {
		return Addressee{}, false, fmt.Errorf("%w: unknown target %q", ErrInvalidOutput, target.Choice)
	}
	if len(target.Probabilities) != len(options) {
		return Addressee{}, false, fmt.Errorf("%w: incomplete target distribution", ErrInvalidOutput)
	}
	sum, other := 0.0, 0.0
	for _, key := range options {
		p, ok := target.Probabilities[key]
		if !ok || !validProbability(&p) {
			return Addressee{}, false, fmt.Errorf("%w: invalid target distribution", ErrInvalidOutput)
		}
		sum += p
		if key != target.Choice {
			other = max(other, p)
		}
	}
	if math.Abs(sum-1) > .02 {
		return Addressee{}, false, fmt.Errorf("%w: target distribution does not sum to one", ErrInvalidOutput)
	}
	if *target.Confidence < .85 || target.Probabilities[target.Choice]-other < .20 {
		return Addressee{}, false, nil
	}
	var raw Addressee
	switch {
	case target.Choice == addresseeNone || target.Choice == addresseeUnavailable:
		raw.Kind = addresseeNone
	case target.Choice == addresseeAmbiguous:
		raw.Kind = addresseeAmbiguous
	default:
		index, _ := strconv.Atoi(strings.TrimPrefix(target.Choice, personPrefix))
		raw.Kind, raw.Name = "address", in.Participants[index].Name
		if in.Account == "user" {
			bare := response.Answers[addresseeBare]
			if !validProbability(bare.Noul) {
				return Addressee{}, false, fmt.Errorf("%w: missing bare probability", ErrInvalidOutput)
			}
			raw.Bare = *bare.Noul >= .90
		}
	}
	result, err := NormalizeAddressee(raw, in)
	return result, err == nil, err
}

// ResolveAddressee asks the decision model who is being addressed. It blocks
// for at most the configured budget. ok is false, with no error, when the
// point is off, ineligible, busy, abstained or the configuration changed
// meanwhile; the caller then continues with its next resolver. Errors are
// admission classifier failures, or the parent context's own error.
func (c *Controller) ResolveAddressee(ctx context.Context, in AddresseeInput, guidance string) (result Addressee, ok bool, err error) {
	if c == nil || ctx.Err() != nil || !eligibleAddressee(in) {
		return Addressee{}, false, nil
	}
	c.mu.Lock()
	g := c.current
	if g == nil || !g.options.Addressee || c.addresseeBusy {
		c.mu.Unlock()
		return Addressee{}, false, nil
	}
	c.addresseeBusy = true
	c.mu.Unlock()

	budgetCtx, cancel := context.WithTimeout(ctx, g.options.Budget)
	defer cancel()
	stop := context.AfterFunc(g.ctx, cancel)
	defer stop()

	type outcome struct {
		response Response
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() { c.mu.Lock(); c.addresseeBusy = false; c.mu.Unlock() }()
		response, err := g.options.Policy.Evaluator.Evaluate(budgetCtx, AddresseeRequest(in, guidance))
		done <- outcome{response, err}
	}()
	var got outcome
	select {
	case got = <-done:
	case <-budgetCtx.Done():
		got.err = budgetCtx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Addressee{}, false, err
	}
	if g.ctx.Err() != nil {
		return Addressee{}, false, nil
	}
	if got.err == nil {
		if result, accepted, ierr := InterpretAddressee(in, got.response); ierr != nil {
			got.err = ierr
		} else {
			return result, accepted, nil
		}
	}
	wrapped := fmt.Errorf("resolve addressee with decision model: %w", got.err)
	switch {
	case errors.Is(got.err, ErrInvalidOutput):
		return Addressee{}, false, admission.WrapClassifierFailure(admission.ClassifierInvalidOutput, wrapped)
	case errors.Is(budgetCtx.Err(), context.DeadlineExceeded):
		return Addressee{}, false, admission.WrapClassifierFailure(admission.ClassifierLocalTimeout, wrapped)
	}
	return Addressee{}, false, admission.WrapClassifierFailure(admission.ClassifierUnavailable, wrapped)
}
