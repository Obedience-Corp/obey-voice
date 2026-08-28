package scene

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"
)

// Synthesizer speaks one turn with that cast member's template. The command
// layer injects it, so internal/scene never imports pkg/voiceagent/tts and the
// whole render path is testable without a model on disk.
type Synthesizer func(ctx context.Context, tmpl SynthTemplate, text string) (samples []float32, sampleRate int, err error)

// WAVWriter writes mono float32 PCM to a WAV file.
type WAVWriter func(path string, sampleRate int, samples []float32) error

// WAVReader reads a mono WAV back as float32 PCM at its recorded rate.
type WAVReader func(path string) (samples []float32, sampleRate int, err error)

// Renderer records a verbatim scene. Every capability that would drag in the
// cgo audio stack is a field, not an import.
type Renderer struct {
	Synthesize Synthesizer
	WriteWAV   WAVWriter
	ReadWAV    WAVReader
	Resample   Resampler

	// Progress reports one line per finished turn, for humans watching a long
	// Qwen run. Optional.
	Progress func(format string, args ...any)
}

// Request is one verbatim render: a validated script, its resolved plan, and
// the cast with their personas already applied.
type Request struct {
	Doc     *Document
	Plan    *Plan
	Cast    []CastRuntime
	Options Options
}

// Result is what the command layer needs to report and post-process a run.
type Result struct {
	Manifest RunManifest
	// Conversation is the absolute path of the combined WAV, empty when no
	// mix was written.
	Conversation string
}

// Render synthesizes every turn, concatenates them into conversation.wav, and
// returns the run manifest.
//
// One failed turn does not stop the rest: it is recorded as failed, carries no
// resume key, and the run returns an error alongside a manifest the caller can
// persist and re-run with --resume. A cancelled run keeps the turns it finished
// but writes no conversation.wav — a truncated mix that looks whole is worse
// than no mix at all.
func (r Renderer) Render(ctx context.Context, req Request) (Result, error) {
	if err := r.validate(req); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Join(req.Options.OutDir, TurnsDir), 0o755); err != nil {
		return Result{}, fmt.Errorf("scene: creating %s: %w", req.Options.OutDir, err)
	}
	script, err := writeScripts(req)
	if err != nil {
		return Result{}, err
	}

	turns, failed, firstErr := r.synthesizeTurns(ctx, req)
	manifest := newRunManifest(req, script, turns)
	if err := ctx.Err(); err != nil {
		return Result{Manifest: manifest}, fmt.Errorf("scene: %w", err)
	}

	conversation, mixErr := r.mix(req, &manifest)
	if failed > 0 {
		return Result{Manifest: manifest, Conversation: conversation},
			fmt.Errorf("scene: %d of %d turn(s) failed: %w", failed, len(turns), firstErr)
	}
	if mixErr != nil {
		return Result{Manifest: manifest}, mixErr
	}
	return Result{Manifest: manifest, Conversation: conversation}, nil
}

// validate checks the injected capabilities and the request shape before any
// directory is created.
func (r Renderer) validate(req Request) error {
	switch {
	case r.Synthesize == nil:
		return fmt.Errorf("scene: no synthesizer")
	case r.WriteWAV == nil:
		return fmt.Errorf("scene: no WAV writer")
	case r.ReadWAV == nil:
		return fmt.Errorf("scene: no WAV reader")
	case r.Resample == nil:
		return fmt.Errorf("scene: no resampler")
	case req.Doc == nil || req.Plan == nil:
		return fmt.Errorf("scene: no script document")
	case len(req.Doc.Turns) == 0:
		return fmt.Errorf("scene: nothing to record: the script has no turns")
	case len(req.Cast) == 0:
		return fmt.Errorf("scene: no cast")
	case req.Options.OutDir == "":
		return fmt.Errorf("scene: provide --out-dir DIR")
	}
	return nil
}

