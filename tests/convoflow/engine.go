package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
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

type engine struct {
	scn     *Scenario
	runDir  string
	rec     *recorder
	runner  runner
	playing func() bool
}

func (e *engine) run(ctx context.Context) error {
	if err := os.MkdirAll(e.runDir, 0o700); err != nil {
		return err
	}
	for _, turn := range e.scn.Turns {
		if err := e.runTurn(ctx, turn); err != nil {
			return fmt.Errorf("turn %s: %w", turn.ID, err)
		}
	}
	m := collectMetrics(e.rec)
	for _, ex := range e.scn.Expect {
		if err := evalRunExpect(ex, e.rec, m); err != nil {
			return fmt.Errorf("run expect: %w", err)
		}
	}
	return writeReport(e)
}

func (e *engine) runTurn(ctx context.Context, turn Turn) error {
	timeout := turn.Timeout.Duration
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mark := len(e.rec.snapshot())
	if turn.User != "" {
		if e.runner == nil {
			return fmt.Errorf("no runner (live agent not wired)")
		}
		if err := e.runner.runTurn(turnCtx, turn.User); err != nil {
			return err
		}
	}
	wait := turn.Wait
	if wait == "" {
		wait = waitIdle
	}
	playing := e.playing
	if playing == nil && e.runner != nil {
		playing = e.runner.playing
	}
	if err := e.rec.waitFor(turnCtx, wait, playing); err != nil {
		return fmt.Errorf("wait %s: %w", wait, err)
	}

	st := turnState{
		spoken: e.rec.spokenText(),
		events: e.rec.snapshot()[mark:],
	}
	if e.runner != nil {
		st.history = e.runner.history()
		st.liveRouterPolicy = e.runner.livePolicy()
		st.workerRouterPolicy = e.runner.workerPolicy()
		st.workerToolsEnabled = e.runner.workerTools()
		st.liveBrainCalled = e.runner.liveCalled()
		st.workerCalled = e.runner.workerCalled()
	}
	for _, ex := range turn.Expect {
		if err := evalExpect(ex, e.rec, st); err != nil {
			return err
		}
	}
	return nil
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
	routes []pipeline.RouteDecision
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
