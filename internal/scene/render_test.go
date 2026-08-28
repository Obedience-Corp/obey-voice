package scene

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAudio stands in for the cgo WAV stack: it writes a real (tiny) file so
// resume's on-disk check is honest, and remembers the samples so the mix reads
// back exactly what synthesis produced.
type fakeAudio struct {
	stored    map[string]TurnPCM
	written   []string
	failWrite map[string]bool
	failRead  map[string]bool
}

func newFakeAudio() *fakeAudio {
	return &fakeAudio{stored: map[string]TurnPCM{}, failWrite: map[string]bool{}, failRead: map[string]bool{}}
}

func (f *fakeAudio) write(path string, rate int, samples []float32) error {
	if f.failWrite[filepath.Base(path)] {
		return errors.New("synthetic write failure")
	}
	if err := os.WriteFile(path, []byte("RIFF"), 0o644); err != nil {
		return err
	}
	f.stored[path] = TurnPCM{Samples: samples, Rate: rate}
	f.written = append(f.written, filepath.Base(path))
	return nil
}

func (f *fakeAudio) read(path string) ([]float32, int, error) {
	if f.failRead[filepath.Base(path)] {
		return nil, 0, errors.New("synthetic read failure")
	}
	pcm, ok := f.stored[path]
	if !ok {
		return nil, 0, fmt.Errorf("no wav recorded at %s", path)
	}
	return pcm.Samples, pcm.Rate, nil
}

// turnWAVs is what was written under turns/, in order.
func (f *fakeAudio) turnWAVs() []string {
	var out []string
	for _, name := range f.written {
		if name != ConversationFile {
			out = append(out, name)
		}
	}
	return out
}

// synthCall records one synthesis request so a test can prove the turn's own
// template travelled with it.
type synthCall struct {
	Template SynthTemplate
	Text     string
}

// fakeSynth speaks a fixed number of samples per character, fails on nominated
// lines, and can cancel the run after its first call.
type fakeSynth struct {
	rate     int
	failText map[string]bool
	silent   map[string]bool
	cancel   context.CancelFunc
	calls    []synthCall
}

func (s *fakeSynth) synthesize(_ context.Context, tmpl SynthTemplate, text string) ([]float32, int, error) {
	s.calls = append(s.calls, synthCall{Template: tmpl, Text: text})
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.failText[text] {
		return nil, 0, errors.New("synthetic synth failure")
	}
	if s.silent[text] {
		return nil, s.rate, nil
	}
	return make([]float32, len(text)*100), s.rate, nil
}

const twoVoiceScript = `schema: obey-voice.scene.v1
id: coffee-counter
title: Coffee counter
mode: verbatim
pause: 400ms
cast:
  - id: sam
    persona: samantha
  - id: uncle
    persona: uncle-fu
turns:
  - speaker: sam
    text: "You're late."
  - speaker: uncle
    text: "The bus had opinions."
  - speaker: sam
    text: "The bus is not on trial."
`

// sceneFixture writes the script to disk and returns the request a runner would
// have assembled: validated document, resolved plan, applied cast.
func sceneFixture(t *testing.T, dir string, personas ...*ttsPersona) Request {
	t.Helper()
	return sceneFixtureFrom(t, dir, twoVoiceScript, personas...)
}

func sceneFixtureFrom(t *testing.T, dir, script string, personas ...*ttsPersona) Request {
	t.Helper()
	scriptPath := filepath.Join(dir, "script.yaml")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	doc, err := Parse([]byte(script))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := Options{Script: scriptPath, OutDir: filepath.Join(dir, "out")}
	cast := []CastRuntime{}
	for _, p := range personas {
		cast = append(cast, p.runtime())
	}
	return Request{
		Doc:     doc,
		Plan:    &Plan{Pause: 400 * time.Millisecond, Mode: ModeVerbatim},
		Cast:    cast,
		Options: opts,
	}
}

// ttsPersona is a cast member without a config: BuildCast is tested separately,
// and the render loop only ever reads the template.
type ttsPersona struct {
	id       string
	persona  string
	template SynthTemplate
}

