package brain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"
	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// heldRequests makes the next streamed request send its first text and wait
// for hold to close, ignoring cancellation, as a CLI slow to stop does. It
// then fails, or completes when succeed is set.
type heldRequests struct {
	mu      sync.Mutex
	hold    chan struct{}
	succeed bool
	n       int
	threads []string
}

func (h *heldRequests) take() (chan struct{}, bool, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hold := h.hold
	h.hold = nil
	h.n++
	return hold, h.succeed, h.n
}

func (h *heldRequests) resumed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.threads)
}

type stallCodex struct{ *heldRequests }

func (f stallCodex) run(ctx context.Context, r codexRequest, emit func(string) error, _ StreamOptions) (codexResult, error) {
	if r.Ephemeral {
		return codexResult{}, emit(harnessSpawn)
	}
	f.mu.Lock()
	f.threads = append(f.threads, r.ThreadID)
	f.mu.Unlock()
	hold, succeed, n := f.take()
	thread := fmt.Sprintf("thread-%d", n)
	if hold == nil {
		return codexResult{ThreadID: thread}, emit("Noted.")
	}
	_ = emit("The vault code is")
	<-hold
	if !succeed {
		return codexResult{}, errors.New("stopped")
	}
	_ = emit(" cobalt.")
	return codexResult{ThreadID: thread}, nil
}

type stallMuse struct{ *heldRequests }

func (f stallMuse) RunPromptCtx(context.Context, string, *muse.RunOptions) (*muse.Result, error) {
	return &muse.Result{Terminal: "completed", Text: harnessSpawn}, nil
}

func (f stallMuse) StreamPrompt(context.Context, string, *muse.RunOptions) (<-chan muse.Event, <-chan error) {
	hold, succeed, _ := f.take()
	events, errs := make(chan muse.Event, 4), make(chan error, 1)
	go func() {
		defer close(errs)
		defer close(events)
		if hold == nil {
			events <- museEvent("run.output.delta", "Noted.")
			events <- museEvent("run.terminal.completed", "Noted.")
			return
		}
		events <- museEvent("run.output.delta", "The vault code is")
		<-hold
		if !succeed {
			errs <- errors.New("stopped")
			return
		}
		events <- museEvent("run.output.delta", " cobalt.")
		events <- museEvent("run.terminal.completed", "The vault code is cobalt.")
	}()
	return events, errs
}

func stallObey(h *heldRequests) obeyRunFunc {
	return func(_ context.Context, prompt string, _ *obey.RunOptions, emit func(obey.Event) error) (*obey.Result, error) {
		if strings.Contains(prompt, "latest_user_request") {
			return &obey.Result{Text: harnessSpawn}, nil
		}
		hold, succeed, _ := h.take()
		if hold == nil {
			return &obey.Result{Text: "Noted."}, nil
		}
		_ = emit(obey.Event{Delta: &obey.MessageDelta{ContentKind: "assistant", Text: "The vault code is"}})
		<-hold
		if !succeed {
			return nil, errors.New("stopped")
		}
		return &obey.Result{Text: "The vault code is cobalt."}, nil
	}
}

type cliHistoryBrain interface {
	Provider
	ContinuityTransferProvider
	ContextHistoryProvider
	ReseedContinuity([]Turn)
	StoppedRequestRecorder
	RequestAbandoner
	IdentityHistoryAppender
	LiveRouteClassifier
}

func stallCLIBrains(t *testing.T) map[string]func() (cliHistoryBrain, *heldRequests) {
	cfg := func() *config.Config {
		return &config.Config{AgentName: "Mira", ActivePersona: "mira", MaxHistory: 20, PromptsDir: t.TempDir(), ObeyToolPolicy: "daemon"}
	}
	return map[string]func() (cliHistoryBrain, *heldRequests){
		"codex": func() (cliHistoryBrain, *heldRequests) {
			h := &heldRequests{}
			return &CodexBrain{client: stallCodex{h}, cfg: cfg(), systemPrompt: "persona", turnInstruction: "turn"}, h
		},
		"muse-spark": func() (cliHistoryBrain, *heldRequests) {
			h := &heldRequests{}
			return &MuseSparkBrain{client: stallMuse{h}, cfg: cfg(), systemPrompt: "persona", turnInstruction: "turn"}, h
		},
		"obey": func() (cliHistoryBrain, *heldRequests) {
			h := &heldRequests{}
			return &ObeyBrain{client: stallObey(h), cfg: cfg(), systemPrompt: "persona", turnInstruction: "turn"}, h
		},
	}
}

