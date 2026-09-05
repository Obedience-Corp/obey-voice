package correct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Transformer sends a system prompt and a user text to a model and returns
// its reply. brain.BatchProvider satisfies it through a thin adapter.
type Transformer interface {
	Transform(ctx context.Context, system, user string) (string, error)
}

// Mode selects what the model is asked to do.
type Mode string

const (
	// ModePatch asks for substitutions restricted to offered candidates and
	// applies only those. It is the talk-loop mode: a handful of output
	// tokens, and no call at all when there is nothing to ask.
	ModePatch Mode = "patch"
	// ModeRewrite asks for a full cleanup, line by line, under fidelity guards.
	ModeRewrite Mode = "rewrite"
)

// ParseMode returns the Mode for a config value.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModePatch, "":
		return ModePatch, nil
	case ModeRewrite:
		return ModeRewrite, nil
	}
	return "", fmt.Errorf("unknown post-processing mode %q (patch or rewrite)", s)
}

// ModelStats counts what the stage did; every field is monotonic.
type ModelStats struct {
	Calls        int64 // model calls made
	Skipped      int64 // patch turns with no candidates, so no call
	Timeouts     int64 // calls that hit the deadline (input kept)
	Errors       int64 // transport or parse failures (input kept)
	Applied      int64 // patch substitutions applied
	Rejected     int64 // patch substitutions refused by the guard
	ChangedLines int64 // rewrite lines the model changed and the guard accepted
	KeptRaw      int64 // rewrite lines (or whole windows) kept raw by a guard
}

// Model is the optional post-processing stage. It fails open: whatever goes
// wrong, the input text comes back and a counter says why.
type Model struct {
	T       Transformer
	Mode    Mode
	Prompt  func() string // the current rendered system prompt
	Timeout time.Duration
	// Candidates supplies the near-miss spans for patch mode, usually the
	// CustomWords stage's Candidates method.
	Candidates func(text string) []Candidate
	// Known lists the names a rewrite may introduce without counting as an
	// invention: the persona names and custom words.
	Known []string
	Logf  func(string, ...any)

	calls, skipped, timeouts, errs, applied, rejected, changed, keptRaw atomic.Int64
}

// Name implements Stage.
func (m *Model) Name() string { return "model" }

// Stats reports the counters.
func (m *Model) Stats() ModelStats {
	return ModelStats{
		Calls: m.calls.Load(), Skipped: m.skipped.Load(), Timeouts: m.timeouts.Load(),
		Errors: m.errs.Load(), Applied: m.applied.Load(), Rejected: m.rejected.Load(),
		ChangedLines: m.changed.Load(), KeptRaw: m.keptRaw.Load(),
	}
}

func (m *Model) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// Apply implements Stage. It returns an error only for the caller's own
// cancellation; every model-side failure keeps the input and is counted.
func (m *Model) Apply(ctx context.Context, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return text, err
	}
	if m == nil || m.T == nil || strings.TrimSpace(text) == "" {
		return text, nil
	}
	switch m.Mode {
	case ModeRewrite:
		return m.rewrite(ctx, text), nil
	default:
		return m.patch(ctx, text), nil
	}
}

func (m *Model) call(ctx context.Context, user string) (string, bool) {
	if m.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.Timeout)
		defer cancel()
	}
	m.calls.Add(1)
	system := ""
	if m.Prompt != nil {
		system = m.Prompt()
	}
	reply, err := m.T.Transform(ctx, system, user)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			m.timeouts.Add(1)
			m.logf("post-processing model: deadline after %s, transcript kept", m.Timeout)
		} else {
			m.errs.Add(1)
			m.logf("post-processing model: %v, transcript kept", err)
		}
		return "", false
	}
	return CleanReply(reply), true
}

// --- patch ---

type substitution struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type patchReply struct {
	Substitutions []substitution `json:"substitutions"`
}

// PatchUserMessage is what the model sees in patch mode: the transcript,
// then the candidates it may choose from.
func PatchUserMessage(text string, cands []Candidate) string {
	var b strings.Builder
	b.WriteString("Transcript:\n")
	b.WriteString(text)
	b.WriteString("\n\nCandidates:\n")
	for _, c := range cands {
		fmt.Fprintf(&b, "- %q may be %q\n", c.Span, c.Term)
	}
	return b.String()
}

func (m *Model) patch(ctx context.Context, text string) string {
	var cands []Candidate
	if m.Candidates != nil {
		cands = m.Candidates(text)
	}
	if len(cands) == 0 {
		m.skipped.Add(1)
		return text
	}
	reply, ok := m.call(ctx, PatchUserMessage(text, cands))
	if !ok {
		return text
	}
	var parsed patchReply
	dec := json.NewDecoder(strings.NewReader(reply))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		m.errs.Add(1)
		m.logf("post-processing model: reply is not a substitution list (%v), transcript kept", err)
		return text
	}
	out := text
	for _, s := range parsed.Substitutions {
		if !offered(cands, s) {
			m.rejected.Add(1)
			m.logf("post-processing model: substitution %q → %q was not offered, skipped", s.From, s.To)
			continue
		}
		if !strings.Contains(out, s.From) {
			m.rejected.Add(1)
			continue
		}
		out = strings.Replace(out, s.From, s.To, 1)
		m.applied.Add(1)
	}
	return out
}

