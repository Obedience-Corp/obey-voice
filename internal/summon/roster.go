package summon

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Identity is one configured persona in a routing roster. DisplayName also
// supplies its first word as a wake name for multi-word names, matching
// NewMatcher's existing R2 behavior.
type Identity struct {
	ID          string
	DisplayName string
	Aliases     []string
}

// Address is the result of classifying one finalized transcript. A zero
// PersonaID means that the input did not select a persona; in that case
// Remainder is always the original transcript, including its case and spaces.
type Address struct {
	PersonaID string
	Remainder string
	Reason    string
	Bare      bool
}

const (
	ReasonNoAddress      = "no-address"
	ReasonBareName       = "bare-name"
	ReasonVocative       = "vocative"
	ReasonImperative     = "punctuationless-imperative"
	ReasonExplicitSwitch = "explicit-switch"
	ReasonUnknownTarget  = "unknown-target"
)

// UnknownTargetError reports an explicit switch request for a persona that is
// not in the roster. It is intentionally distinct from an ordinary no-address
// result so callers can explain a requested switch without changing selection.
type UnknownTargetError struct {
	Target string
}

func (e *UnknownTargetError) Error() string {
	return fmt.Sprintf("unknown persona %q", e.Target)
}

type rosterName struct {
	value       string
	personaID   string
	orderLength int
}

// Roster resolves direct addresses against all configured personas.
// Roster values are immutable after construction and safe for concurrent
// Resolve calls.
type Roster struct {
	names  []rosterName
	byName map[string]rosterName
}

// NewRoster validates and builds a deterministic address table. A normalized
// name may belong to only one identity. This includes generated first-word
// names, so a collision is rejected rather than resolved by input order.
func NewRoster(identities []Identity) (*Roster, error) {
	roster := &Roster{byName: make(map[string]rosterName)}
	ids := make(map[string]struct{}, len(identities))
	for i, identity := range identities {
		id := strings.TrimSpace(identity.ID)
		if id == "" {
			return nil, fmt.Errorf("identity %d has empty ID", i)
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("duplicate identity ID %q", id)
		}
		ids[id] = struct{}{}

		display := normalizeName(identity.DisplayName)
		if display == "" {
			return nil, fmt.Errorf("identity %q has empty display name", id)
		}
		candidates := []string{display}
		if words := strings.Fields(display); len(words) > 1 {
			candidates = append(candidates, words[0])
		}
		candidates = append(candidates, identity.Aliases...)
		for _, candidate := range candidates {
			name := normalizeName(candidate)
			if name == "" {
				continue
			}
			entry := rosterName{value: name, personaID: id, orderLength: len(name)}
			if prior, exists := roster.byName[name]; exists {
				if prior.personaID != id {
					return nil, fmt.Errorf("name %q is used by personas %q and %q", name, prior.personaID, id)
				}
				continue
			}
			roster.byName[name] = entry
			roster.names = append(roster.names, entry)
		}
	}
	slices.SortFunc(roster.names, func(a, b rosterName) int {
		return cmp.Or(cmp.Compare(b.orderLength, a.orderLength), cmp.Compare(a.value, b.value))
	})
	return roster, nil
}

// Resolve classifies one finalized transcript. Names at the opening of the
// transcript can select a persona; incidental and declarative mentions remain
// normal input. A direct explicit switch to an unknown target returns an error
// with no selected PersonaID and the original transcript as Remainder.
func (r *Roster) Resolve(text string) (Address, error) {
	noAddress := Address{Remainder: text, Reason: ReasonNoAddress}
	if r == nil {
		return noAddress, nil
	}

	lead := trimLead(text)
	if lead == "" {
		return noAddress, nil
	}
	if strings.HasPrefix(strings.ToLower(lead), "switch") {
		if address, recognized, err := r.resolveSwitch(text, lead); recognized {
			return address, err
		}
	}

	addressText := lead
	if fillerRest, ok := cutFillerOriginal(lead); ok {
		addressText = fillerRest
	}
	entry, remainder, ok := r.matchOpening(addressText)
	if !ok {
		return noAddress, nil
	}

	if remainder == "" || trimLead(remainder) == "" {
		return Address{PersonaID: entry.personaID, Reason: ReasonBareName, Bare: true}, nil
	}

	punctuated := startsWithBoundary(remainder)
	cleanRemainder := trimLead(remainder)
	if cleanRemainder == "" {
		return Address{PersonaID: entry.personaID, Reason: ReasonBareName, Bare: true}, nil
	}
	if !punctuated && !requestLead(cleanRemainder) {
		return noAddress, nil
	}
	reason := ReasonImperative
	if punctuated {
		reason = ReasonVocative
	}
	return Address{PersonaID: entry.personaID, Remainder: cleanRemainder, Reason: reason}, nil
}

