package brain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type fakeMuse struct {
	events []muse.Event
	result *muse.Result
	err    error
	prompt string
	opts   *muse.RunOptions
}

func (f *fakeMuse) RunPromptCtx(ctx context.Context, prompt string, o *muse.RunOptions) (*muse.Result, error) {
	f.prompt, f.opts = prompt, o
	return f.result, f.err
}
func (f *fakeMuse) StreamPrompt(ctx context.Context, prompt string, o *muse.RunOptions) (<-chan muse.Event, <-chan error) {
	f.prompt, f.opts = prompt, o
	events, errs := make(chan muse.Event, len(f.events)), make(chan error, 1)
	for _, e := range f.events {
		events <- e
	}
	close(events)
	if f.err != nil {
		errs <- f.err
	}
	close(errs)
	return events, errs
}
func museEvent(kind, text string) muse.Event {
	p := map[string]string{"text": text}
	if strings.HasPrefix(kind, "run.terminal.") {
		p["terminal"] = strings.TrimPrefix(kind, "run.terminal.")
	}
	raw, _ := json.Marshal(p)
	return muse.Event{Type: kind, Payload: raw}
}
func testMuse(f museRunner) *MuseSparkBrain {
	return &MuseSparkBrain{client: f, cfg: &config.Config{AgentName: "Obey", MaxHistory: 10, MuseSparkModel: "test-model"}, systemPrompt: "persona marker", turnInstruction: "turn marker"}
}
func collectMuse(t *testing.T, b *MuseSparkBrain, o StreamOptions) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := b.ThinkStream(ctx, "hello", o)
	if err != nil {
		return "", err
	}
	if stream.Thinking != nil {
		t.Fatal("reasoning must not enter speech stream")
	}
	var out strings.Builder
	for chunk := range stream.Chunks {
		out.WriteString(chunk)
	}
	return out.String(), (<-stream.Done).Err
}
func TestMuseSparkStreamsOnlyOutput(t *testing.T) {
	f := &fakeMuse{events: []muse.Event{
		museEvent("task.reasoning.delta", "private reasoning"), museEvent("run.output.delta", "Obey: Hello "),
		museEvent("task.lifecycle.failed", "internal failure"), museEvent("run.output.delta", "there."), museEvent("run.terminal.completed", "Hello there."),
	}}
	b := testMuse(f)
	identity := &TurnIdentity{PersonaID: "obey", PersonaDisplayName: "Obey", ResponseID: "r1"}
	text, err := collectMuse(t, b, StreamOptions{Identity: identity, OriginalInput: "original hello"})
	if err != nil || text != "Hello there." {
		t.Fatalf("text=%q err=%v", text, err)
	}
	history := b.History()
	if len(history) != 2 || history[0].Content != "original hello" || history[1].ResponseID != "r1" {
		t.Fatalf("history=%+v", history)
	}
	if f.opts.Model != "test-model" || f.opts.Preset != "empty" || !f.opts.DisableShell || !f.opts.NoSessionLog || f.opts.SessionID != "" {
		t.Fatalf("options=%+v", f.opts)
	}
}
func TestMuseSparkFailuresDoNotCommitHistory(t *testing.T) {
	for _, terminal := range []string{"failed", "cancelled", ""} {
		t.Run(terminal, func(t *testing.T) {
			f := &fakeMuse{result: &muse.Result{Terminal: terminal, Text: "failure text"}}
			if terminal != "" {
				f.events = []muse.Event{museEvent("run.terminal."+terminal, "failure text")}
			}
			b := testMuse(f)
			b.LoadHistory([]Turn{{Role: "user", Content: "keep me"}})
			if _, err := b.ThinkFull(context.Background(), "bad", StreamOptions{}); err == nil {
				t.Fatal("full succeeded")
			}
			text, err := collectMuse(t, b, StreamOptions{})
			if err == nil || text != "" || len(b.History()) != 1 {
				t.Fatalf("text=%q err=%v history=%v", text, err, b.History())
			}
		})
	}
	f := &fakeMuse{err: context.Canceled}
	b := testMuse(f)
	if _, err := collectMuse(t, b, StreamOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if len(b.History()) != 0 {
		t.Fatal("cancel committed history")
	}
}
func TestMuseSparkHistoryLifecycle(t *testing.T) {
	f := &fakeMuse{result: &muse.Result{Terminal: "completed", Text: "Hi."}, events: []muse.Event{museEvent("run.terminal.completed", "Hi.")}}
	b := testMuse(f)
	b.LoadHistory([]Turn{{Role: "user", Content: "remember cobalt"}, {Role: "assistant", Content: "remembered"}})
	if _, err := b.ThinkFull(context.Background(), "recall", StreamOptions{OmitTurnInstruction: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.prompt, "remember cobalt") || !strings.Contains(f.prompt, "persona marker") || strings.Contains(f.prompt, "turn marker") {
		t.Fatal(f.prompt)
	}
	before := len(b.History())
	if _, err := collectMuse(t, b, StreamOptions{TransientHistory: true}); err != nil {
		t.Fatal(err)
	}
	if len(b.History()) != before {
		t.Fatal("transient turn persisted")
	}
	if _, err := b.ThinkFull(context.Background(), "continue", StreamOptions{AgentContinuation: true}); err != nil {
		t.Fatal(err)
	}
	if len(b.History()) != before+1 {
		t.Fatal("continuation fabricated user")
	}
	fresh := b.Fresh().(*MuseSparkBrain)
	if len(fresh.History()) != 0 {
		t.Fatal("fresh inherited history")
	}
	b.ClearHistory()
	if len(b.History()) != 0 {
		t.Fatal("clear failed")
	}
	if _, err := b.ThinkFull(context.Background(), "new", StreamOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.prompt, "remember cobalt") {
		t.Fatal("clear leaked previous history")
	}
}
func TestMuseSparkToolGateAndBatch(t *testing.T) {
	cfg := &config.Config{MuseSparkModel: "custom"}
	for _, o := range []StreamOptions{{}, {ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}} {
		opts := museRunOptions(cfg, liveCLITools(o))
		if opts.Preset != "empty" || !opts.DisableWrite || !opts.DisableWebTools || !opts.DisableShell {
			t.Fatal(opts)
		}
	}
	opts := museRunOptions(cfg, true)
	if opts.Preset != "" || opts.DisableSandbox || opts.ApprovalMode != "never" {
		t.Fatal(opts)
	}
	f := &fakeMuse{result: &muse.Result{Terminal: "completed", Text: " transformed "}}
	batch := &museSparkBatch{client: f, cfg: cfg}
	result, err := batch.Transform(context.Background(), BatchRequest{SystemPrompt: "system", Text: "input"})
	if err != nil || result.Text != "transformed" || result.Model != "custom" || !strings.Contains(f.prompt, "system") || f.opts.Preset != "empty" {
		t.Fatalf("%+v %v %s", result, err, f.prompt)
	}
}

// This opt-in exercises the real SDK subprocess and native event wire format
// without model credentials, personal settings, or persistent session files.
func TestMuseSparkNativeEcho(t *testing.T) {
	if os.Getenv("OBEY_VOICE_MUSE_ECHO") != "1" {
		t.Skip("set OBEY_VOICE_MUSE_ECHO=1 for the installed Muse binary")
	}
	bin, err := exec.LookPath("muse")
	if err != nil {
		t.Fatal(err)
	}
	c := muse.NewClient(bin)
	c.WorkingDir = t.TempDir()
	c.Env = []string{"XDG_CONFIG_HOME=" + t.TempDir(), "XDG_DATA_HOME=" + t.TempDir(), "MUSE_NO_AUTO_UPDATE=1"}
	b := testMuse(echoMuse{c})
	b.cfg.MuseSparkModel = ""
	text, err := collectMuse(t, b, StreamOptions{})
	if err != nil || !strings.Contains(text, "hello") || len(b.History()) != 2 {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

type echoMuse struct{ *muse.Client }

func (e echoMuse) StreamPrompt(ctx context.Context, p string, o *muse.RunOptions) (<-chan muse.Event, <-chan error) {
	o.Provider = "echo"
	return e.Client.StreamPrompt(ctx, p, o)
}

type cancelledMuse struct{ started chan struct{} }

func (f cancelledMuse) RunPromptCtx(ctx context.Context, _ string, _ *muse.RunOptions) (*muse.Result, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (f cancelledMuse) StreamPrompt(ctx context.Context, _ string, _ *muse.RunOptions) (<-chan muse.Event, <-chan error) {
	events, errs := make(chan muse.Event), make(chan error, 1)
	go func() { defer close(events); defer close(errs); close(f.started); <-ctx.Done(); errs <- ctx.Err() }()
	return events, errs
}
func TestMuseSparkActiveCancellation(t *testing.T) {
	f := cancelledMuse{started: make(chan struct{})}
	b := testMuse(f)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := b.ThinkStream(ctx, "cancel me", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.started:
	case <-ctx.Done():
		t.Fatal("runner did not start")
	}
	cancel()
	for range stream.Chunks {
	}
	select {
	case result := <-stream.Done:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatal(result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not finish")
	}
	if len(b.History()) != 0 {
		t.Fatal("cancelled turn committed")
	}
}
func TestMuseSparkDefaultModelIsOmitted(t *testing.T) {
	if opts := museRunOptions(&config.Config{MuseSparkModel: "default"}, false); opts.Model != "" {
		t.Fatalf("model=%q", opts.Model)
	}
}
