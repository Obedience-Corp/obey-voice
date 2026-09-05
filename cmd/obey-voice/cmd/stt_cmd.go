package cmd

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

// sttAuditSchemaV1 identifies the `stt audit --json` document.
const sttAuditSchemaV1 = "obey-voice.stt-audit.v1"

func init() {
	rootCmd.AddCommand(newSTTCmd(config.Load))
}

func newSTTCmd(loadConfig func() (*config.Config, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stt",
		Short: "Inspect what speech-to-text does with your words",
	}
	cmd.AddCommand(newSTTAuditCmd(loadConfig))
	return cmd
}

type sttAuditOptions struct {
	sessions  bool
	meetings  bool
	json      bool
	terms     []string
	threshold float64
	limit     int
}

func newSTTAuditCmd(loadConfig func() (*config.Config, error)) *cobra.Command {
	var opts sttAuditOptions
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Replay the custom-words correction over saved transcripts",
		Long: `Replay the custom-words correction over saved transcripts.

Every user turn in saved conversations and every utterance in meeting bundles
is run through the same correction the live pipeline applies, and every change
it would make is listed as phrase → term × count. Nothing is written.

Use it to check a term before trusting it: a phrase that should not change is
a term to remove, to alias exactly, or to drop the "!" from. Try additions with
--terms and a looser or stricter setting with --threshold without touching
config.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSTTAudit(cmd, loadConfig, opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.sessions, "sessions", false, "Replay saved conversations only")
	f.BoolVar(&opts.meetings, "meetings", false, "Replay meeting bundles only")
	f.StringSliceVar(&opts.terms, "terms", nil, "Extra custom-word entries to try, in the config key's syntax")
	f.Float64Var(&opts.threshold, "threshold", 0, "Fuzzy threshold to try (default: the configured one)")
	f.IntVar(&opts.limit, "limit", 40, "Rows to print per table (0 for all)")
	f.BoolVar(&opts.json, "json", false, "Output machine-readable JSON")
	return cmd
}

type sttAuditUtterance struct {
	text string
}

type sttAuditRow struct {
	Phrase  string  `json:"phrase"`
	Term    string  `json:"term"`
	Kind    string  `json:"kind"` // spelling, alias, fuzzy, candidate
	Count   int     `json:"count"`
	Score   float64 `json:"score,omitempty"`
	Example string  `json:"example"`
}

type sttAuditCorpus struct {
	Sessions          int `json:"sessions"`
	SessionUtterances int `json:"session_utterances"`
	Meetings          int `json:"meetings"`
	MeetingUtterances int `json:"meeting_utterances"`
	SkippedMeetings   int `json:"skipped_meetings,omitempty"`
}

type sttAuditResult struct {
	Schema         string         `json:"schema"`
	Threshold      float64        `json:"threshold"`
	Terms          []string       `json:"terms"`
	FuzzyTerms     []string       `json:"fuzzy_terms"`
	IgnoredEntries []string       `json:"ignored_entries"`
	DroppedTerms   int            `json:"dropped_terms"`
	Corpus         sttAuditCorpus `json:"corpus"`
	Utterances     int            `json:"utterances"`
	Changed        int            `json:"changed_utterances"`
	Replacements   []sttAuditRow  `json:"replacements"`
	Candidates     []sttAuditRow  `json:"candidates"`
}

func runSTTAudit(cmd *cobra.Command, loadConfig func() (*config.Config, error), opts sttAuditOptions) error {
	cmd.SilenceUsage = true
	loaded, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg := *loaded
	if len(opts.terms) > 0 {
		cfg.STTCustomWords = append(append([]string{}, cfg.STTCustomWords...), opts.terms...)
	}
	if opts.threshold > 0 {
		cfg.STTWordCorrectionThreshold = opts.threshold
	}
	cw, report := stt.CustomWordsFromConfig(&cfg)

	both := opts.sessions == opts.meetings
	var corpus []sttAuditUtterance
	var counts sttAuditCorpus
	if both || opts.sessions {
		corpus = append(corpus, sttAuditSessions(&counts)...)
	}
	if both || opts.meetings {
		fromMeetings, err := sttAuditMeetings(config.MeetingsDirFrom(&cfg), &counts)
		if err != nil {
			return err
		}
		corpus = append(corpus, fromMeetings...)
	}
	result := sttAuditRun(cw, report, corpus, counts)
	if opts.json {
		return encodeJSON(cmd, result)
	}
	printSTTAudit(cmd.OutOrStdout(), result, opts.limit)
	return nil
}

// sttAuditSessions collects every user turn from saved conversations.
func sttAuditSessions(counts *sttAuditCorpus) []sttAuditUtterance {
	var out []sttAuditUtterance
	for _, s := range session.List() {
		counts.Sessions++
		for _, turn := range s.Turns {
			if turn.Role != "user" || strings.TrimSpace(turn.Content) == "" {
				continue
			}
			counts.SessionUtterances++
			out = append(out, sttAuditUtterance{text: turn.Content})
		}
	}
	return out
}

// sttAuditMeetings collects utterances from every meeting bundle. A bundle
// that has speaker-labelled utterances contributes those, since they restate
// the live ones; otherwise the live utterances count.
func sttAuditMeetings(dir string, counts *sttAuditCorpus) ([]sttAuditUtterance, error) {
	pattern := filepath.Join(dir, "*"+meeting.BundleSuffix, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName)
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("list meetings under %s: %w", dir, err)
	}
	sort.Strings(paths)
	var out []sttAuditUtterance
	for _, path := range paths {
		events, err := meeting.ReadEvents(path)
		if err != nil {
			counts.SkippedMeetings++
			continue
		}
		counts.Meetings++
		var live, labelled []sttAuditUtterance
		for _, e := range events {
			if strings.TrimSpace(e.Text) == "" {
				continue
			}
			switch e.Type {
			case meetinglog.TypeUtterance:
				live = append(live, sttAuditUtterance{text: e.Text})
			case meetinglog.TypeSpeakerUtterance:
				labelled = append(labelled, sttAuditUtterance{text: e.Text})
			}
		}
		if len(labelled) > 0 {
			live = labelled
		}
		counts.MeetingUtterances += len(live)
		out = append(out, live...)
	}
	return out, nil
}

func sttAuditRun(cw *correct.CustomWords, report stt.TermReport, corpus []sttAuditUtterance, counts sttAuditCorpus) sttAuditResult {
	res := sttAuditResult{
		Schema:         sttAuditSchemaV1,
		Threshold:      report.Threshold,
		Terms:          make([]string, 0, len(report.Terms)),
		FuzzyTerms:     nonNil(report.Fuzzy),
		IgnoredEntries: nonNil(report.Ignored),
		DroppedTerms:   report.Dropped,
		Corpus:         counts,
		Utterances:     len(corpus),
	}
	for _, term := range report.Terms {
		res.Terms = append(res.Terms, term.Text)
	}
	replacements := map[string]*sttAuditRow{}
	candidates := map[string]*sttAuditRow{}
	for _, u := range corpus {
		_, reps, cands := cw.Scan(u.text)
		if len(reps) > 0 {
			res.Changed++
		}
		for _, r := range reps {
			sttAuditTally(replacements, r.Span, r.Term, sttAuditKind(r), r.Score, u.text)
		}
		for _, c := range cands {
			sttAuditTally(candidates, c.Span, c.Term, "candidate", c.Score, u.text)
		}
	}
	res.Replacements = sttAuditRows(replacements)
	res.Candidates = sttAuditRows(candidates)
	return res
}

func sttAuditKind(r correct.Replacement) string {
	switch {
	case !r.Exact:
		return "fuzzy"
	case correct.Key(r.Span) == correct.Key(r.Term):
		return "spelling"
	default:
		return "alias"
	}
}

func sttAuditTally(rows map[string]*sttAuditRow, span, term, kind string, score float64, example string) {
	phrase := strings.ToLower(span)
	key := phrase + "\x00" + term
	row := rows[key]
	if row == nil {
		row = &sttAuditRow{Phrase: phrase, Term: term, Kind: kind, Score: math.Round(score*1000) / 1000, Example: sttAuditExample(example)}
		rows[key] = row
	}
	row.Count++
}

func sttAuditRows(rows map[string]*sttAuditRow) []sttAuditRow {
	out := make([]sttAuditRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Phrase < out[j].Phrase
	})
	return out
}

func sttAuditExample(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	const limit = 90
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !isSpace(text[cut]) {
		cut--
	}
	if cut == 0 {
		cut = limit
	}
	return text[:cut] + "…"
}

func isSpace(b byte) bool { return b == ' ' }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func printSTTAudit(w io.Writer, res sttAuditResult, limit int) {
	fmt.Fprintf(w, "\n  %s\n\n", titleStyle.Render("Custom words audit"))
	fmt.Fprintf(w, "  Terms: %d", len(res.Terms))
	if len(res.FuzzyTerms) > 0 {
		fmt.Fprintf(w, " (fuzzy: %s)", strings.Join(res.FuzzyTerms, ", "))
	}
	fmt.Fprintf(w, " · threshold %.2f\n", res.Threshold)
	if len(res.IgnoredEntries) > 0 {
		quoted := make([]string, 0, len(res.IgnoredEntries))
		for _, e := range res.IgnoredEntries {
			quoted = append(quoted, fmt.Sprintf("%q", e))
		}
		fmt.Fprintf(w, "  Ignored entries: %s\n", strings.Join(quoted, ", "))
	}
	if res.DroppedTerms > 0 {
		fmt.Fprintf(w, "  Dropped %d term(s) beyond the cap of %d\n", res.DroppedTerms, correct.MaxTerms)
	}
	c := res.Corpus
	fmt.Fprintf(w, "  Corpus: %d session(s) with %d user turn(s), %d meeting(s) with %d utterance(s)", c.Sessions, c.SessionUtterances, c.Meetings, c.MeetingUtterances)
	if c.SkippedMeetings > 0 {
		fmt.Fprintf(w, ", %d unreadable meeting(s) skipped", c.SkippedMeetings)
	}
	fmt.Fprintf(w, "\n  %d of %d utterance(s) would change\n", res.Changed, res.Utterances)
	printSTTAuditRows(w, "Replacements", res.Replacements, limit, true)
	printSTTAuditRows(w, "Candidates (near misses, never applied)", res.Candidates, limit, false)
	fmt.Fprintln(w)
}

func printSTTAuditRows(w io.Writer, title string, rows []sttAuditRow, limit int, showKind bool) {
	fmt.Fprintf(w, "\n  %s\n", titleStyle.Render(title))
	if len(rows) == 0 {
		fmt.Fprintln(w, dimStyle.Render("    none"))
		return
	}
	for i, r := range rows {
		if limit > 0 && i >= limit {
			fmt.Fprintln(w, dimStyle.Render(fmt.Sprintf("    … %d more", len(rows)-limit)))
			return
		}
		line := fmt.Sprintf("  %5d  ", r.Count)
		if showKind {
			line += fmt.Sprintf("%-9s", r.Kind)
		}
		line += fmt.Sprintf("%s → %s", keyStyle.Render(r.Phrase), r.Term)
		if r.Kind == "fuzzy" || r.Kind == "candidate" {
			line += fmt.Sprintf(" (%.2f)", r.Score)
		}
		fmt.Fprintln(w, line)
		fmt.Fprintf(w, "         %s\n", dimStyle.Render("e.g. "+r.Example))
	}
}
