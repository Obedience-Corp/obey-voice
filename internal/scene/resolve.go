package scene

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Brain is the slice of brain.Provider a generated scene drives: think one
// turn with history, and load the history a resumed run replays.
type Brain interface {
	ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error)
	ClearHistory()
	LoadHistory(turns []brain.Turn)
}

// BrainFactory constructs a brain from a cast member's applied config copy.
// Production passes brain.NewProvider; tests pass a fake.
type BrainFactory func(cfg *config.Config) (Brain, error)

// openingCue is the only line a brain ever hears that no character spoke: it
// asks the opening speaker for the first line when the script wrote none.
const openingCue = "Speak your first line now."

// Stop reasons a generated run reports.
const (
	StoppedMaxTurns   = "max_turns"
	StoppedStopPhrase = "stop_phrase"
)

// Resolver turns a generate-mode script into concrete lines by letting the two
// personas' brains take turns. It never touches TTS.
type Resolver struct {
	NewBrain BrainFactory
	Progress func(format string, args ...any)
}

// Resolved is what resolving left behind: every line now in
// script.resolved.yaml, how many of them a brain wrote in this run, and why
// generation stopped.
type Resolved struct {
	Turns     []Turn
	Generated int
	Stopped   string
}

// Resolve fills req.Doc.Turns in place, persisting script.resolved.yaml after
// every generated line, so the renderer that follows reads the same document
// and a crash loses at most the line in flight.
//
// A verbatim request only writes the scripts: the lines are already there.
// A brain error stops the run (later lines depend on the history) and returns
// the prefix that was written; the caller decides not to record it.
func (r Resolver) Resolve(ctx context.Context, req Request) (Resolved, error) {
	if err := r.validate(req); err != nil {
		return Resolved{}, err
	}
	if err := os.MkdirAll(req.Options.OutDir, 0o755); err != nil {
		return Resolved{}, fmt.Errorf("scene: creating %s: %w", req.Options.OutDir, err)
	}
	if _, err := copyInputScript(req.Options); err != nil {
		return Resolved{}, err
	}
	if req.Plan.Mode != ModeGenerate {
		return Resolved{Turns: req.Doc.Turns}, writeResolvedScript(req)
	}

	d, err := r.newDialogue(req)
	if err != nil {
		return Resolved{}, err
	}
	return d.run(ctx)
}

func (r Resolver) validate(req Request) error {
	switch {
	case req.Doc == nil || req.Plan == nil:
		return fmt.Errorf("scene: no script document")
	case req.Options.OutDir == "":
		return fmt.Errorf("scene: provide --out-dir DIR")
	case req.Plan.Mode == ModeGenerate && r.NewBrain == nil:
		return fmt.Errorf("scene: no brain factory")
	case req.Plan.Mode == ModeGenerate && len(req.Cast) != castSize:
		return fmt.Errorf("scene: generate needs %d speakers, the cast has %d", castSize, len(req.Cast))
	case req.Plan.Mode == ModeGenerate && req.Doc.Scene == nil:
		return fmt.Errorf("scene: scene: generate mode needs beats or an opener")
	}
	return nil
}

// dialogue is one generate run: two speakers, their brains, the lines so far.
type dialogue struct {
	req      Request
	resolver Resolver
	cast     []CastRuntime
	brains   map[string]Brain
	briefing string
	maxTurns int
	turns    []Turn
	resumed  int
}