func (p *ttsPersona) runtime() CastRuntime {
	return CastRuntime{
		ID:          p.id,
		PersonaID:   p.persona,
		DisplayName: p.persona,
		Template:    p.template,
	}
}

func kokoroCast() []*ttsPersona {
	return []*ttsPersona{
		{id: "sam", persona: "samantha", template: SynthTemplate{Provider: "kokoro", Voice: "af_heart"}},
		{id: "uncle", persona: "uncle-fu", template: SynthTemplate{Provider: "kokoro", Voice: "am_michael"}},
	}
}

func newRenderer(audio *fakeAudio, synth *fakeSynth) Renderer {
	return Renderer{
		Synthesize: synth.synthesize,
		WriteWAV:   audio.write,
		ReadWAV:    audio.read,
		Resample:   ResampleLinear,
	}
}

func TestSceneRenderRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	audio, synth := newFakeAudio(), &fakeSynth{rate: 24000}
	req := sceneFixture(t, dir, kokoroCast()...)

	noTurns := req
	docNoTurns := *req.Doc
	docNoTurns.Turns = nil
	noTurns.Doc = &docNoTurns

	noOutDir := req
	noOutDir.Options.OutDir = ""

	noCast := req
	noCast.Cast = nil

	cases := map[string]struct {
		renderer Renderer
		req      Request
	}{
		"no synthesizer": {Renderer{WriteWAV: audio.write, ReadWAV: audio.read, Resample: ResampleLinear}, req},
		"no wav writer":  {Renderer{Synthesize: synth.synthesize, ReadWAV: audio.read, Resample: ResampleLinear}, req},
		"no wav reader":  {Renderer{Synthesize: synth.synthesize, WriteWAV: audio.write, Resample: ResampleLinear}, req},
		"no resampler":   {Renderer{Synthesize: synth.synthesize, WriteWAV: audio.write, ReadWAV: audio.read}, req},
		"no turns":       {newRenderer(audio, synth), noTurns},
		"no cast":        {newRenderer(audio, synth), noCast},
		"no out-dir":     {newRenderer(audio, synth), noOutDir},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.renderer.Render(context.Background(), tc.req); err == nil {
				t.Fatal("Render accepted an invocation it cannot execute")
			}
		})
	}
	if len(synth.calls) != 0 {
		t.Errorf("a rejected invocation synthesized %d turn(s)", len(synth.calls))
	}
}

