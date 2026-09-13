package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// runner is the conversation driver. PR 2 tests inject a fake; live
// voiceagent wiring lands in the follow-up that constructs an Agent.
type runner interface {
	runTurn(ctx context.Context, user string) error
	history() []brain.Turn
	playing() bool
	livePolicy() string
	workerPolicy() string
	workerTools() *bool
	liveCalled() bool
	workerCalled() bool
}

type backgroundRunner interface {
	runBackground(context.Context) error
	waitWork(context.Context, bool) error
}

type recoveryRunner interface {
	compact(context.Context) error
}

type recoveryHistoryRunner interface {
	importCanonicalHistory(string) error
}

type engine struct {
	latencyProvenance string
	latencySamples    []workLatencySample
	scn               *Scenario
	runDir            string
	rec               *recorder
	runner            runner
	player            *recordingPlayer
	fixtureConfig     string
	personaDir        string
	playing           func() bool
	profile           runProfile
	onClose           []func()

	started    time.Time
	stopReason string
	turns      []turnRecord
}

// turnRecord is the report's per-turn row.
type turnRecord struct {
	ID     string
	User   string
	Route  string
	Spoken string
	Err    string
}

const defaultScenarioWall = 5 * time.Minute

func (e *engine) run(ctx context.Context) (runErr error) {
	defer func() {
		runErr = errors.Join(runErr, e.closeAll())
		if p, ok := e.runner.(personaRunner); ok {
			runErr = errors.Join(runErr, p.writePersonaArtifacts(e.runDir), p.validatePersonaRun())
		}
	}()
	if err := os.MkdirAll(e.runDir, 0o700); err != nil {
		return err
	}
	wall := e.scn.Duration.Duration
	if wall == 0 {
		wall = defaultScenarioWall
	}
	ctx, cancel := context.WithTimeout(ctx, wall)
	defer cancel()

	e.started = time.Now()
	e.stopReason = "completed"
	if e.scn.HistoryFixture != "" {
		seed, ok := e.runner.(recoveryHistoryRunner)
		if !ok {
			return fmt.Errorf("history fixture: runner does not support canonical import")
		}
		if err := seed.importCanonicalHistory(e.scn.HistoryFixture); err != nil {
			return err
		}
	}
	for i, turn := range e.scn.Turns {
		if err := e.runTurn(ctx, i, turn); err != nil {
			e.stopReason = "turn_failed"
			if ctx.Err() != nil {
				e.stopReason = "timeout"
			}
			e.noteTurnError(turn.ID, err)
			_ = writeReport(e)
			return fmt.Errorf("turn %s: %w", turn.ID, err)
		}
	}
	if p, ok := e.runner.(personaRunner); ok {
		if err := p.writePersonaArtifacts(e.runDir); err != nil {
			return err
		}
		if err := p.validatePersonaRun(); err != nil {
			return err
		}
	}
	m := e.metrics()
	var failures []error
	for _, ex := range e.scn.Expect {
		if err := evalRunExpect(ex, e.rec, m); err != nil {
			failures = append(failures, fmt.Errorf("run expect: %w", err))
		}
	}
	if len(failures) > 0 {
		e.stopReason = "run_expect_failed"
	}
	if err := writeReport(e); err != nil {
		return err
	}
	if len(failures) > 0 {
		return failures[0]
	}
	return nil
}

func (e *engine) closeAll() error {
	// Retire and persist the runner before closing event capture or exporting
	// the final disk snapshot. Shutdown errors belong to this run's result.
	if c, ok := e.runner.(interface{ close() }); ok {
		c.close()
	}
	for i := len(e.onClose) - 1; i >= 0; i-- {
		e.onClose[i]()
	}
	e.onClose = nil
	if e.player != nil {
		return e.player.Close()
	}
	return nil
}

func (e *engine) noteTurnError(id string, err error) {
	for i := range e.turns {
		if e.turns[i].ID == id {
			e.turns[i].Err = err.Error()
			return
		}
	}
	e.turns = append(e.turns, turnRecord{ID: id, Err: err.Error()})
}

