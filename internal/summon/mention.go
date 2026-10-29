package summon

import (
	"strings"
	"unicode"
)

// Completion inserts a canonical @id. Resolve it without a model call or
// display-name ambiguity, and keep the message following it as the request.
func (r *Roster) resolveMention(input string) ExactControl {
	target, rest := input[1:], ""
	if i := strings.IndexFunc(target, unicode.IsSpace); i >= 0 {
		target, rest = target[:i], strings.TrimSpace(target[i:])
	}
	id, ok := r.byID[strings.ToLower(target)]
	if !ok {
		return ExactControl{Kind: ExactControlInvalid, Err: &UnknownTargetError{Target: "@" + target}}
	}
	return ExactControl{Kind: ExactControlValid, Address: Address{
		PersonaID: id, Remainder: rest, Reason: "explicit-mention", Bare: rest == "",
	}}
}