// synthesizeTurns speaks every line in order, returning the turns rendered so
// far plus how many failed. Cancellation stops promptly and returns the prefix;
// the caller detects it from the context, not from the returned error, so a
// turn failure earlier in the run is still reported.
func (r Renderer) synthesizeTurns(ctx context.Context, req Request) ([]RunTurn, int, error) {
	byID := castByID(req.Cast)
	prior := priorTurns(req.Options)
	turns := make([]RunTurn, 0, len(req.Doc.Turns))
	failed := 0
	var firstErr error

	for i, line := range req.Doc.Turns {
		if ctx.Err() != nil {
			return turns, failed, firstErr
		}
		member, ok := byID[line.Speaker]
		if !ok {
			// Validation resolves every speaker against the cast, so this is a
			// programming error. It is still recorded as a failed turn rather
			// than returned, because truncating here would silently drop every
			// line after it from the manifest.
			if firstErr == nil {
				firstErr = fmt.Errorf("scene: turn %d: no cast member %q", i, line.Speaker)
			}
			failed++
			turns = append(turns, RunTurn{
				Index: i, ID: fmt.Sprintf("%03d", i), Speaker: line.Speaker,
				Text: line.Text, TextSHA256: textHash(line.Text), Status: StatusFailed,
			})
			continue
		}
		started := time.Now()
		turn, err := r.renderTurn(ctx, req, i, line, member, prior)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			failed++
		}
		r.report(i, len(req.Doc.Turns), member, turn, time.Since(started))
		turns = append(turns, turn)
	}
	return turns, failed, firstErr
}

// renderTurn synthesizes one line to its own WAV, or skips it when --resume
// finds identical audio already on disk.
func (r Renderer) renderTurn(ctx context.Context, req Request, index int, line Turn, member CastRuntime, prior map[string]RunTurn) (RunTurn, error) {
	turn := newRunTurn(index, line, member)
	wavPath := filepath.Join(req.Options.OutDir, filepath.FromSlash(turn.WAV))

	if req.Options.Resume && !req.Options.Overwrite {
		if p, ok := prior[turn.WAV]; ok && resumable(p, turn.ResumeKey, wavPath) {
			turn.Status = StatusSkipped
			turn.SampleRate = p.SampleRate
			return turn, nil
		}
	}

	samples, rate, err := r.Synthesize(ctx, member.Template, line.Text)
	if err == nil && len(samples) > 0 {
		err = r.WriteWAV(wavPath, rate, samples)
	}
	if err != nil {
		// Remove whatever a half-finished write left behind: the next --resume
		// must see a missing turn, not a truncated WAV.
		os.Remove(wavPath)
		turn.Status = StatusFailed
		turn.ResumeKey = ""
		turn.WAV = ""
		return turn, fmt.Errorf("scene: turn %d (%s): %w", index, member.ID, err)
	}
	if len(samples) == 0 {
		// A line that cleans away to nothing — an emoji, a stray symbol — is
		// silence, not a failure. There is no WAV to point at and nothing a
		// retry would do differently, so it carries no resume key either.
		turn.WAV = ""
		turn.ResumeKey = ""
		return turn, nil
	}
	turn.SampleRate = rate
	return turn, nil
}

// newRunTurn is the manifest entry for one line before it has been spoken.
// Filenames come from the validated kebab cast id, never from author text, so a
// script cannot write outside its own turns directory.
func newRunTurn(index int, line Turn, member CastRuntime) RunTurn {
	name := fmt.Sprintf("%03d-%s", index, member.ID)
	rel := path.Join(TurnsDir, name+".wav")
	return RunTurn{
		Index:       index,
		ID:          name,
		Speaker:     line.Speaker,
		PersonaID:   member.PersonaID,
		DisplayName: member.DisplayName,
		Text:        line.Text,
		TextSHA256:  textHash(line.Text),
		WAV:         rel,
		TTSProvider: member.Template.Provider,
		TTSVoice:    member.Template.Voice,
		Status:      StatusComplete,
		ResumeKey:   turnResumeKey(member, line.Text, rel),
	}
}

