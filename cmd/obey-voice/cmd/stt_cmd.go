package cmd

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
	cmd.AddCommand(newSTTPreviewCmd(loadConfig))
	return cmd
}

// sttStageOverrides are the flags shared by audit and preview that turn a
// stage on for the run without touching config.
type sttStageOverrides struct {
	terms     []string
	threshold float64
	fillers   bool
	numbers   bool
}

func bindStageOverrides(f *pflag.FlagSet, p *sttStageOverrides) {
	f.StringSliceVar(&p.terms, "terms", nil, "Extra custom-word entries to try, in the config key's syntax")
	f.Float64Var(&p.threshold, "threshold", 0, "Fuzzy threshold to try (default: the configured one)")
	f.BoolVar(&p.fillers, "fillers", false, "Try filler removal on for this run")
	f.BoolVar(&p.numbers, "numbers", false, "Try numbers as digits on for this run")
}

// apply returns a copy of cfg with the overrides folded in.
func (o sttStageOverrides) apply(loaded *config.Config) *config.Config {
	cfg := *loaded
	if len(o.terms) > 0 {
		cfg.STTCustomWords = append(append([]string{}, cfg.STTCustomWords...), o.terms...)
	}
	if o.threshold > 0 {
		cfg.STTWordCorrectionThreshold = o.threshold
	}
	cfg.STTFillerRemoval = cfg.STTFillerRemoval || o.fillers
	cfg.STTNumbers = cfg.STTNumbers || o.numbers
	return &cfg
}

type sttAuditOptions struct {
	sessions bool
	meetings bool
	json     bool
	limit    int
	stages   sttStageOverrides
}

func newSTTAuditCmd(loadConfig func() (*config.Config, error)) *cobra.Command {
	var opts sttAuditOptions
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Replay the transcript corrections over saved transcripts",
		Long: `Replay the transcript corrections over saved transcripts.

Every user turn in saved conversations and every utterance in meeting bundles
is run through the same stages the live pipeline applies — filler removal,
custom words, numbers as digits — and every change they would make is listed
as phrase → replacement × count. Nothing is written.

Use it to check a term or a stage before trusting it: a phrase that should not
change is a term to remove, to alias exactly, or to drop the "!" from. Try
additions with --terms, a looser or stricter setting with --threshold, and the
other stages with --fillers and --numbers, all without touching config.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSTTAudit(cmd, loadConfig, opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.sessions, "sessions", false, "Replay saved conversations only")
	f.BoolVar(&opts.meetings, "meetings", false, "Replay meeting bundles only")
	f.IntVar(&opts.limit, "limit", 40, "Rows to print per table (0 for all)")
	f.BoolVar(&opts.json, "json", false, "Output machine-readable JSON")
	bindStageOverrides(f, &opts.stages)
	return cmd
}

type sttAuditUtterance struct {
	text string
}

type sttAuditRow struct {
	Phrase  string  `json:"phrase"`
	Term    string  `json:"term"`
	Kind    string  `json:"kind"` // spelling, alias, fuzzy, candidate, filler, number
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
	Stages         []string       `json:"stages"`
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
	Fillers        []sttAuditRow  `json:"filler_removals"`
	Numbers        []sttAuditRow  `json:"number_conversions"`
}

func runSTTAudit(cmd *cobra.Command, loadConfig func() (*config.Config, error), opts sttAuditOptions) error {
	cmd.SilenceUsage = true
	loaded, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg := opts.stages.apply(loaded)

	both := opts.sessions == opts.meetings
	var corpus []sttAuditUtterance
	var counts sttAuditCorpus
	if both || opts.sessions {
		corpus = append(corpus, sttAuditSessions(&counts)...)
	}
	if both || opts.meetings {
		fromMeetings, err := sttAuditMeetings(config.MeetingsDirFrom(cfg), &counts)
		if err != nil {
			return err
		}
		corpus = append(corpus, fromMeetings...)
	}
	result := sttAuditRun(cfg, corpus, counts)
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
		texts := meetingUtteranceTexts(events)
		counts.MeetingUtterances += len(texts)
		for _, t := range texts {
			out = append(out, sttAuditUtterance{text: t})
		}
	}
	return out, nil
}

// meetingUtteranceTexts returns the spoken lines of a meeting: the
// speaker-labelled utterances when analysis ran, the live ones otherwise.
func meetingUtteranceTexts(events []meetinglog.Event) []string {
	var live, labelled []string
	for _, e := range events {
		if strings.TrimSpace(e.Text) == "" {
			continue
		}
		switch e.Type {
		case meetinglog.TypeUtterance:
			live = append(live, e.Text)
		case meetinglog.TypeSpeakerUtterance:
			labelled = append(labelled, e.Text)
		}
	}
	if len(labelled) > 0 {
		return labelled
	}
	return live
}

func sttAuditRun(cfg *config.Config, corpus []sttAuditUtterance, counts sttAuditCorpus) sttAuditResult {
	cw, report := stt.CustomWordsFromConfig(cfg)
	_, stages := stt.StagesFromConfig(cfg)
	res := sttAuditResult{
		Schema:         sttAuditSchemaV1,
		Stages:         nonNil(stages.Stages),
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
	fillers := map[string]*sttAuditRow{}
	numbers := map[string]*sttAuditRow{}
	for _, u := range corpus {
		text := u.text
		if cfg.STTFillerRemoval {
			out, dropped := correct.ScanFillers(text)
			for _, d := range dropped {
				sttAuditTally(fillers, d, "", "filler", 0, u.text)
			}
			text = out
		}
		if !cw.Empty() {
			out, reps, cands := cw.Scan(text)
			for _, r := range reps {
				sttAuditTally(replacements, r.Span, r.Term, sttAuditKind(r), r.Score, u.text)
			}
			for _, c := range cands {
				sttAuditTally(candidates, c.Span, c.Term, "candidate", c.Score, u.text)
			}
			text = out
		}
		if cfg.STTNumbers {
			out, reps := correct.ScanNumbers(text)
			for _, r := range reps {
				sttAuditTally(numbers, r.Span, r.Term, "number", 0, u.text)
			}
			text = out
		}
		if text != u.text {
			res.Changed++
		}
	}
	res.Replacements = sttAuditRows(replacements)
	res.Candidates = sttAuditRows(candidates)
	res.Fillers = sttAuditRows(fillers)
	res.Numbers = sttAuditRows(numbers)
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
	for cut > 0 && text[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = limit
	}
	return text[:cut] + "…"
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func printSTTAudit(w io.Writer, res sttAuditResult, limit int) {
	fmt.Fprintf(w, "\n  %s\n\n", titleStyle.Render("Transcript corrections audit"))
	fmt.Fprintf(w, "  Stages: %s\n", strings.Join(res.Stages, " → "))
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
	printSTTAuditRows(w, "Custom words", res.Replacements, limit)
	printSTTAuditRows(w, "Custom-word candidates (near misses, never applied)", res.Candidates, limit)
	printSTTAuditRows(w, "Fillers removed", res.Fillers, limit)
	printSTTAuditRows(w, "Numbers converted", res.Numbers, limit)
	fmt.Fprintln(w)
}

func printSTTAuditRows(w io.Writer, title string, rows []sttAuditRow, limit int) {
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
		switch r.Kind {
		case "filler":
			line += keyStyle.Render(r.Phrase)
		case "number":
			line += fmt.Sprintf("%s → %s", keyStyle.Render(r.Phrase), r.Term)
		default:
			line += fmt.Sprintf("%-9s%s → %s", r.Kind, keyStyle.Render(r.Phrase), r.Term)
			if r.Kind == "fuzzy" || r.Kind == "candidate" {
				line += fmt.Sprintf(" (%.2f)", r.Score)
			}
		}
		fmt.Fprintln(w, line)
		fmt.Fprintf(w, "         %s\n", dimStyle.Render("e.g. "+r.Example))
	}
}

// --- preview ---

type sttPreviewOptions struct {
	diff   bool
	stages sttStageOverrides
}

func newSTTPreviewCmd(loadConfig func() (*config.Config, error)) *cobra.Command {
	var opts sttPreviewOptions
	cmd := &cobra.Command{
		Use:   "preview [meeting]",
		Short: "Render a meeting's notes with the transcript corrections applied",
		Long: `Render a meeting's notes with the transcript corrections applied.

The meeting is the latest bundle by default, or a .meeting directory,
meeting.md, or events.jsonl path. The configured stages run over every
utterance — plus any turned on for this run with --fillers, --numbers and
--terms — and the note is rendered the way meeting.md is. With --diff only the
utterances that changed are printed, before and after. Nothing is written.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			return runSTTPreview(cmd, loadConfig, path, opts)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.diff, "diff", false, "Print only the utterances that change, before and after")
	bindStageOverrides(f, &opts.stages)
	return cmd
}