func (e *engine) runTurn(ctx context.Context, i int, turn Turn) (runErr error) {
	start := time.Now()
	latencyMark := len(e.rec.snapshot())
	var before work.Snapshot
	if wr, ok := e.runner.(interface{ workSnapshot() work.Snapshot }); ok {
		before = wr.workSnapshot()
	}
	defer func() { runErr = errors.Join(runErr, e.collectLatency(turn, start, latencyMark, before, runErr)) }()
	timeout := turn.Timeout.Duration
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if e.player != nil {
		turnDir := filepath.Join(e.runDir, "audio", fmt.Sprintf("%02d-%s", i+1, turn.ID))
		if err := e.player.BeginTurn(turnDir); err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, e.player.EndTurn()) }()
	}

	mark := len(e.rec.snapshot())
	routeMark := e.rec.routeMark()
	var selectionErr error
	var workAtSelection work.Snapshot
	if turn.Op == "select_persona" {
		if wr, ok := e.runner.(interface{ workSnapshot() work.Snapshot }); ok {
			workAtSelection = wr.workSnapshot()
		}
	}
	if turn.Op == "select_persona" {
		p, ok := e.runner.(personaRunner)
		if !ok {
			return fmt.Errorf("turn %s: runner does not support persona selection", turn.ID)
		}
		if turn.Persona == "" {
			return fmt.Errorf("turn %s: persona is required", turn.ID)
		}
		if err := p.selectPersona(turnCtx, turn.Persona); err != nil {
			if turn.ExpectError != "" && strings.Contains(err.Error(), turn.ExpectError) {
				selectionErr = err
			} else {
				return err
			}
		} else if turn.ExpectError != "" {
			return fmt.Errorf("turn %s: expected selection error containing %q", turn.ID, turn.ExpectError)
		}
	} else if turn.Op == "run_background" {
		bg, ok := e.runner.(backgroundRunner)
		if !ok {
			return fmt.Errorf("turn %s: runner does not support background work", turn.ID)
		}
		if err := bg.runBackground(turnCtx); err != nil {
			return err
		}
	} else if turn.Op == "compact" {
		recovery, ok := e.runner.(recoveryRunner)
		if !ok {
			return fmt.Errorf("turn %s: runner does not support compaction", turn.ID)
		}
		if err := recovery.compact(turnCtx); err != nil {
			return err
		}
	} else if turn.Op == "quiet_gap" {
		gap := turn.Duration.Duration
		if gap <= 0 {
			return fmt.Errorf("turn %s: quiet_gap requires a positive duration", turn.ID)
		}
		timer := time.NewTimer(gap)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-turnCtx.Done():
			return turnCtx.Err()
		}
	} else if turn.WAV != "" {
		replay, ok := e.runner.(interface {
			runWAV(context.Context, string) error
		})
		if !ok {
			return fmt.Errorf("runner does not support recorded speech")
		}
		if err := replay.runWAV(turnCtx, turn.WAV); err != nil {
			return err
		}
	} else if e.scn.Input == "speech" && turn.User != "" {
		return fmt.Errorf("speech input requires WAV")
	} else if turn.User != "" {
		if e.runner == nil {
			return fmt.Errorf("no runner (live agent not wired)")
		}
		if turn.InterruptAfter.Duration > 0 {
			control, ok := e.runner.(interface{ stopPlayback() })
			if !ok {
				return fmt.Errorf("turn %s: runner does not support playback interruption", turn.ID)
			}
			if err := runInterruptedTurn(turnCtx, e.rec, turn.InterruptAfter.Duration,
				func(ctx context.Context) error { return e.runner.runTurn(ctx, turn.User) }, control.stopPlayback); err != nil {
				return err
			}
		} else if err := e.runner.runTurn(turnCtx, turn.User); err != nil {
			return err
		}
	}
	wait := turn.Wait
	if wait == "" {
		wait = waitIdle
	}
	if wait == waitWorkDurable {
		bg, ok := e.runner.(backgroundRunner)
		if !ok {
			return fmt.Errorf("turn %s: runner does not support durable work wait", turn.ID)
		}
		if err := bg.waitWork(turnCtx, true); err != nil {
			return fmt.Errorf("wait %s: %w", wait, err)
		}
	} else if err := e.rec.waitFor(turnCtx, wait, e.isPlaying); err != nil {
		return fmt.Errorf("wait %s: %w", wait, err)
	}

	st := turnState{
		spoken: e.rec.spokenText(),
		events: e.rec.snapshot()[mark:],
	}
	for _, te := range st.events {
		if ev, ok := te.event.(events.ResponseReady); ok {
			st.response = ev.Response
		}
	}
	if d, ok := e.rec.lastRouteSince(routeMark); ok {
		st.route = &d
	}
	if e.runner != nil {
		st.history = e.runner.history()
		st.liveRouterPolicy = e.runner.livePolicy()
		st.workerRouterPolicy = e.runner.workerPolicy()
		st.workerToolsEnabled = e.runner.workerTools()
		st.liveBrainCalled = e.runner.liveCalled()
		st.workerCalled = e.runner.workerCalled()
	}
	if p, ok := e.runner.(personaRunner); ok {
		st.persona = p.personaState()
		st.persona.WorkAtSelection = workAtSelection
	}
	e.turns = append(e.turns, e.recordTurn(turn, mark, st.route))
	if selectionErr != nil {
		e.turns[len(e.turns)-1].Err = "expected error: " + selectionErr.Error()
	}
	for _, ex := range turn.Expect {
		if err := evalExpect(ex, e.rec, st); err != nil {
			return err
		}
	}
	return nil
}

