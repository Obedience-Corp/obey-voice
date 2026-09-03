package scene

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// fakeBrain records what a persona's brain was told and answers with numbered
// lines, so a test can prove the seeding, the alternation and the tools gate
// without a model. It honours the Provider contract: an error leaves the
// history exactly as it was.
type fakeBrain struct {
	name    string
	history []brain.Turn
	loaded  [][]brain.Turn
	inputs  []string
	tools   []bool
	reply   func(call int) (string, error)
	cancel  context.CancelFunc
	cleared int
}

func (b *fakeBrain) ThinkFull(_ context.Context, input string, opts brain.StreamOptions) (string, error) {
	b.inputs = append(b.inputs, input)
	b.tools = append(b.tools, opts.ToolsEnabled)
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	text := fmt.Sprintf("%s line %d", b.name, len(b.inputs))
	if b.reply != nil {
		var err error
		if text, err = b.reply(len(b.inputs)); err != nil {
			return "", err
		}
	}
	b.history = append(b.history,
		brain.Turn{Role: "user", Content: input},
		brain.Turn{Role: "assistant", Content: text})
	return text, nil
}

func (b *fakeBrain) ClearHistory() {
	b.cleared++
	b.history = nil
}

func (b *fakeBrain) LoadHistory(turns []brain.Turn) {
	b.history = slices.Clone(turns)
	b.loaded = append(b.loaded, slices.Clone(turns))
}

// fakeBrains is a factory keyed by the applied config's agent name, which is
// the persona's display name after Apply.
type fakeBrains struct {
	made  map[string]*fakeBrain
	calls int
	fail  map[string]error
	setup func(b *fakeBrain)
}

func newFakeBrains() *fakeBrains {
	return &fakeBrains{made: map[string]*fakeBrain{}, fail: map[string]error{}}
}

func (f *fakeBrains) factory(cfg *config.Config) (Brain, error) {
	f.calls++
	if err := f.fail[cfg.AgentName]; err != nil {
		return nil, err
	}
	b := &fakeBrain{name: cfg.AgentName}
	if f.setup != nil {
		f.setup(b)
	}
	f.made[cfg.AgentName] = b
	return b, nil
}

func (f *fakeBrains) sam() *fakeBrain   { return f.made["Samantha"] }
func (f *fakeBrains) uncle() *fakeBrain { return f.made["Uncle Fu"] }

const generateScript = `schema: obey-voice.scene.v1
id: coffee-improv
title: Coffee counter
mode: generate
cast:
  - id: sam
    persona: samantha
  - id: uncle
    persona: uncle-fu
scene:
  setting: A coffee counter, morning rush.
  beats:
    - Uncle dodges why he is late.
    - Sam does not buy it.
  opener:
    speaker: sam
    text: "You're late."
  max_turns: 6
  stop_phrases: ["[END]"]
`

const generateScriptNoOpener = `schema: obey-voice.scene.v1
id: coffee-improv
mode: generate
cast:
  - id: sam
    persona: samantha
  - id: uncle
    persona: uncle-fu
scene:
  beats:
    - They argue about the bus.
  max_turns: 4
`

// generateCast is two applied cast members with the configs a brain factory
// keys on. Brain routing is snapshotted the way BuildCast would.
func generateCast() []CastRuntime {
	return []CastRuntime{
		{
			ID: "sam", PersonaID: "samantha", DisplayName: "Samantha",
			BrainProvider: "ollama", BrainModel: "qwen3:8b",
			Template: SynthTemplate{Provider: "kokoro", Voice: "af_heart"},
			Config:   &config.Config{AgentName: "Samantha"},
		},
		{
			ID: "uncle", PersonaID: "uncle-fu", DisplayName: "Uncle Fu",
			BrainProvider: "ollama", BrainModel: "qwen3:8b",
			Template: SynthTemplate{Provider: "kokoro", Voice: "am_michael"},
			Config:   &config.Config{AgentName: "Uncle Fu"},
		},
	}
}