func (r Resolver) newDialogue(req Request) (*dialogue, error) {
	req.Doc.GenerateFingerprint = GenerateFingerprint(req.Doc, req.Cast)
	turns, err := startingTurns(req)
	if err != nil {
		return nil, err
	}
	d := &dialogue{
		req:      req,
		resolver: r,
		cast:     req.Cast,
		brains:   map[string]Brain{},
		briefing: briefingFor(req.Doc, req.Cast),
		maxTurns: cmp.Or(req.Plan.MaxTurns, DefaultMaxTurns),
		turns:    turns,
		resumed:  len(turns),
	}
	if len(turns) > 0 {
		if err := d.persist(); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// run generates until the cap, a stop phrase, an error or cancellation.
func (d *dialogue) run(ctx context.Context) (Resolved, error) {
	if stopped := d.stoppedAlready(); stopped != "" {
		return d.result(stopped), nil
	}
	if err := d.seedBrains(); err != nil {
		return d.result(""), err
	}
	for len(d.turns) < d.maxTurns {
		if err := ctx.Err(); err != nil {
			return d.result(""), fmt.Errorf("scene: %w", err)
		}
		line, err := d.speakNext(ctx)
		if err != nil {
			return d.result(""), err
		}
		if d.matchesStopPhrase(line.Text) {
			return d.result(StoppedStopPhrase), nil
		}
	}
	return d.result(StoppedMaxTurns), nil
}

func (d *dialogue) result(stopped string) Resolved {
	return Resolved{Turns: d.turns, Generated: len(d.turns) - d.resumed, Stopped: stopped}
}

// stoppedAlready reports whether the prefix on disk already ended the scene,
// so a resume of a finished run constructs no brain at all.
func (d *dialogue) stoppedAlready() string {
	switch {
	case len(d.turns) >= d.maxTurns:
		return StoppedMaxTurns
	case len(d.turns) > 0 && d.matchesStopPhrase(d.turns[len(d.turns)-1].Text):
		return StoppedStopPhrase
	}
	return ""
}

// seedBrains constructs both brains and gives each the same briefing followed
// by the lines already spoken — its own as assistant turns, the other's as user
// turns. The peer's final line is left out of the next speaker's history: it
// arrives as that speaker's next input instead, exactly once.
func (d *dialogue) seedBrains() error {
	next := d.nextSpeaker()
	for _, member := range d.cast {
		b, err := d.resolver.NewBrain(member.Config)
		if err != nil {
			return fmt.Errorf("scene: %s brain: %w", member.ID, err)
		}
		history := []brain.Turn{{Role: "user", Content: d.briefing}}
		replay := d.turns
		if member.ID == next && len(replay) > 0 {
			replay = replay[:len(replay)-1]
		}
		for _, turn := range replay {
			role := "user"
			if turn.Speaker == member.ID {
				role = "assistant"
			}
			history = append(history, brain.Turn{Role: role, Content: turn.Text})
		}
		b.ClearHistory()
		b.LoadHistory(history)
		d.brains[member.ID] = b
	}
	return nil
}

// speakNext asks the next speaker for a line, given the peer's last one, and
// persists it before anything else can go wrong.
func (d *dialogue) speakNext(ctx context.Context) (Turn, error) {
	speaker := d.nextSpeaker()
	input := openingCue
	if n := len(d.turns); n > 0 {
		input = d.turns[n-1].Text
	}
	started := time.Now()
	reply, err := d.brains[speaker].ThinkFull(ctx, input, brain.StreamOptions{ToolsEnabled: false})
	if err != nil {
		return Turn{}, fmt.Errorf("scene: turn %d (%s): %w", len(d.turns)+1, speaker, err)
	}
	text := strings.TrimSpace(reply)
	if text == "" {
		return Turn{}, fmt.Errorf("scene: turn %d (%s): the brain returned an empty line", len(d.turns)+1, speaker)
	}
	line := Turn{Speaker: speaker, Text: text}
	d.turns = append(d.turns, line)
	if err := d.persist(); err != nil {
		return Turn{}, err
	}
	d.progress(speaker, text, time.Since(started))
	return line, nil
}

// nextSpeaker alternates: whoever did not speak last, or the opener when no
// one has.
func (d *dialogue) nextSpeaker() string {
	if len(d.turns) == 0 {
		return d.cast[0].ID
	}
	last := d.turns[len(d.turns)-1].Speaker
	for _, member := range d.cast {
		if member.ID != last {
			return member.ID
		}
	}
	return last
}

func (d *dialogue) persist() error {
	d.req.Doc.Turns = d.turns
	return writeResolvedScript(d.req)
}

func (d *dialogue) matchesStopPhrase(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, phrase := range d.req.Doc.Scene.StopPhrases {
		if phrase != "" && strings.Contains(trimmed, phrase) {
			return true
		}
	}
	return false
}

func (d *dialogue) progress(speaker, text string, elapsed time.Duration) {
	if d.resolver.Progress == nil {
		return
	}
	member := castByID(d.cast)[speaker]
	d.resolver.Progress("turn %d/%d %s (%s) %s %q",
		len(d.turns), d.maxTurns, speaker, brainLabel(member), elapsed.Round(100*time.Millisecond), excerpt(text, 72))
}

func brainLabel(member CastRuntime) string {
	if member.BrainModel == "" {
		return cmp.Or(member.BrainProvider, "brain")
	}
	return member.BrainProvider + "/" + member.BrainModel
}

func excerpt(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// startingTurns picks the lines a run begins from: a resumed prefix written
// for this same brief, else the authored turns, else the opener alone.
func startingTurns(req Request) ([]Turn, error) {
	if req.Options.Resume && !req.Options.Overwrite {
		prefix, err := loadResolvedPrefix(req)
		if err != nil {
			return nil, err
		}
		if len(prefix) > 0 {
			return prefix, nil
		}
	}
	if len(req.Doc.Turns) > 0 {
		return req.Doc.Turns, nil
	}
	if opener := req.Doc.Scene.Opener; opener != nil {
		speaker := cmp.Or(opener.Speaker, req.Doc.Cast[0].ID)
		return []Turn{{Speaker: speaker, Text: strings.TrimSpace(opener.Text)}}, nil
	}
	return nil, nil
}

// loadResolvedPrefix reads the previous run's script.resolved.yaml and returns
// its turns when they were written for this brief. A missing file, an empty
// one, or one fingerprinted for a different brief yields no prefix; a file
// that is present but broken is an error, because a hand-edited line the
// operator wanted kept must not be discarded quietly.
func loadResolvedPrefix(req Request) ([]Turn, error) {
	path := filepath.Join(req.Options.OutDir, ResolvedScriptFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scene: resuming %s: %w", path, err)
	}
	prior, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("scene: resuming %s: %w", path, err)
	}
	if prior.GenerateFingerprint != req.Doc.GenerateFingerprint || len(prior.Turns) == 0 {
		return nil, nil
	}
	if err := prior.Validate(); err != nil {
		return nil, fmt.Errorf("scene: resuming %s: %w", path, err)
	}
	if err := sameCast(prior.Cast, req.Doc.Cast); err != nil {
		return nil, fmt.Errorf("scene: resuming %s: %w", path, err)
	}
	return prior.Turns, nil
}

func sameCast(prior, current []CastMember) error {
	if len(prior) != len(current) {
		return fmt.Errorf("cast has %d members, this script has %d", len(prior), len(current))
	}
	for i := range current {
		if prior[i].ID != current[i].ID {
			return fmt.Errorf("cast[%d] is %q, this script names %q", i, prior[i].ID, current[i].ID)
		}
	}
	return nil
}

// GenerateFingerprint identifies the brief a generated prefix was written for:
// renderer version, the personas in cast order, and everything the brains were
// told or given verbatim (setting, beats, opener, authored turns). max_turns is
// deliberately absent, so raising it resumes rather than restarts.
func GenerateFingerprint(doc *Document, cast []CastRuntime) string {
	h := sha256.New()
	write := func(s string) {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	write(RendererVersion)
	for _, member := range cast {
		write(member.PersonaID)
	}
	if block := doc.Scene; block != nil {
		write(block.Setting)
		for _, beat := range block.Beats {
			write(beat)
		}
		if block.Opener != nil {
			write(block.Opener.Speaker)
			write(block.Opener.Text)
		}
	}
	for _, turn := range doc.Turns {
		write(turn.Speaker)
		write(turn.Text)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// briefingFor is the one user message both brains read before any line. It
// names both characters, so the bytes are identical for each, and leans on the
// persona system prompt for who "you" is.
func briefingFor(doc *Document, cast []CastRuntime) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are performing a short spoken scene titled %s.\n", cmp.Or(doc.Title, doc.ID))
	if doc.Scene != nil && strings.TrimSpace(doc.Scene.Setting) != "" {
		fmt.Fprintf(&b, "Setting: %s\n", strings.TrimSpace(doc.Scene.Setting))
	}
	names := make([]string, 0, len(cast))
	for _, member := range cast {
		names = append(names, cmp.Or(member.DisplayName, member.ID))
	}
	fmt.Fprintf(&b, "Cast: %s. Stay in your own character.\n", strings.Join(names, " and "))
	b.WriteString("You are talking to the other character, not to a user or an operator.\n")
	b.WriteString("Reply with spoken dialogue only: no markdown, no stage directions, no tool calls.")
	if doc.Scene != nil && len(doc.Scene.Beats) > 0 {
		b.WriteString("\nBeats:")
		for _, beat := range doc.Scene.Beats {
			fmt.Fprintf(&b, "\n- %s", strings.TrimSpace(beat))
		}
	}
	return b.String()
}