var sayMoreIdentity = &TurnIdentity{PersonaID: "mira", ResponseID: "r2"}

// stoppedSayMore is how the host records the held request when it stopped it.
var stoppedSayMore = StoppedRequest{
	Line:  &Turn{Role: "user", Content: "Say more."},
	Reply: &Turn{Role: "assistant", Content: "The vault code is", Disposition: "interrupted", PersonaID: "mira", ResponseID: "r2"},
}

// startHeld starts "Say more." as a held request and reads its first text.
// release lets it end and returns its result.
func startHeld(t *testing.T, b cliHistoryBrain, h *heldRequests, succeed bool) (stop context.CancelFunc, release func() StreamResult) {
	t.Helper()
	hold := make(chan struct{})
	h.mu.Lock()
	h.hold, h.succeed = hold, succeed
	h.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	stream, err := b.ThinkStream(ctx, "Say more.", StreamOptions{Identity: sayMoreIdentity})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case first := <-stream.Chunks:
		if first != "The vault code is" {
			t.Fatalf("first text = %q", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held request sent nothing")
	}
	return cancel, func() StreamResult {
		close(hold)
		for range stream.Chunks {
		}
		return <-stream.Done
	}
}

func complete(t *testing.T, b cliHistoryBrain, input, responseID string) {
	t.Helper()
	stream, err := b.ThinkStream(t.Context(), input, StreamOptions{Identity: &TurnIdentity{PersonaID: "mira", ResponseID: responseID}})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	if result := <-stream.Done; result.Err != nil {
		t.Fatalf("%q: %v", input, result.Err)
	}
}

func lines(turns []Turn) []string {
	out := make([]string, 0, len(turns))
	for _, t := range turns {
		out = append(out, t.Role+": "+t.Content+" "+t.Disposition)
	}
	return out
}

func requireTurns(t *testing.T, name string, got, want []Turn) {
	t.Helper()
	if !slices.Equal(lines(got), lines(want)) {
		t.Fatalf("%s = %q, want %q", name, lines(got), lines(want))
	}
}

var (
	priorTurns = []Turn{{Role: "user", Content: "Hello."}, {Role: "assistant", Content: "Hi.", PersonaID: "mira", ResponseID: "r1"}}
	otherTurns = []Turn{{Role: "user", Content: "Unrelated question."}, {Role: "assistant", Content: "Unrelated answer.", PersonaID: "ember", ResponseID: "e1"}}
)

// A request that ends after the conversation was cleared, loaded or replaced
// on a persona switch changes nothing, whether it ends stopped and the host
// then records it, or completes.
func TestLateEndOfARequestLeavesAReplacedConversationAlone(t *testing.T) {
	resets := map[string]struct {
		apply              func(cliHistoryBrain)
		canonical, working []Turn
	}{
		"clear":  {func(b cliHistoryBrain) { b.ClearHistory() }, nil, nil},
		"load":   {func(b cliHistoryBrain) { b.LoadHistory(otherTurns) }, otherTurns, otherTurns},
		"import": {func(b cliHistoryBrain) { _ = b.ImportContinuity(ContinuitySnapshot{Canonical: otherTurns}) }, otherTurns, otherTurns},
		"reseed": {func(b cliHistoryBrain) { b.ReseedContinuity(otherTurns[1:]) }, priorTurns, otherTurns[1:]},
	}
	for provider, newBrain := range stallCLIBrains(t) {
		for name, reset := range resets {
			for _, stopped := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/stopped=%v", provider, name, stopped), func(t *testing.T) {
					b, h := newBrain()
					b.LoadHistory(priorTurns)
					stop, release := startHeld(t, b, h, !stopped)
					if stopped {
						stop()
					}
					reset.apply(b)
					release()
					if stopped {
						b.RecordStoppedRequest(stoppedSayMore)
					}
					requireTurns(t, "history", b.History(), reset.canonical)
					requireTurns(t, "model view", b.ContextHistory(), reset.working)
				})
			}
		}
	}
}