// generateRequest assembles the request the runner would have built for a
// generate script: parsed document, effective plan, applied cast.
func generateRequest(t *testing.T, dir, script string, opts Options) Request {
	t.Helper()
	scriptPath := filepath.Join(dir, "script.yaml")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	doc, err := Parse([]byte(script))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts.Script = scriptPath
	if opts.OutDir == "" {
		opts.OutDir = filepath.Join(dir, "out")
	}
	return Request{
		Doc:     doc,
		Plan:    &Plan{Mode: opts.EffectiveMode(doc), MaxTurns: opts.EffectiveMaxTurns(doc)},
		Cast:    generateCast(),
		Options: opts,
	}
}

func resolveWith(t *testing.T, req Request, brains *fakeBrains) (Resolved, error) {
	t.Helper()
	return Resolver{NewBrain: brains.factory}.Resolve(t.Context(), req)
}

func readResolved(t *testing.T, outDir string) *Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outDir, ResolvedScriptFile))
	if err != nil {
		t.Fatalf("reading resolved script: %v", err)
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("the resolved script does not load as a scene: %v", err)
	}
	return doc
}

// sameTurn compares the parts of a history turn a scene writes; brain.Turn
// itself is not comparable.
func sameTurn(got brain.Turn, role, content string) bool {
	return got.Role == role && got.Content == content
}

func speakers(turns []Turn) []string {
	out := make([]string, 0, len(turns))
	for _, turn := range turns {
		out = append(out, turn.Speaker)
	}
	return out
}

// D11: the opener is turn 0 and is never asked for; the other speaker hears it
// as their first input; the two then alternate to the cap. Both brains read the
// same briefing first, and tools are off on every call.
func TestSceneResolveAlternatesFromTheOpener(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScript, Options{})
	brains := newFakeBrains()

	resolved, err := resolveWith(t, req, brains)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{"sam", "uncle", "sam", "uncle", "sam", "uncle"}
	if got := speakers(resolved.Turns); !slices.Equal(got, want) {
		t.Fatalf("speakers = %v, want %v", got, want)
	}
	if resolved.Generated != 5 || resolved.Stopped != StoppedMaxTurns {
		t.Errorf("generated %d, stopped %q; want 5 generated at max_turns", resolved.Generated, resolved.Stopped)
	}
	if resolved.Turns[0].Text != "You're late." {
		t.Errorf("turn 0 = %q, want the opener verbatim", resolved.Turns[0].Text)
	}

	sam, uncle := brains.sam(), brains.uncle()
	if uncle.inputs[0] != "You're late." {
		t.Errorf("uncle's first input = %q, want the opener", uncle.inputs[0])
	}
	if len(sam.inputs) != 2 || sam.inputs[0] != resolved.Turns[1].Text {
		t.Errorf("sam was asked %d times, first with %q; want twice, first with uncle's line 1", len(sam.inputs), sam.inputs[0])
	}
	for _, b := range []*fakeBrain{sam, uncle} {
		if slices.Contains(b.tools, true) {
			t.Errorf("%s was asked with tools enabled", b.name)
		}
		if b.cleared != 1 {
			t.Errorf("%s history cleared %d times, want 1", b.name, b.cleared)
		}
	}

	briefing := sam.loaded[0][0]
	if briefing.Role != "user" || !sameTurn(uncle.loaded[0][0], "user", briefing.Content) {
		t.Fatalf("the two brains were not seeded with the same user briefing:\n%v\n%v", sam.loaded[0][0], uncle.loaded[0][0])
	}
	for _, want := range []string{"Coffee counter", "A coffee counter, morning rush.", "Samantha and Uncle Fu", "- Uncle dodges why he is late.", "no tool calls", "only your own next spoken line, in the first person", "do not write the other character's words"} {
		if !strings.Contains(briefing.Content, want) {
			t.Errorf("briefing lacks %q:\n%s", want, briefing.Content)
		}
	}
	// After turn 1, sam's own history holds the opener as her line and uncle's
	// holds the briefing and then sam's line as what he was told.
	if got := sam.loaded[0][1]; !sameTurn(got, "assistant", "You're late.") {
		t.Errorf("sam's seeded opener = %+v", got)
	}
	if got := uncle.history[1]; !sameTurn(got, "user", "You're late.") {
		t.Errorf("uncle's history[1] = %+v, want sam's line as a user turn", got)
	}
}