// mix reads every turn that has audio back off disk, joins it with the
// configured pause, and writes conversation.wav.
//
// Reading the WAVs rather than keeping PCM from the synthesis loop is what
// makes a resumed run produce the same mix as a fresh one: a skipped turn only
// exists as a file. It also means the combined clocks always describe the file
// that was just written, so the manifest cannot describe a stale mix.
func (r Renderer) mix(req Request, m *RunManifest) (string, error) {
	spoken := make([]int, 0, len(m.Turns))
	pcm := make([]TurnPCM, 0, len(m.Turns))
	for i, turn := range m.Turns {
		if turn.WAV == "" || turn.Status == StatusFailed {
			continue
		}
		wavPath := filepath.Join(req.Options.OutDir, filepath.FromSlash(turn.WAV))
		samples, rate, err := r.ReadWAV(wavPath)
		if err != nil {
			return "", fmt.Errorf("scene: reading %s: %w", turn.WAV, err)
		}
		spoken = append(spoken, i)
		pcm = append(pcm, TurnPCM{Samples: samples, Rate: rate})
	}

	combined, err := ConcatTurns(pcm, req.Plan.Pause, r.Resample)
	if err != nil {
		return "", err
	}
	out := filepath.Join(req.Options.OutDir, ConversationFile)
	if err := r.WriteWAV(out, combined.Rate, combined.Samples); err != nil {
		return "", fmt.Errorf("scene: writing %s: %w", ConversationFile, err)
	}

	for n, i := range spoken {
		m.Turns[i].StartMS = combined.Clocks[n].StartMS
		m.Turns[i].DurationMS = combined.Clocks[n].DurationMS
		m.Turns[i].SampleRate = pcm[n].Rate
	}
	m.Conversation = ConversationFile
	m.SampleRate = combined.Rate
	m.PauseMS = combined.PauseMS
	m.DurationMS = combined.DurationMS
	return out, nil
}

// report prints one progress line per finished turn, e.g.
// "turn 3/8 sam (kokoro/af_heart) 1.8s".
func (r Renderer) report(index, total int, member CastRuntime, turn RunTurn, elapsed time.Duration) {
	if r.Progress == nil {
		return
	}
	r.Progress("turn %d/%d %s (%s) %s %s",
		index+1, total, member.ID, member.Template.Mouth(), turn.Status, elapsed.Round(100*time.Millisecond))
}

// newRunManifest assembles the run record. CreatedAt is stamped by the command
// layer, outside this deterministic core, so two identical runs differ only by
// that field.
func newRunManifest(req Request, script scriptRecord, turns []RunTurn) RunManifest {
	cast := make([]RunCast, 0, len(req.Cast))
	for _, member := range req.Cast {
		cast = append(cast, RunCast{
			ID:            member.ID,
			PersonaID:     member.PersonaID,
			DisplayName:   member.DisplayName,
			TTSProvider:   member.Template.Provider,
			TTSVoice:      member.Template.Voice,
			TTSTier:       member.Template.Tier,
			BrainProvider: member.BrainProvider,
			BrainModel:    member.BrainModel,
		})
	}
	return RunManifest{
		Schema:         RunSchema,
		Source:         script.Source,
		SourceSHA256:   script.SHA256,
		Title:          req.Doc.Title,
		Mode:           ModeVerbatim,
		PauseMS:        req.Plan.Pause.Milliseconds(),
		ScriptResolved: ResolvedScriptFile,
		Cast:           cast,
		Turns:          turns,
	}
}

// priorTurns indexes the previous run's turns by output path for resume.
func priorTurns(opts Options) map[string]RunTurn {
	prior := map[string]RunTurn{}
	if !opts.Resume || opts.Overwrite {
		return prior
	}
	m, ok := LoadRunManifest(ManifestPath(opts.OutDir))
	if !ok {
		return prior
	}
	for _, turn := range m.Turns {
		if turn.WAV != "" {
			prior[turn.WAV] = turn
		}
	}
	return prior
}

func castByID(cast []CastRuntime) map[string]CastRuntime {
	byID := make(map[string]CastRuntime, len(cast))
	for _, member := range cast {
		byID[member.ID] = member
	}
	return byID
}
