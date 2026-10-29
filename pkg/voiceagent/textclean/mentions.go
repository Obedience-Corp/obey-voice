package textclean

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AgentMention identifies a standalone @name control in response text. Offsets
// are bytes in the original text, so renderers can style it without rewriting it.
type AgentMention struct {
	Start, End int
	Target     string
}

var agentMentionRE = regexp.MustCompile(`@[\p{L}\p{N}_][\p{L}\p{N}_-]*`)

// AgentMentions recognizes @id and single-token @display-name controls. Email
// addresses, URL paths, and directly quoted/code-wrapped tokens are not controls.
func AgentMentions(text string) []AgentMention {
	var mentions []AgentMention
	for _, loc := range agentMentionRE.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		if start > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:start])
			if !unicode.IsSpace(previous) {
				continue
			}
		}
		if end < len(text) {
			next, _ := utf8.DecodeRuneInString(text[end:])
			if !unicode.IsSpace(next) && !strings.ContainsRune(",:;.!?", next) {
				continue
			}
			// A domain/handle suffix is not part of the agent-control syntax.
			if next == '.' && end+1 < len(text) {
				after, _ := utf8.DecodeRuneInString(text[end+1:])
				if unicode.IsLetter(after) || unicode.IsDigit(after) {
					continue
				}
			}
		}
		mentions = append(mentions, AgentMention{Start: start, End: end, Target: text[start+1 : end]})
	}
	return mentions
}

// StripAgentMentions removes routing controls only from the speech projection.
// Plain names in the message ("Hey Venessa!") remain spoken.
func StripAgentMentions(text string) string {
	mentions := AgentMentions(text)
	if len(mentions) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, mention := range mentions {
		prefix := text[last:mention.Start]
		if mention.End < len(text) && strings.ContainsRune(".!?", rune(text[mention.End])) {
			prefix = strings.TrimSuffix(strings.TrimRightFunc(prefix, unicode.IsSpace), ",")
		}
		b.WriteString(prefix)
		last = mention.End
		// A separator attached to the address belongs to the silent control.
		if last < len(text) && strings.ContainsRune(",:;", rune(text[last])) {
			last++
		}
	}
	b.WriteString(text[last:])
	return repairSpacing(strings.Join(strings.Fields(b.String()), " "))
}