// The resolved script is the renderer's input: verbatim, every line, the brief
// it came from, and each speaker's effective stack.
func TestSceneResolveWritesAVerbatimResolvedScript(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScript, Options{})
	brains := newFakeBrains()
	if _, err := resolveWith(t, req, brains); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	doc := readResolved(t, req.Options.OutDir)
	if doc.Mode != ModeVerbatim || len(doc.Turns) != 6 {
		t.Errorf("resolved script = %s mode with %d turns, want verbatim with 6", doc.Mode, len(doc.Turns))
	}
	// The fingerprint is the authored brief's, not the resolved document's:
	// the generated lines must not feed back into it.
	authored := mustParse(t, generateScript)
	if doc.GenerateFingerprint == "" || doc.GenerateFingerprint != GenerateFingerprint(authored, req.Cast) {
		t.Errorf("generate_fingerprint = %q, want the brief's fingerprint", doc.GenerateFingerprint)
	}
	if doc.Scene == nil || len(doc.Scene.Beats) != 2 {
		t.Error("the scene block was not preserved")
	}
	if got := doc.Cast[1]; got.DisplayName != "Uncle Fu" || got.TTSProvider != "kokoro" || got.TTSVoice != "am_michael" || got.BrainProvider != "ollama" || got.BrainModel != "qwen3:8b" {
		t.Errorf("cast[1] effective stack = %+v", got)
	}
	if len(req.Doc.Turns) != 6 {
		t.Errorf("the request document has %d turns after resolve, want 6 for the renderer", len(req.Doc.Turns))
	}
	if req.Doc.Cast[0].TTSVoice != "" {
		t.Error("the caller's document had the output-only cast fields written into it")
	}
	if _, err := os.Stat(filepath.Join(req.Options.OutDir, ScriptFile)); err != nil {
		t.Errorf("the input script was not copied: %v", err)
	}
}

// Without an opener the first speaker is cast[0] and the only non-dialogue
// input any brain ever hears is the opening cue.
func TestSceneResolveOpensWithTheCueWhenNoOpener(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScriptNoOpener, Options{})
	brains := newFakeBrains()

	resolved, err := resolveWith(t, req, brains)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := speakers(resolved.Turns); !slices.Equal(got, []string{"sam", "uncle", "sam", "uncle"}) {
		t.Fatalf("speakers = %v", got)
	}
	if brains.sam().inputs[0] != openingCue {
		t.Errorf("sam's first input = %q, want %q", brains.sam().inputs[0], openingCue)
	}
	if brains.uncle().inputs[0] != resolved.Turns[0].Text {
		t.Errorf("uncle's first input = %q, want sam's first line %q", brains.uncle().inputs[0], resolved.Turns[0].Text)
	}
	if resolved.Generated != 4 {
		t.Errorf("generated %d, want 4", resolved.Generated)
	}
}

// D31: a stop phrase ends the scene and keeps the line that carried it.
func TestSceneResolveKeepsTheStopPhraseLine(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScript, Options{})
	brains := newFakeBrains()
	brains.setup = func(b *fakeBrain) {
		if b.name == "Uncle Fu" {
			b.reply = func(int) (string, error) { return "Fine. [END]", nil }
		}
	}

	resolved, err := resolveWith(t, req, brains)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Stopped != StoppedStopPhrase || len(resolved.Turns) != 2 {
		t.Fatalf("stopped %q with %d turns, want stop_phrase after 2", resolved.Stopped, len(resolved.Turns))
	}
	if resolved.Turns[1].Text != "Fine. [END]" {
		t.Errorf("the stop line was not kept: %q", resolved.Turns[1].Text)
	}
	if len(brains.sam().inputs) != 0 {
		t.Errorf("sam was asked to reply after the stop phrase")
	}
	if got := readResolved(t, req.Options.OutDir).Turns; len(got) != 2 {
		t.Errorf("resolved script has %d turns, want 2", len(got))
	}
}