// resolveSwitch handles only the explicit, anchored "switch to …" grammar.
// It returns recognized=false for ordinary text beginning with another use of
// the word switch, leaving that text eligible for normal routing.
func (r *Roster) resolveSwitch(original, lead string) (Address, bool, error) {
	rest, ok := cutLiteral(lead, "switch to")
	if !ok {
		return Address{}, false, nil
	}
	targetText := strings.TrimSpace(rest)
	if targetText == "" {
		return Address{}, false, nil
	}
	entry, targetRemainder, ok := r.matchOpening(targetText)
	if ok && strings.TrimSpace(trimLead(targetRemainder)) == "" {
		return Address{PersonaID: entry.personaID, Reason: ReasonExplicitSwitch, Bare: true}, true, nil
	}
	// Explicit switch targets end at a separator. This lets "switch to
	// Unknown, please" report Unknown while keeping ordinary unknown first
	// words out of the error path.
	target := switchTarget(targetText)
	if target == "" {
		return Address{}, false, nil
	}
	return Address{Remainder: original, Reason: ReasonUnknownTarget}, true, &UnknownTargetError{Target: target}
}

func (r *Roster) matchOpening(text string) (rosterName, string, bool) {
	for _, entry := range r.names {
		consumed, ok := matchPrefix(text, entry.value)
		if !ok {
			continue
		}
		return entry, text[consumed:], true
	}
	return rosterName{}, "", false
}

func cutFillerOriginal(text string) (string, bool) {
	for _, filler := range Fillers {
		consumed, ok := matchPrefix(text, normalizeName(filler))
		if !ok {
			continue
		}
		return trimLead(text[consumed:]), true
	}
	return "", false
}

func matchPrefix(text, name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	words := strings.Fields(name)
	position := 0
	for wordIndex, word := range words {
		if wordIndex > 0 {
			start := position
			for position < len(text) {
				r, size := utf8.DecodeRuneInString(text[position:])
				if !unicode.IsSpace(r) {
					break
				}
				position += size
			}
			if start == position {
				return 0, false
			}
		}
		start := position
		for range word {
			_, size := utf8.DecodeRuneInString(text[position:])
			if size == 0 {
				return 0, false
			}
			position += size
		}
		if !strings.EqualFold(text[start:position], word) {
			return 0, false
		}
	}
	rest := text[position:]
	if rest != "" && !closesName(rest) {
		return 0, false
	}
	return position, true
}

func cutLiteral(text, literal string) (string, bool) {
	consumed, ok := matchPrefix(text, literal)
	if !ok {
		return "", false
	}
	return text[consumed:], true
}

func startsWithBoundary(text string) bool {
	if text == "" {
		return false
	}
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	if text == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text)
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// requestLead accepts a conservative set of request openings when STT omitted
// punctuation. Unknown name-leading clauses stay ordinary conversation.
func requestLead(text string) bool {
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return false
	}
	switch strings.ToLower(firstWord(text)) {
	case "can", "could", "would", "will", "do", "did", "are", "have":
		return len(words) > 1 && firstWord(words[1]) == "you"
	case "please", "tell", "help", "remember", "explain", "show", "say", "read", "write", "make", "find", "look", "search", "check", "think", "give", "stop", "start", "continue", "summarize", "compare", "what", "why", "how", "where", "when", "who":
		return true
	default:
		return false
	}
}

func firstWord(text string) string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func switchTarget(text string) string {
	text = strings.TrimSpace(text)
	for i, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			text = text[:i]
			break
		}
	}
	return strings.TrimSpace(text)
}

var _ error = (*UnknownTargetError)(nil)
