package summon

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// ResolveAgentMention resolves explicit controls anywhere in a delivered agent
// response, including after a preamble. Unlike semantic addresses, @controls
// can use canonical IDs as well as unambiguous public names and aliases.
func (r *Roster) ResolveAgentMention(text string) ExactControl {
	result := ExactControl{Kind: ExactControlNone}
	for _, mention := range textclean.AgentMentions(text) {
		id, ok := r.byID[strings.ToLower(mention.Target)]
		if !ok {
			address, err := r.Lookup(mention.Target)
			if err != nil {
				return ExactControl{Kind: ExactControlInvalid, Err: err}
			}
			id = address.PersonaID
		}
		if result.Kind == ExactControlValid && result.Address.PersonaID != id {
			return ExactControl{Kind: ExactControlInvalid, Err: fmt.Errorf("agent handoff addresses multiple personas; use one @mention")}
		}
		result = ExactControl{Kind: ExactControlValid, Address: Address{PersonaID: id, Remainder: text, Reason: "explicit-mention"}}
	}
	return result
}

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