func (e *engine) recordTurn(turn Turn, mark int, route *pipeline.RouteDecision) turnRecord {
	tr := turnRecord{ID: turn.ID, User: turn.User}
	if route != nil {
		tr.Route = string(route.Kind)
		if route.Source != "" {
			tr.Route += "/" + route.Source
		}
	}
	var spoken []string
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || (len(spoken) > 0 && spoken[len(spoken)-1] == text) {
			return
		}
		spoken = append(spoken, text)
	}
	for _, te := range e.rec.snapshot()[mark:] {
		switch v := te.event.(type) {
		case events.SpeechSegmentReady:
			add(v.Text)
		case events.GeneratingVoice:
			add(v.Sentence)
		}
	}
	tr.Spoken = clip(strings.Join(spoken, " "), 160)
	return tr
}

func (e *engine) isPlaying() bool {
	if e.playing != nil {
		return e.playing()
	}
	if e.player != nil {
		return e.player.IsPlaying()
	}
	if e.runner != nil {
		return e.runner.playing()
	}
	return false
}

func (e *engine) metrics() runMetrics {
	m := collectMetrics(e.rec)
	if e.player != nil {
		m.crackleKnown = true
		m.CrackleFailingWavs = e.player.failingCount()
		m.PlaybackWavs = e.player.scoredCount()
	}
	return m
}

func defaultRunDir(root, name string) (string, error) {
	if root == "" {
		cache := os.Getenv("OBEY_VOICE_CONVOFLOW_DIR")
		if cache == "" {
			base := os.Getenv("XDG_CACHE_HOME")
			if base == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return "", err
				}
				base = filepath.Join(home, ".cache")
			}
			cache = filepath.Join(base, "obey-voice", "convoflow")
		}
		root = cache
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	return filepath.Join(root, name+"-"+stamp), nil
}

// fakeRunner is the harness self-test: no models.
type fakeRunner struct {
	hist   []brain.Turn
	rec    *recorder
	onTurn func(user string)
	play   bool
	live   string
	worker string
	tools  bool
	liveOn bool
	workOn bool
}

func (f *fakeRunner) runTurn(_ context.Context, user string) error {
	if f.onTurn != nil {
		f.onTurn(user)
	}
	f.hist = append(f.hist, brain.Turn{Role: "user", Content: user})
	f.liveOn = true
	if f.rec != nil {
		f.rec.add(events.ResponseReady{Response: "ok"})
	}
	return nil
}
func (f *fakeRunner) history() []brain.Turn { return f.hist }
func (f *fakeRunner) playing() bool         { return f.play }
func (f *fakeRunner) livePolicy() string    { return f.live }
func (f *fakeRunner) workerPolicy() string  { return f.worker }
func (f *fakeRunner) workerTools() *bool {
	if !f.workOn {
		return nil
	}
	v := f.tools
	return &v
}
func (f *fakeRunner) liveCalled() bool   { return f.liveOn }
func (f *fakeRunner) workerCalled() bool { return f.workOn }
