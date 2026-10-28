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
	literal, ok := normalizeExactControlInput(text)
	if !ok || startsWithQuote(literal) {
		return ExactControl{Kind: ExactControlNone}
	}
	if strings.HasPrefix(literal, "@") {
		return r.resolveMention(literal)
	}

	// Try the configured spelling first, then peel one terminal mark at a time.
	// Santa!! must still see the alias Santa! before it falls through to Santa.
	deferredUnknown := ExactControl{Kind: ExactControlNone}
	candidate := literal
	for {
		if matched := r.matchWholeIdentity(text, candidate, ReasonBareName); matched.Kind != ExactControlNone {
			return matched
		}
		if matched := r.matchExactGreetingOrSwitch(text, candidate); matched.Kind != ExactControlNone {
			if isUnknownExactControl(matched) {
				deferredUnknown = matched
			} else {
				return matched
			}
		}
		next, peeled := stripOneExactControlTerminalMark(candidate)
		if !peeled {
			break
		}
		candidate = next
	}
	if deferredUnknown.Kind != ExactControlNone {
		return deferredUnknown
	}
	return ExactControl{Kind: ExactControlNone}
}

func (r *Roster) matchExactGreetingOrSwitch(original, core string) ExactControl {
	if core == "" {
		return ExactControl{Kind: ExactControlNone}
	}
	if target, ok := cutExactGreeting(core); ok && !startsWithQuote(target) {
		if matched := r.matchWholeIdentity(original, target, ReasonBareName); matched.Kind != ExactControlNone {
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
		address, err := r.address(entry, original, "", ReasonExplicitSwitch, true)
		if err != nil {
			return ExactControl{
				Kind:    ExactControlInvalid,
				Address: Address{Remainder: original, Reason: ReasonAmbiguousTarget},
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
		Address: Address{Remainder: original, Reason: ReasonUnknownTarget},
		Err:     &UnknownTargetError{Target: strings.TrimSpace(switchTarget)},
	}
}

func isUnknownExactControl(matched ExactControl) bool {
	if matched.Kind != ExactControlInvalid || matched.Err == nil {
		return false
	}
	_, ok := matched.Err.(*UnknownTargetError)
	return ok
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
	return core, core != ""
}

// stripOneExactControlTerminalMark removes a single trailing punctuation mark.
// Spaces around that mark are trimmed. The caller retries identity matching
// so a shorter form is tried only after the longer configured spelling misses.
func stripOneExactControlTerminalMark(text string) (string, bool) {
	trimmed := strings.TrimRightFunc(text, unicode.IsSpace)
	if trimmed == "" {
		return text, false
	}
	r, size := utf8.DecodeLastRuneInString(trimmed)
	if !isTerminalControlPunctuation(r) {
		return text, false
	}
	next := strings.TrimSpace(trimmed[:len(trimmed)-size])
	if next == "" || next == text {
		return text, false
	}
	return next, true
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
