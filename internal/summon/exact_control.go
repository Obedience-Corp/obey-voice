package summon

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ExactControlKind is the exact-control classifier outcome.
type ExactControlKind uint8

const (
	ExactControlNone ExactControlKind = iota
	ExactControlValid
	ExactControlInvalid
)

// ExactControl reports whether text is an exact foreground identity control.
// Valid and Invalid are both "recognized controls"; Invalid must refuse.
type ExactControl struct {
	Kind    ExactControlKind
	Address Address
	Err     error
}

// ResolveExactControl classifies whole-input identity controls only.
// It returns one of three outcomes: not a control, valid control, or an
// invalid/ambiguous control that must be refused.
func (r *Roster) ResolveExactControl(text string) ExactControl {
	if r == nil {
		return ExactControl{Kind: ExactControlNone}
	}
	core, ok := normalizeExactControlInput(text)
	if !ok || startsWithQuote(core) {
		return ExactControl{Kind: ExactControlNone}
	}

	// A literal configured public name/alias wins over grammar parsing.
	if matched := r.matchWholeIdentity(text, core, ReasonBareName); matched.Kind != ExactControlNone {
		return matched
	}

	if target, ok := cutExactGreeting(core); ok && !startsWithQuote(target) {
		if matched := r.matchWholeIdentity(text, target, ReasonBareName); matched.Kind != ExactControlNone {
			return matched
		}
	}

	switchTarget, switched := cutExactSwitch(core)
	if !switched || switchTarget == "" || startsWithQuote(switchTarget) {
		return ExactControl{Kind: ExactControlNone}
	}
	if entry, remainder, ok := r.matchOpening(switchTarget); ok {
		if strings.TrimSpace(remainder) != "" {
			return ExactControl{Kind: ExactControlNone}
		}
		address, err := r.address(entry, text, "", ReasonExplicitSwitch, true)
		if err != nil {
			return ExactControl{
				Kind:    ExactControlInvalid,
				Address: Address{Remainder: text, Reason: ReasonAmbiguousTarget},
				Err:     err,
			}
		}
		return ExactControl{Kind: ExactControlValid, Address: address}
	}
	// A tail after an unknown target is not a strict whole-input switch
	// control; leave it to semantic routing instead of deterministic refusal.
	if strings.ContainsFunc(switchTarget, unicode.IsSpace) {
		return ExactControl{Kind: ExactControlNone}
	}
	if !looksLikeSwitchTarget(switchTarget) {
		return ExactControl{Kind: ExactControlNone}
	}
	return ExactControl{
		Kind:    ExactControlInvalid,
		Address: Address{Remainder: text, Reason: ReasonUnknownTarget},
		Err:     &UnknownTargetError{Target: strings.TrimSpace(switchTarget)},
	}
}

func (r *Roster) matchWholeIdentity(original, normalized string, reason string) ExactControl {
	key := normalizeName(normalized)
	if key == "" {
		return ExactControl{Kind: ExactControlNone}
	}
	entry, ok := r.byName[key]
	if !ok {
		return ExactControl{Kind: ExactControlNone}
	}
	address, err := r.address(entry, original, "", reason, true)
	if err != nil {
		return ExactControl{
			Kind:    ExactControlInvalid,
			Address: Address{Remainder: original, Reason: ReasonAmbiguousTarget},
			Err:     err,
		}
	}
	return ExactControl{Kind: ExactControlValid, Address: address}
}

func normalizeExactControlInput(text string) (string, bool) {
	core := strings.TrimSpace(text)
	if core == "" {
		return "", false
	}
	end := len(core)
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(core[:end])
		if unicode.IsSpace(r) || isTerminalControlPunctuation(r) {
			end -= size
			continue
		}
		break
	}
	core = strings.TrimSpace(core[:end])
	return core, core != ""
}

func startsWithQuote(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text)
	return isQuoteRune(r)
}

func isQuoteRune(r rune) bool {
	switch r {
	case '"', '\'', '`', '\u2018', '\u2019', '\u201c', '\u201d', '\u00ab', '\u00bb', '\u300c', '\u300d', '\uff02':
		return true
	default:
		return false
	}
}

func isTerminalControlPunctuation(r rune) bool {
	switch r {
	case '.', ',', '!', '?', ':', ';', '\u3002', '\uff0c', '\uff01', '\uff1f', '\uff1a', '\uff1b', '\u2026':
		return true
	default:
		return false
	}
}

func cutExactGreeting(text string) (string, bool) {
	for _, filler := range Fillers {
		consumed, ok := matchPrefix(text, normalizeName(filler))
		if !ok || consumed >= len(text) {
			continue
		}
		rest := strings.TrimLeftFunc(text[consumed:], func(r rune) bool {
			return unicode.IsSpace(r) || r == ',' || r == ':' || r == ';' || r == '-' || r == '\u2013' || r == '\u2014'
		})
		if rest == "" {
			return "", false
		}
		return rest, true
	}
	return "", false
}

func cutExactSwitch(text string) (string, bool) {
	rest, ok := cutLiteral(text, "switch to")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func looksLikeSwitchTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, r := range target {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || r == '-' || r == '\'' || r == '’' || r == 'ʼ' {
			continue
		}
		return false
	}
	return true
}
