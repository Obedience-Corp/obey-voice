package textclean

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AgentMention identifies a standalone @name candidate in response text. Offsets
// are bytes in the original text, so renderers can style it without rewriting it.
type AgentMention struct {
	Start, End int
	Target     string
}

var agentMentionRE = regexp.MustCompile(`@[\p{L}\p{N}_][\p{L}\p{N}_-]*`)

// AgentMentions finds @id and single-token @display-name candidates. The caller
// resolves them against its roster. Email addresses, URL paths, and directly
// quoted/code-wrapped tokens are not candidates.
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

// SpeakAgentMentions says who a response addresses. Each candidate that
// spokenName confirms, from the response's frozen roster lookup, is replaced by
// that name, so "@Milo, what do you think?" is heard as "Milo, what do you
// think?". Mentions with only space between them are heard as a list. A name
// the text already says in plain words is not said twice: that mention is
// dropped with its separator ("@Milo Hey Milo!" is heard as "Hey Milo!").
// With no lookup, and for ordinary handles the lookup does not know, the text
// is spoken as written.
func SpeakAgentMentions(text string, spokenName func(target string) (string, bool)) string {
	if spokenName == nil {
		return text
	}
	type control struct {
		AgentMention
		name string
	}
	var controls []control
	for _, mention := range AgentMentions(text) {
		if name, ok := spokenName(mention.Target); ok {
			controls = append(controls, control{mention, strings.TrimSpace(name)})
		}
	}
	if len(controls) == 0 {
		return text
	}
	var plain strings.Builder
	last := 0
	for _, c := range controls {
		plain.WriteString(text[last:c.Start])
		plain.WriteByte(' ')
		last = c.End
	}
	plain.WriteString(text[last:])
	said := spokenWords(plain.String())

	var b strings.Builder
	last = 0
	afterName := false
	for _, c := range controls {
		prefix := text[last:c.Start]
		if words := spokenWords(c.name); words != " " && !strings.Contains(said, words) {
			if afterName && strings.TrimSpace(prefix) == "" {
				prefix = ", "
			}
			b.WriteString(prefix)
			b.WriteString(c.name)
			said += words
			last = c.End
			afterName = true
			continue
		}
		// A repeat dropped from the middle of a run leaves the run a list.
		afterName = afterName && strings.TrimSpace(prefix) == ""
		if c.End < len(text) && strings.ContainsRune(".!?", rune(text[c.End])) {
			prefix = strings.TrimSuffix(strings.TrimRightFunc(prefix, unicode.IsSpace), ",")
		}
		b.WriteString(prefix)
		last = c.End
		// A separator attached to a dropped address goes with it, unless it
		// closes a list of names already being said.
		if !afterName && last < len(text) && strings.ContainsRune(",:;", rune(text[last])) {
			last++
		}
	}
	b.WriteString(text[last:])
	return repairSpacing(strings.Join(strings.Fields(b.String()), " "))
}

// spokenWords is text as space-delimited lower-case words, with a space at
// each end so one phrase can be searched for in another on word boundaries.
func spokenWords(text string) string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) })
	if len(fields) == 0 {
		return " "
	}
	return " " + strings.Join(fields, " ") + " "
}