func TestSceneRenderWritesConversationAndManifest(t *testing.T) {
	dir := t.TempDir()
	audio, synth := newFakeAudio(), &fakeSynth{rate: 24000}
	req := sceneFixture(t, dir, kokoroCast()...)

	result, err := newRenderer(audio, synth).Render(context.Background(), req)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	m := result.Manifest
	if m.Schema != RunSchema {
		t.Errorf("schema = %q, want %q", m.Schema, RunSchema)
	}
	if complete, skipped, failed := m.Counts(); complete != 3 || skipped != 0 || failed != 0 {
		t.Errorf("counts = %d/%d/%d, want 3/0/0", complete, skipped, failed)
	}
	if m.Conversation != ConversationFile || result.Conversation == "" {
		t.Errorf("conversation = %q / %q, want the mix", m.Conversation, result.Conversation)
	}
	if m.SampleRate != 24000 || m.PauseMS != 400 {
		t.Errorf("sample_rate = %d, pause_ms = %d, want 24000 / 400", m.SampleRate, m.PauseMS)
	}
	if m.SourceSHA256 == "" || m.Source == "" {
		t.Errorf("manifest does not identify its source: %q / %q", m.Source, m.SourceSHA256)
	}

	// The video step's contract: speaker, text, wav, and clocks that add up.
	for i, turn := range m.Turns {
		if turn.Speaker == "" || turn.Text == "" || turn.WAV == "" || turn.DurationMS <= 0 {
			t.Errorf("turn %d is missing a video-step field: %+v", i, turn)
		}
		if want := fmt.Sprintf("turns/%03d-%s.wav", i, turn.Speaker); turn.WAV != want {
			t.Errorf("turn %d wav = %q, want %q", i, turn.WAV, want)
		}
		if i > 0 {
			prev := m.Turns[i-1]
			if want := prev.StartMS + prev.DurationMS + m.PauseMS; turn.StartMS != want {
				t.Errorf("turn %d starts at %d ms, want %d", i, turn.StartMS, want)
			}
		}
	}
	if m.Turns[0].StartMS != 0 {
		t.Errorf("first turn starts at %d ms, want 0", m.Turns[0].StartMS)
	}

	for _, name := range []string{ScriptFile, ResolvedScriptFile, ConversationFile} {
		if _, err := os.Stat(filepath.Join(req.Options.OutDir, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
	// The resolved script is always verbatim, so a human can edit a line and
	// re-run without regenerating.
	resolved, err := os.ReadFile(filepath.Join(req.Options.OutDir, ResolvedScriptFile))
	if err != nil {
		t.Fatalf("reading resolved script: %v", err)
	}
	doc, err := Parse(resolved)
	if err != nil {
		t.Fatalf("the resolved script does not load as a scene: %v", err)
	}
	if doc.Mode != ModeVerbatim || len(doc.Turns) != 3 {
		t.Errorf("resolved script = %s mode with %d turns, want verbatim with 3", doc.Mode, len(doc.Turns))
	}
}

// D12/D16: each turn carries its own speaker's voice AND language, so two
// personas sharing one worker do not inherit the constructing persona's mouth.
func TestSceneRenderSendsThePerTurnTemplate(t *testing.T) {
	dir := t.TempDir()
	audio, synth := newFakeAudio(), &fakeSynth{rate: 24000}
	req := sceneFixture(t, dir,
		&ttsPersona{id: "sam", persona: "samantha", template: SynthTemplate{Provider: "qwen3-tts", Tier: "0.6b", Voice: "Vivian", Language: "English"}},
		&ttsPersona{id: "uncle", persona: "uncle-fu", template: SynthTemplate{Provider: "qwen3-tts", Tier: "0.6b", Voice: "Uncle_Fu", Language: "Chinese"}},
	)

	if _, err := newRenderer(audio, synth).Render(context.Background(), req); err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := []SynthTemplate{
		{Provider: "qwen3-tts", Tier: "0.6b", Voice: "Vivian", Language: "English"},
		{Provider: "qwen3-tts", Tier: "0.6b", Voice: "Uncle_Fu", Language: "Chinese"},
		{Provider: "qwen3-tts", Tier: "0.6b", Voice: "Vivian", Language: "English"},
	}
	if len(synth.calls) != len(want) {
		t.Fatalf("%d synthesis calls, want %d", len(synth.calls), len(want))
	}
	for i, call := range synth.calls {
		if call.Template != want[i] {
			t.Errorf("call %d template = %+v, want %+v", i, call.Template, want[i])
		}
		if call.Text != req.Doc.Turns[i].Text {
			t.Errorf("call %d text = %q, want %q", i, call.Text, req.Doc.Turns[i].Text)
		}
	}
}

// One bad turn is recorded and kept visible; the rest of the scene still
// records, and the mix simply omits the missing line.
func TestSceneRenderContinuesAfterOneFailedTurn(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	synth := &fakeSynth{rate: 24000, failText: map[string]bool{"The bus had opinions.": true}}
	req := sceneFixture(t, dir, kokoroCast()...)

	result, err := newRenderer(audio, synth).Render(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when a turn fails")
	}
	m := result.Manifest
	if complete, _, failed := m.Counts(); complete != 2 || failed != 1 {
		t.Errorf("counts complete=%d failed=%d, want 2/1", complete, failed)
	}
	bad := m.Turns[1]
	if bad.Status != StatusFailed || bad.ResumeKey != "" || bad.WAV != "" {
		t.Errorf("failed turn = %+v, want failed status with no resume key and no wav", bad)
	}
	if len(audio.turnWAVs()) != 2 {
		t.Errorf("wrote %v, want only the two turns that spoke", audio.turnWAVs())
	}
	// The mix is still written: two thirds of a scene beats none of it, and the
	// manifest says which line is missing.
	if result.Conversation == "" || m.Conversation == "" {
		t.Error("a partially failed run wrote no conversation.wav")
	}
	if m.Turns[2].StartMS != m.Turns[0].DurationMS+m.PauseMS {
		t.Errorf("turn 2 starts at %d ms; the failed turn should occupy no time", m.Turns[2].StartMS)
	}
}

// Nothing spoke, so there is nothing to mix: the run must not leave an empty
// conversation.wav that looks like a recording.
func TestSceneRenderAllTurnsFailedWritesNoConversation(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	synth := &fakeSynth{rate: 24000, failText: map[string]bool{
		"You're late.": true, "The bus had opinions.": true, "The bus is not on trial.": true,
	}}
	req := sceneFixture(t, dir, kokoroCast()...)

	result, err := newRenderer(audio, synth).Render(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when every turn fails")
	}
	if result.Conversation != "" || result.Manifest.Conversation != "" {
		t.Errorf("conversation = %q / %q, want none", result.Conversation, result.Manifest.Conversation)
	}
	if _, err := os.Stat(filepath.Join(req.Options.OutDir, ConversationFile)); !os.IsNotExist(err) {
		t.Error("an all-failed run left a conversation.wav behind")
	}
}

// A cancelled run returns the turns it finished so the manifest stays
// resumable, and writes no mix: a truncated conversation that looks whole is
// worse than none.
func TestSceneRenderCancellationReturnsPrefix(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	audio := newFakeAudio()
	synth := &fakeSynth{rate: 24000, cancel: cancel}
	req := sceneFixture(t, dir, kokoroCast()...)

	result, err := newRenderer(audio, synth).Render(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(result.Manifest.Turns) != 1 {
		t.Fatalf("partial manifest has %d turn(s), want 1", len(result.Manifest.Turns))
	}
	if result.Manifest.Turns[0].Status != StatusComplete {
		t.Errorf("the finished turn = %s, want complete", result.Manifest.Turns[0].Status)
	}
	if result.Conversation != "" {
		t.Error("a cancelled run wrote a conversation.wav")
	}
	if len(synth.calls) != 1 {
		t.Errorf("%d synthesis calls after cancellation, want 1", len(synth.calls))
	}
}

// The full recovery loop: a run that fails one turn, persisted, then a resume
// that skips the good turns and retries only the bad one.
func TestSceneRenderResumeSkipsAndRetries(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	bad := &fakeSynth{rate: 24000, failText: map[string]bool{"The bus had opinions.": true}}
	req := sceneFixture(t, dir, kokoroCast()...)

	first, err := newRenderer(audio, bad).Render(context.Background(), req)
	if err == nil {
		t.Fatal("first pass should report the failed turn")
	}
	if err := WriteRunManifest(ManifestPath(req.Options.OutDir), first.Manifest); err != nil {
		t.Fatalf("persisting the partial manifest: %v", err)
	}

	audio.written = nil
	good := &fakeSynth{rate: 24000}
	resume := req
	resume.Options.Resume = true
	second, err := newRenderer(audio, good).Render(context.Background(), resume)
	if err != nil {
		t.Fatalf("resume after a partial failure: %v", err)
	}
	if complete, skipped, failed := second.Manifest.Counts(); complete != 1 || skipped != 2 || failed != 0 {
		t.Errorf("resume counts = %d/%d/%d, want 1/2/0", complete, skipped, failed)
	}
	if len(good.calls) != 1 || good.calls[0].Text != "The bus had opinions." {
		t.Errorf("resume synthesized %+v, want only the failed line", good.calls)
	}
	if got := audio.turnWAVs(); len(got) != 1 || got[0] != "001-uncle.wav" {
		t.Errorf("resume wrote %v, want only 001-uncle.wav", got)
	}
	// The mix is rebuilt from every turn on disk, skipped ones included, so a
	// resumed run produces the same conversation as a clean one.
	if second.Manifest.SampleRate != 24000 || second.Manifest.DurationMS <= 0 {
		t.Errorf("resumed mix = %d Hz / %d ms, want a rebuilt conversation", second.Manifest.SampleRate, second.Manifest.DurationMS)
	}
	for i, turn := range second.Manifest.Turns {
		if turn.DurationMS <= 0 {
			t.Errorf("turn %d has no duration after resume: %+v", i, turn)
		}
	}
}

// --overwrite re-records even when --resume would have skipped.
func TestSceneRenderOverwriteReRecords(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	req := sceneFixture(t, dir, kokoroCast()...)

	first, err := newRenderer(audio, &fakeSynth{rate: 24000}).Render(context.Background(), req)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := WriteRunManifest(ManifestPath(req.Options.OutDir), first.Manifest); err != nil {
		t.Fatalf("persisting the manifest: %v", err)
	}

	again := req
	again.Options.Resume = true
	again.Options.Overwrite = true
	synth := &fakeSynth{rate: 24000}
	second, err := newRenderer(audio, synth).Render(context.Background(), again)
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if len(synth.calls) != 3 {
		t.Errorf("%d synthesis calls with --overwrite, want all 3", len(synth.calls))
	}
	if _, skipped, _ := second.Manifest.Counts(); skipped != 0 {
		t.Errorf("--overwrite skipped %d turn(s), want 0", skipped)
	}
}

// A changed voice must re-record: the resume key folds the mouth in, so the
// same line spoken by a different persona is not the same audio.
func TestSceneRenderResumeInvalidatedByVoiceChange(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	req := sceneFixture(t, dir, kokoroCast()...)

	first, err := newRenderer(audio, &fakeSynth{rate: 24000}).Render(context.Background(), req)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := WriteRunManifest(ManifestPath(req.Options.OutDir), first.Manifest); err != nil {
		t.Fatalf("persisting the manifest: %v", err)
	}

	recast := kokoroCast()
	recast[1].template.Voice = "am_adam"
	changed := sceneFixture(t, dir, recast...)
	changed.Options.OutDir = req.Options.OutDir
	changed.Options.Resume = true

	synth := &fakeSynth{rate: 24000}
	if _, err := newRenderer(audio, synth).Render(context.Background(), changed); err != nil {
		t.Fatalf("resume after a recast: %v", err)
	}
	if len(synth.calls) != 1 || synth.calls[0].Text != "The bus had opinions." {
		t.Errorf("recast re-recorded %+v, want only the recast speaker's line", synth.calls)
	}
}

// A line that cleans away to nothing is silence, not a failure. It records as
// complete with no duration, and takes no time in the mix.
func TestSceneRenderSilentTurnIsComplete(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	synth := &fakeSynth{rate: 24000, silent: map[string]bool{"The bus had opinions.": true}}
	req := sceneFixture(t, dir, kokoroCast()...)

	result, err := newRenderer(audio, synth).Render(context.Background(), req)
	if err != nil {
		t.Fatalf("a silent turn is not a failure: %v", err)
	}
	silent := result.Manifest.Turns[1]
	if silent.Status != StatusComplete || silent.DurationMS != 0 || silent.WAV != "" {
		t.Errorf("silent turn = %+v, want complete with no audio", silent)
	}
	if _, _, failed := result.Manifest.Counts(); failed != 0 {
		t.Errorf("%d failed turn(s), want 0", failed)
	}
}

// A WAV that cannot be written is a failed turn, not a corrupt output: the
// partial file is removed so the next --resume sees a missing turn.
func TestSceneRenderRemovesPartialTurnWAV(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	audio.failWrite["001-uncle.wav"] = true
	req := sceneFixture(t, dir, kokoroCast()...)

	result, err := newRenderer(audio, &fakeSynth{rate: 24000}).Render(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when a turn's WAV cannot be written")
	}
	if _, statErr := os.Stat(filepath.Join(req.Options.OutDir, TurnsDir, "001-uncle.wav")); !os.IsNotExist(statErr) {
		t.Error("a failed write left a WAV behind")
	}
	if result.Manifest.Turns[1].Status != StatusFailed {
		t.Errorf("turn = %+v, want failed", result.Manifest.Turns[1])
	}
}

// Progress is reported once per turn, for a human watching a slow Qwen run.
func TestSceneRenderReportsProgress(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	renderer := newRenderer(audio, &fakeSynth{rate: 24000})
	var lines []string
	renderer.Progress = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	if _, err := renderer.Render(context.Background(), sceneFixture(t, dir, kokoroCast()...)); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("%d progress lines, want 3", len(lines))
	}
	for _, want := range []string{"turn 1/3", "sam", "kokoro/af_heart"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("progress line %q is missing %q", lines[0], want)
		}
	}
}

// fiveTurnScript has room for a silent turn and a failed turn in the middle,
// with audio on both sides of each.
const fiveTurnScript = `schema: obey-voice.scene.v1
id: coffee-counter
mode: verbatim
pause: 400ms
cast:
  - id: sam
    persona: samantha
  - id: uncle
    persona: uncle-fu
turns:
  - speaker: sam
    text: "One."
  - speaker: uncle
    text: "Silent."
  - speaker: sam
    text: "Three."
  - speaker: uncle
    text: "Broken."
  - speaker: sam
    text: "Five."
`

// Every turn gets a place on the timeline, including the two that produce no
// audio. Without this a silent or failed line keeps start_ms 0 and a caption
// step puts it at the top of the conversation.
func TestSceneRenderClocksCoverSilentAndFailedTurns(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	synth := &fakeSynth{
		rate:     24000,
		silent:   map[string]bool{"Silent.": true},
		failText: map[string]bool{"Broken.": true},
	}
	req := sceneFixtureFrom(t, dir, fiveTurnScript, kokoroCast()...)

	result, err := newRenderer(audio, synth).Render(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error for the failed turn")
	}
	m := result.Manifest
	if complete, _, failed := m.Counts(); complete != 4 || failed != 1 {
		t.Fatalf("counts complete=%d failed=%d, want 4/1", complete, failed)
	}

	silent, broken := m.Turns[1], m.Turns[3]
	if silent.Status != StatusComplete || silent.DurationMS != 0 {
		t.Errorf("silent turn = %+v, want complete with no duration", silent)
	}
	if broken.Status != StatusFailed || broken.DurationMS != 0 {
		t.Errorf("failed turn = %+v, want failed with no duration", broken)
	}

	// A turn with no audio is stamped where the previous turn ended, and spends
	// no pause of its own.
	if want := m.Turns[0].StartMS + m.Turns[0].DurationMS; silent.StartMS != want {
		t.Errorf("silent turn starts at %d ms, want %d (the end of turn 0)", silent.StartMS, want)
	}
	if want := m.Turns[2].StartMS + m.Turns[2].DurationMS; broken.StartMS != want {
		t.Errorf("failed turn starts at %d ms, want %d (the end of turn 2)", broken.StartMS, want)
	}
	if silent.StartMS == 0 || broken.StartMS == 0 {
		t.Fatalf("a mid-conversation turn is still stamped at 0 ms: silent=%d failed=%d", silent.StartMS, broken.StartMS)
	}

	// The identity still holds across the turns that produced audio, with the
	// silent and failed turns costing nothing in between.
	audible := []int{0, 2, 4}
	for n := 0; n+1 < len(audible); n++ {
		prev, next := m.Turns[audible[n]], m.Turns[audible[n+1]]
		if want := prev.StartMS + prev.DurationMS + m.PauseMS; next.StartMS != want {
			t.Errorf("turn %d starts at %d ms, want %d", audible[n+1], next.StartMS, want)
		}
	}
	if last := m.Turns[4]; m.DurationMS != last.StartMS+last.DurationMS {
		t.Errorf("duration_ms = %d, want %d (no trailing silence)", m.DurationMS, last.StartMS+last.DurationMS)
	}
}

// A failed turn and an unmixable run are different problems; reporting only the
// first would hide why there is no conversation.wav.
func TestSceneRenderReportsBothFailureAndMixError(t *testing.T) {
	dir := t.TempDir()
	audio := newFakeAudio()
	audio.failRead["000-sam.wav"] = true
	synth := &fakeSynth{rate: 24000, failText: map[string]bool{"The bus had opinions.": true}}
	req := sceneFixture(t, dir, kokoroCast()...)

	result, err := newRenderer(audio, synth).Render(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"turn(s) failed", "synthetic read failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not report %q", err, want)
		}
	}
	if result.Conversation != "" {
		t.Error("a run that could not mix reported a conversation")
	}
}