// D15: a brain error stops the run and keeps what was written; nothing after
// the failed turn is asked for.
func TestSceneResolveBrainErrorStopsAndKeepsThePrefix(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScript, Options{})
	boom := errors.New("ollama: connection refused")
	brains := newFakeBrains()
	brains.setup = func(b *fakeBrain) {
		if b.name == "Uncle Fu" {
			b.reply = func(call int) (string, error) {
				if call == 2 {
					return "", boom
				}
				return fmt.Sprintf("uncle line %d", call), nil
			}
		}
	}

	resolved, err := resolveWith(t, req, brains)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the brain's error wrapped", err)
	}
	if !strings.Contains(err.Error(), "turn 4 (uncle)") {
		t.Errorf("error does not name the turn and speaker: %v", err)
	}
	if len(resolved.Turns) != 3 || resolved.Generated != 2 {
		t.Errorf("kept %d turns (%d generated), want the 3-line prefix", len(resolved.Turns), resolved.Generated)
	}
	if got := readResolved(t, req.Options.OutDir).Turns; len(got) != 3 {
		t.Errorf("resolved script has %d turns, want 3", len(got))
	}
	if len(brains.sam().inputs) != 1 {
		t.Errorf("sam was asked %d times after the failure, want 1 before it", len(brains.sam().inputs))
	}
}

func TestSceneResolveEmptyLineIsAnError(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScript, Options{})
	brains := newFakeBrains()
	brains.setup = func(b *fakeBrain) {
		b.reply = func(int) (string, error) { return "  \n", nil }
	}
	_, err := resolveWith(t, req, brains)
	if err == nil || !strings.Contains(err.Error(), "empty line") {
		t.Fatalf("error = %v, want an empty-line refusal", err)
	}
}

