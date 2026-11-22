package summon

import (
	"slices"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// ResolveAgentMention resolves explicit controls anywhere in a delivered agent
// response, including after a preamble. Unlike semantic addresses, @controls
// can use canonical IDs as well as unambiguous public names and aliases.
// Several personas mentioned in one response are an ordered invitation: the
// first is the address and the rest are Invited, in mention order.
func (r *Roster) ResolveAgentMention(text string) ExactControl {
	var ids []string
	for _, mention := range textclean.AgentMentions(text) {
		id, ok := r.byID[strings.ToLower(mention.Target)]
		if !ok {
			address, err := r.Lookup(mention.Target)
			if err != nil {
				return ExactControl{Kind: ExactControlInvalid, Err: err}
			}
			id = address.PersonaID
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ExactControl{Kind: ExactControlNone}
	}
	address := Address{PersonaID: ids[0], Remainder: text, Reason: "explicit-mention"}
	if len(ids) > 1 {
		address.Invited = ids[1:]
	}
	return ExactControl{Kind: ExactControlValid, Address: address}
}

// Completion inserts a canonical @id. Resolve it without a model call or
// display-name ambiguity, and keep the message following it as the request.
func (r *Roster) resolveMention(input string) ExactControl {
	target, rest := input[1:], ""
	if i := strings.IndexFunc(target, unicode.IsSpace); i >= 0 {
		target, rest = target[:i], strings.TrimSpace(target[i:])
	}
	if strings.ContainsRune(target, '/') {
		return ExactControl{Kind: ExactControlNone}
	}
	id, ok := r.byID[strings.ToLower(target)]
	if !ok {
		return ExactControl{Kind: ExactControlInvalid, Err: &UnknownTargetError{Target: "@" + target}}
	}
	return ExactControl{Kind: ExactControlValid, Address: Address{
		PersonaID: id, Remainder: rest, Reason: "explicit-mention", Bare: rest == "",
	}}
}

// leadingPathMention reports whether text opens with an @path such as
// @projects/app/ or @./README.md. A persona id never contains "/", so that
// token names a file or directory the message is about, never an address.
func leadingPathMention(text string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "@") {
		return false
	}
	token := text[1:]
	if i := strings.IndexFunc(token, unicode.IsSpace); i >= 0 {
		token = token[:i]
	}
	return strings.ContainsRune(token, '/')
}