func offered(cands []Candidate, s substitution) bool {
	for _, c := range cands {
		if c.Span == s.From && c.Term == s.To {
			return true
		}
	}
	return false
}

// --- rewrite ---

const (
	rewriteMinRatio = 0.6
	rewriteMaxRatio = 1.5
	rewriteMinF1    = 0.5
	// rewriteMaxAdded is how many words a cleaned line may contain that the
	// spoken line did not, beyond digits and known names. A cleanup removes
	// words and recases them; a line that gains two is being written, not
	// cleaned ("He doesn't have that much energy" for "Have that much energy").
	rewriteMaxAdded = 1
)

// RewriteUserMessage numbers the lines so the reply can be matched back.
func RewriteUserMessage(lines []string) string {
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%d. %s\n", i+1, l)
	}
	return b.String()
}

func (m *Model) rewrite(ctx context.Context, text string) string {
	lines := strings.Split(text, "\n")
	reply, ok := m.call(ctx, RewriteUserMessage(lines))
	if !ok {
		return text
	}
	got := numberedLines(reply)
	if len(got) != len(lines) {
		m.keptRaw.Add(int64(len(lines)))
		m.logf("post-processing model: reply has %d lines for %d, window kept raw", len(got), len(lines))
		return text
	}
	out := make([]string, len(lines))
	for i, raw := range lines {
		clean := got[i]
		switch {
		case strings.TrimSpace(raw) == "":
			out[i] = raw
		case strings.TrimSpace(clean) == "":
			out[i] = raw
			m.keptRaw.Add(1)
		case !rewriteFaithful(raw, clean) || addedWords(raw, clean, m.Known) > rewriteMaxAdded:
			out[i] = raw
			m.keptRaw.Add(1)
			m.logf("post-processing model: line %d failed the fidelity guard, kept raw", i+1)
		default:
			out[i] = clean
			if clean != raw {
				m.changed.Add(1)
			}
		}
	}
	return strings.Join(out, "\n")
}

var numberedLine = regexp.MustCompile(`^\s*(\d+)[.):]\s?(.*)$`)

// numberedLines parses "N. text" lines in order; a reply that is not numbered
// throughout yields nil.
func numberedLines(reply string) []string {
	var out []string
	next := 1
	for _, l := range strings.Split(strings.TrimSpace(reply), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		mm := numberedLine.FindStringSubmatch(l)
		if mm == nil {
			if len(out) > 0 {
				out[len(out)-1] += " " + strings.TrimSpace(l)
				continue
			}
			return nil
		}
		n, err := strconv.Atoi(mm[1])
		if err != nil || n != next {
			return nil
		}
		out = append(out, strings.TrimSpace(mm[2]))
		next++
	}
	return out
}

// rewriteFaithful is the per-line guard: a cleanup keeps most of the words
// and about the same length; an answer or a paraphrase does neither.
func rewriteFaithful(raw, clean string) bool {
	rw, cw := bagOfWords(raw), bagOfWords(clean)
	if len(rw) == 0 {
		return true
	}
	ratio := float64(len(cw)) / float64(len(rw))
	if ratio < rewriteMinRatio || ratio > rewriteMaxRatio {
		return false
	}
	return bagF1(rw, cw) >= rewriteMinF1
}

// addedWords counts the words in clean that raw does not contain, ignoring
// digits, symbols and the known names a cleanup is expected to introduce.
func addedWords(raw, clean string, known []string) int {
	have := map[string]int{}
	for _, w := range bagOfWords(raw) {
		have[w]++
	}
	allowed := map[string]bool{}
	for _, k := range known {
		for _, w := range bagOfWords(k) {
			allowed[w] = true
		}
	}
	added := 0
	for _, w := range bagOfWords(clean) {
		if have[w] > 0 {
			have[w]--
			continue
		}
		if allowed[w] || isNumeric(w) {
			continue
		}
		added++
	}
	return added
}

func isNumeric(key string) bool {
	for i := 0; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	return key != ""
}

func bagOfWords(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		if k := Key(f); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func bagF1(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	counts := make(map[string]int, len(a))
	for _, w := range a {
		counts[w]++
	}
	overlap := 0
	for _, w := range b {
		if counts[w] > 0 {
			counts[w]--
			overlap++
		}
	}
	if overlap == 0 {
		return 0
	}
	p := float64(overlap) / float64(len(b))
	r := float64(overlap) / float64(len(a))
	return 2 * p * r / (p + r)
}

// --- reply hygiene ---

var (
	thinkBlock  = regexp.MustCompile(`(?s)<think>.*?</think>`)
	codeFence   = regexp.MustCompile("(?s)^```[a-zA-Z]*\\s*(.*?)\\s*```$")
	zeroWidthRe = regexp.MustCompile("[\u200B\u200C\u200D\uFEFF]")
)

// CleanReply strips a thinking block, zero-width characters and a code fence
// around the whole reply, then trims it.
func CleanReply(reply string) string {
	reply = thinkBlock.ReplaceAllString(reply, "")
	reply = zeroWidthRe.ReplaceAllString(reply, "")
	reply = strings.TrimSpace(reply)
	if mm := codeFence.FindStringSubmatch(reply); mm != nil {
		reply = strings.TrimSpace(mm[1])
	}
	return reply
}