func runSTTPreview(cmd *cobra.Command, loadConfig func() (*config.Config, error), path string, opts sttPreviewOptions) error {
	cmd.SilenceUsage = true
	loaded, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg := opts.stages.apply(loaded)
	chain, report := stt.StagesFromConfig(cfg)
	if chain.Len() == 0 {
		return fmt.Errorf("no correction stage is enabled; turn one on in config or pass --fillers, --numbers or --terms")
	}
	jsonlPath, err := meeting.ResolveMeetingFile(config.MeetingsDirFrom(cfg), path)
	if err != nil {
		return err
	}
	events, err := meeting.ReadEvents(jsonlPath)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	changed := 0
	// meeting.md shows the speaker-labelled utterances when analysis ran and
	// the live ones otherwise; the preview follows the same rule.
	spoken := meetinglog.TypeUtterance
	for _, e := range events {
		if e.Type == meetinglog.TypeSpeakerUtterance && strings.TrimSpace(e.Text) != "" {
			spoken = meetinglog.TypeSpeakerUtterance
			break
		}
	}
	for i := range events {
		e := &events[i]
		if e.Type != spoken || strings.TrimSpace(e.Text) == "" {
			continue
		}
		cleaned, err := chain.Correct(cmd.Context(), e.Text)
		if err != nil {
			return err
		}
		if cleaned == e.Text {
			continue
		}
		changed++
		if opts.diff {
			fmt.Fprintf(out, "%s %s\n%s %s\n\n", dimStyle.Render("-"), e.Text, keyStyle.Render("+"), cleaned)
		}
		e.Text = cleaned
	}
	if opts.diff {
		fmt.Fprintf(out, "%s\n", dimStyle.Render(fmt.Sprintf("%d utterance(s) changed · stages %s · %s", changed, strings.Join(report.Stages, " → "), jsonlPath)))
		return nil
	}
	summary, err := meeting.LoadSummaryFromJSONL(jsonlPath)
	if err != nil {
		return err
	}
	note := meeting.RenderEvents(summary, events, meeting.BodyFull)
	fmt.Fprint(out, note.Body)
	fmt.Fprintf(out, "\n%s\n", dimStyle.Render(fmt.Sprintf("preview: %d utterance(s) changed · stages %s · nothing written", changed, strings.Join(report.Stages, " → "))))
	return nil
}