// A stopped request is recorded as the turn read it in place of the provider's
// own record, but never in place of a line recorded after it, such as an idle
// job result.
func TestStoppedRequestKeepsALineRecordedAfterIt(t *testing.T) {
	stoppedLines := append(slices.Clone(priorTurns), *stoppedSayMore.Line, *stoppedSayMore.Reply)
	for provider, newBrain := range stallCLIBrains(t) {
		for _, completed := range []bool{false, true} {
			for _, job := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/completed=%v/job=%v", provider, completed, job), func(t *testing.T) {
					b, h := newBrain()
					b.LoadHistory(priorTurns)
					stop, release := startHeld(t, b, h, completed)
					if !completed {
						stop()
					}
					release()
					want := stoppedLines
					if job {
						if err := b.AppendAssistantResponseWithIdentity("The report is ready.", TurnIdentity{PersonaID: "mira", ResponseID: "job-1"}); err != nil {
							t.Fatal(err)
						}
						want = b.History()
					}
					b.RecordStoppedRequest(stoppedSayMore)
					requireTurns(t, "history", b.History(), want)
					requireTurns(t, "model view", b.ContextHistory(), want)
				})
			}
		}
	}
}

// A request a route answered before the provider saw it gets its line back
// when the turn did not follow the route, after the exchange before it.
func TestUnfollowedRouteKeepsTheExchangeBeforeIt(t *testing.T) {
	for provider, newBrain := range stallCLIBrains(t) {
		t.Run(provider, func(t *testing.T) {
			b, _ := newBrain()
			complete(t, b, "Hello.", "r1")
			if r, err := b.ClassifyLiveRoute(t.Context(), LiveRouteRequest{Text: "Build me a site.", WorkerTiers: &WorkerTierAvailability{Simple: true}}); err != nil || r.Route != "spawn" {
				t.Fatalf("route = %+v, err %v", r, err)
			}
			line := Turn{Role: "user", Content: "Build me a site."}
			b.RecordStoppedRequest(StoppedRequest{Line: &line})
			requireTurns(t, "history", b.History(), []Turn{{Role: "user", Content: "Hello."}, {Role: "assistant", Content: "Noted."}, line})
		})
	}
}

// A request the host abandoned records nothing when it ends, even if it
// completes.
func TestAbandonedRequestRecordsNothingWhenItEnds(t *testing.T) {
	for provider, newBrain := range stallCLIBrains(t) {
		t.Run(provider, func(t *testing.T) {
			b, h := newBrain()
			b.LoadHistory(priorTurns)
			_, release := startHeld(t, b, h, true)
			b.AbandonRequest()
			release()
			b.RecordStoppedRequest(stoppedSayMore)
			requireTurns(t, "history", b.History(), priorTurns)
		})
	}
}

// The next request after an abandoned Codex request starts a thread of its
// own, which the abandoned request does not drop when it ends.
func TestAbandonedCodexRequestKeepsItsThreadToItself(t *testing.T) {
	b, h := stallCLIBrains(t)["codex"]()
	complete(t, b, "Hello.", "r1")
	stop, release := startHeld(t, b, h, false)
	stop()
	b.AbandonRequest()
	complete(t, b, "Go on.", "r3")
	release()
	complete(t, b, "And now?", "r4")
	if got, want := h.resumed(), []string{"", "thread-1", "", "thread-3"}; !slices.Equal(got, want) {
		t.Fatalf("threads resumed = %q, want %q", got, want)
	}
	requireTurns(t, "history", b.History(), []Turn{
		{Role: "user", Content: "Hello."}, {Role: "assistant", Content: "Noted."},
		{Role: "user", Content: "Go on."}, {Role: "assistant", Content: "Noted."},
		{Role: "user", Content: "And now?"}, {Role: "assistant", Content: "Noted."},
	})
}