// D30: --resume replays the written lines into both brains and asks for the
// first missing one. Stopped at three, resumed to six: the next speaker's last
// user text is line three, and the prefix survives.
func TestSceneResolveResumesFromTheResolvedScript(t *testing.T) {
	dir := t.TempDir()
	first := generateRequest(t, dir, generateScript, Options{MaxTurns: 3, MaxTurnsSet: true})
	if _, err := resolveWith(t, first, newFakeBrains()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	prefix := slices.Clone(readResolved(t, first.Options.OutDir).Turns)
	if got := speakers(prefix); !slices.Equal(got, []string{"sam", "uncle", "sam"}) {
		t.Fatalf("first run wrote %v", got)
	}

	second := generateRequest(t, dir, generateScript, Options{Resume: true})
	brains := newFakeBrains()
	resolved, err := resolveWith(t, second, brains)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if brains.calls != 2 {
		t.Errorf("resumed run constructed %d brains, want 2", brains.calls)
	}
	if resolved.Generated != 3 || len(resolved.Turns) != 6 {
		t.Fatalf("resumed run generated %d for %d total, want 3 more on the 3-line prefix", resolved.Generated, len(resolved.Turns))
	}
	for i, turn := range prefix {
		if resolved.Turns[i] != turn {
			t.Errorf("turn %d changed on resume: %+v -> %+v", i, turn, resolved.Turns[i])
		}
	}

	// uncle speaks turn 4: he was seeded with the briefing, line 1 (user) and
	// line 2 (his own), then asked line 3.
	uncle := brains.uncle()
	if len(uncle.loaded) != 1 || len(uncle.loaded[0]) != 3 {
		t.Fatalf("uncle was seeded with %v", uncle.loaded)
	}
	if uncle.loaded[0][1].Role != "user" || uncle.loaded[0][1].Content != prefix[0].Text ||
		uncle.loaded[0][2].Role != "assistant" || uncle.loaded[0][2].Content != prefix[1].Text {
		t.Errorf("uncle's replayed history = %+v", uncle.loaded[0][1:])
	}
	if uncle.inputs[0] != prefix[2].Text {
		t.Errorf("uncle's first resumed input = %q, want line 3 %q", uncle.inputs[0], prefix[2].Text)
	}
	lastUser := ""
	for i := len(uncle.history) - 1; i >= 0; i-- {
		if uncle.history[i].Role == "user" {
			lastUser = uncle.history[i].Content
			break
		}
	}
	if lastUser != resolved.Turns[4].Text {
		t.Errorf("uncle's last user text = %q, want sam's line 5", lastUser)
	}
	// sam spoke last: her whole prefix is replayed, line 3 as her own.
	sam := brains.sam()
	if len(sam.loaded[0]) != 4 || !sameTurn(sam.loaded[0][3], "assistant", prefix[2].Text) {
		t.Errorf("sam's replayed history = %+v", sam.loaded[0])
	}
}

// D30: max_turns is not in the fingerprint; the brief is.
func TestSceneResolveFingerprintTracksTheBriefNotTheCap(t *testing.T) {
	base := generateRequest(t, t.TempDir(), generateScript, Options{})
	raised := generateRequest(t, t.TempDir(), strings.Replace(generateScript, "max_turns: 6", "max_turns: 12", 1), Options{})
	rebeat := generateRequest(t, t.TempDir(), strings.Replace(generateScript, "Sam does not buy it.", "Sam buys it.", 1), Options{})
	reset := generateRequest(t, t.TempDir(), strings.Replace(generateScript, "morning rush", "closing time", 1), Options{})

	fp := GenerateFingerprint(base.Doc, base.Cast)
	if GenerateFingerprint(raised.Doc, raised.Cast) != fp {
		t.Error("raising max_turns changed the fingerprint")
	}
	if GenerateFingerprint(rebeat.Doc, rebeat.Cast) == fp {
		t.Error("changing a beat kept the fingerprint")
	}
	if GenerateFingerprint(reset.Doc, reset.Cast) == fp {
		t.Error("changing the setting kept the fingerprint")
	}
	recast := generateCast()
	recast[1].PersonaID = "aunt-fu"
	if GenerateFingerprint(base.Doc, recast) == fp {
		t.Error("changing a persona kept the fingerprint")
	}
}

// A prefix written for a different brief is not resumed: the run starts over
// from the opener. --overwrite starts over regardless.
func TestSceneResolveStartsFreshOnMismatchOrOverwrite(t *testing.T) {
	for name, tc := range map[string]struct {
		script string
		opts   Options
	}{
		"beat changed": {strings.Replace(generateScript, "Sam does not buy it.", "Sam buys it.", 1), Options{Resume: true}},
		"--overwrite":  {generateScript, Options{Resume: true, Overwrite: true}},
		"no --resume":  {generateScript, Options{}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			first := generateRequest(t, dir, generateScript, Options{MaxTurns: 3, MaxTurnsSet: true})
			if _, err := resolveWith(t, first, newFakeBrains()); err != nil {
				t.Fatalf("first run: %v", err)
			}
			second := generateRequest(t, dir, tc.script, tc.opts)
			brains := newFakeBrains()
			resolved, err := resolveWith(t, second, brains)
			if err != nil {
				t.Fatalf("second run: %v", err)
			}
			if resolved.Generated != 5 || len(resolved.Turns) != 6 {
				t.Errorf("second run generated %d of %d, want a fresh 5 on the opener", resolved.Generated, len(resolved.Turns))
			}
			if brains.uncle().inputs[0] != "You're late." {
				t.Errorf("the fresh run did not start from the opener: %q", brains.uncle().inputs[0])
			}
		})
	}
}

// A resumed run that already reached its end constructs no brain: nothing is
// asked, nothing costs a model load.
func TestSceneResolveFinishedRunConstructsNoBrain(t *testing.T) {
	for name, tc := range map[string]struct {
		firstOpts Options
		reply     func(int) (string, error)
		stopped   string
	}{
		"at the cap":       {Options{MaxTurns: 3, MaxTurnsSet: true}, nil, StoppedMaxTurns},
		"on a stop phrase": {Options{}, func(int) (string, error) { return "Enough. [END]", nil }, StoppedStopPhrase},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			first := generateRequest(t, dir, generateScript, tc.firstOpts)
			setup := newFakeBrains()
			setup.setup = func(b *fakeBrain) { b.reply = tc.reply }
			if _, err := resolveWith(t, first, setup); err != nil {
				t.Fatalf("first run: %v", err)
			}

			again := generateRequest(t, dir, generateScript, Options{Resume: true, MaxTurns: first.Plan.MaxTurns, MaxTurnsSet: true})
			brains := newFakeBrains()
			resolved, err := resolveWith(t, again, brains)
			if err != nil {
				t.Fatalf("resumed run: %v", err)
			}
			if brains.calls != 0 {
				t.Errorf("a finished run constructed %d brain(s)", brains.calls)
			}
			if resolved.Generated != 0 || resolved.Stopped != tc.stopped {
				t.Errorf("generated %d, stopped %q; want 0 and %q", resolved.Generated, resolved.Stopped, tc.stopped)
			}
		})
	}
}

