package ollama

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// A model can fall into saying one sentence over and over ("Let me make you
// a picture." until its output cap). Five copies in a row of a sentence this
// long is a loop; a song can sing a line three or four times, and shorter
// sentences ("No. No. No.") repeat in ordinary speech.
const (
	loopCopies   = 5
	loopMinRunes = 16
)

// lineLoopGuard stops a reply that repeats its last sentence back to back.
// Text streams as it arrives, except a sentence that so far matches the one
// before it: that is held until it diverges, which releases it unchanged, or
// becomes the fifth copy, which stops the reply with only the first copy
// sent. Code is never held or counted: repeated lines are ordinary there.
type lineLoopGuard struct {
	last   string // the previous whole sentence, whitespace-normalized
	copies int    // copies of last so far, the sent one included
	held   strings.Builder

	// The sentence being read: as written, and whitespace-normalized.
	unit, norm strings.Builder
	sent       int  // bytes of unit already sent
	space      bool // a space is owed before the next normalized byte
	ended      bool // the last non-space byte ends a sentence
	matching   bool // norm is still a prefix of last, so its words are held
	code       bool // the sentence has a backtick
	fence      bool // inside a ``` block

	stopped bool
}

func (g *lineLoopGuard) feed(text string) (string, error) {
	if g.stopped {
		return "", ErrResponseRepetition
	}
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		b := text[i]
		blank := b == ' ' || b == '\t' || b == '\n' || b == '\r'
		if g.norm.Len() > 0 && (b == '\n' || (g.ended && blank)) && g.complete(&out) {
			return out.String(), ErrResponseRepetition
		}
		g.add(b, blank, &out)
	}
	return out.String(), nil
}

// add reads one byte of the current sentence and sends it unless it is held.
func (g *lineLoopGuard) add(b byte, blank bool, out *strings.Builder) {
	if g.unit.Len() == 0 {
		g.matching = g.last != "" && !g.fence
	}
	g.unit.WriteByte(b)
	if blank {
		g.space = g.norm.Len() > 0
	} else {
		if g.space {
			g.normalize(' ')
			g.space = false
		}
		g.normalize(b)
		g.ended = b == '.' || b == '!' || b == '?'
		if b == '`' {
			g.code, g.matching = true, false
		}
	}
	// White space before a sentence goes out at once, unless it follows a
	// held copy: then it stays in order behind that copy.
	if !g.matching || g.norm.Len() == 0 && g.held.Len() == 0 {
		g.send(out)
	}
}

// normalize appends c to norm and notes whether last still starts with it.
func (g *lineLoopGuard) normalize(c byte) {
	p := g.norm.Len()
	g.matching = g.matching && p < len(g.last) && g.last[p] == c
	g.norm.WriteByte(c)
}

// send writes what is held and what of the current sentence is unsent.
func (g *lineLoopGuard) send(out *strings.Builder) {
	out.WriteString(g.held.String())
	g.held.Reset()
	out.WriteString(g.unit.String()[g.sent:])
	g.sent = g.unit.Len()
}

// complete ends the current sentence and reports whether it made a loop.
func (g *lineLoopGuard) complete(out *strings.Builder) bool {
	norm := g.norm.String()
	if strings.HasPrefix(norm, "```") {
		g.fence = !g.fence
	}
	if g.matching && norm == g.last && countable(norm) {
		g.copies++
		if g.copies >= loopCopies {
			g.stopped = true
			g.held.Reset()
			return true
		}
		g.held.WriteString(g.unit.String()[g.sent:])
	} else {
		g.send(out)
		g.last, g.copies = norm, 1
		if g.fence || g.code {
			g.last = ""
		}
	}
	g.unit.Reset()
	g.norm.Reset()
	g.sent, g.space, g.ended, g.code = 0, false, false, false
	return false
}

// flush sends whatever is held at the end of the reply: fewer than five
// copies are left as the model wrote them.
func (g *lineLoopGuard) flush() string {
	if g.stopped {
		return ""
	}
	var out strings.Builder
	g.send(&out)
	g.unit.Reset()
	g.norm.Reset()
	g.sent = 0
	return out.String()
}

func countable(sentence string) bool {
	return utf8.RuneCountInString(sentence) >= loopMinRunes && strings.IndexFunc(sentence, unicode.IsLetter) >= 0
}