// Cancellation returns the lines written so far and the context's error; the
// resolved script on disk matches.
func TestSceneResolveCancelKeepsThePrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := generateRequest(t, t.TempDir(), generateScript, Options{})
	brains := newFakeBrains()
	brains.setup = func(b *fakeBrain) {
		if b.name == "Uncle Fu" {
			b.cancel = cancel
		}
	}

	resolved, err := Resolver{NewBrain: brains.factory}.Resolve(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(resolved.Turns) != 2 {
		t.Errorf("kept %d turns, want the opener and the line in flight", len(resolved.Turns))
	}
	if got := readResolved(t, req.Options.OutDir).Turns; len(got) != 2 {
		t.Errorf("resolved script has %d turns, want 2", len(got))
	}
}

// A verbatim request has nothing to generate: the scripts are written with the
// effective stack and no brain is constructed.
func TestSceneResolveVerbatimWritesScriptsOnly(t *testing.T) {
	req := generateRequest(t, t.TempDir(), twoVoiceScript, Options{ResolveOnly: true})
	brains := newFakeBrains()

	resolved, err := resolveWith(t, req, brains)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if brains.calls != 0 {
		t.Errorf("a verbatim resolve constructed %d brain(s)", brains.calls)
	}
	if len(resolved.Turns) != 3 || resolved.Generated != 0 {
		t.Errorf("resolved %d turns (%d generated), want the 3 written ones", len(resolved.Turns), resolved.Generated)
	}
	doc := readResolved(t, req.Options.OutDir)
	if doc.GenerateFingerprint != "" {
		t.Errorf("a verbatim resolved script carries a generate fingerprint %q", doc.GenerateFingerprint)
	}
	if doc.Cast[0].TTSVoice != "af_heart" || doc.Cast[0].BrainModel != "qwen3:8b" {
		t.Errorf("cast[0] effective stack = %+v", doc.Cast[0])
	}
}

// A resolved script that is present but unreadable is an error, not a silent
// restart: a hand-edited prefix the operator meant to keep must not vanish.
func TestSceneResolveBrokenResolvedScriptIsAnError(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScript, Options{Resume: true})
	if err := os.MkdirAll(req.Options.OutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(req.Options.OutDir, ResolvedScriptFile), []byte("turns: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveWith(t, req, newFakeBrains())
	if err == nil || !strings.Contains(err.Error(), "resuming") {
		t.Fatalf("error = %v, want a resume failure", err)
	}
}

// --mode generate on a script with no brief is refused at plan time, before a
// config is loaded or a brain constructed.
func TestScenePlanRefusesGenerateWithoutABrief(t *testing.T) {
	doc := mustParse(t, twoVoiceScript)
	_, err := BuildPlan(t.Context(), doc, Options{Script: "s.yaml", OutDir: "out", Mode: "generate", ModeSet: true}, twoVoices())
	if err == nil || !strings.Contains(err.Error(), "beats or an opener") {
		t.Fatalf("error = %v, want the missing-brief refusal", err)
	}
}

func TestSceneResolveRefusesAnIncompleteRequest(t *testing.T) {
	req := generateRequest(t, t.TempDir(), generateScript, Options{})
	for name, tc := range map[string]struct {
		mutate func(r *Request, res *Resolver)
		want   string
	}{
		"no brain factory": {func(_ *Request, res *Resolver) { res.NewBrain = nil }, "no brain factory"},
		"one speaker":      {func(r *Request, _ *Resolver) { r.Cast = r.Cast[:1] }, "generate needs 2 speakers"},
		"no document":      {func(r *Request, _ *Resolver) { r.Doc = nil }, "no script document"},
	} {
		t.Run(name, func(t *testing.T) {
			r := req
			res := Resolver{NewBrain: newFakeBrains().factory}
			tc.mutate(&r, &res)
			_, err := res.Resolve(t.Context(), r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
