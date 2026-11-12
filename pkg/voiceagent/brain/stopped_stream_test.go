package brain

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
)

// resumeLog records the session each request asked the CLI to resume.
type resumeLog struct {
	mu  sync.Mutex
	ids []string
}

func (l *resumeLog) add(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ids = append(l.ids, id)
}

func (l *resumeLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ids) == 0 {
		return ""
	}
	return l.ids[len(l.ids)-1]
}

// heldClaudeRunner answers its first request, then holds the stream open
// once it is stopped, until release closes, and only then sends a session id,
// usage and text of its own: the SDK's view of a CLI that ends late. Later
// requests answer at once in session sess-2.
type heldClaudeRunner struct {
	fakeClaudeClient
	release chan struct{}
	calls   atomic.Int32
	resumes resumeLog
}

func (r *heldClaudeRunner) StreamPrompt(ctx context.Context, _ string, opts *claude.RunOptions) (<-chan claude.Message, <-chan error) {
	r.resumes.add(opts.ResumeID)
	msgs, errs := make(chan claude.Message, 2), make(chan error)
	if r.calls.Add(1) > 1 {
		msgs <- assistantMsgUsage("Second answer.", 100, 0, 0, 5)
		msgs <- resultMsg("sess-2")
		close(msgs)
		close(errs)
		return msgs, errs
	}
	msgs <- assistantMsg("The vault code is cobalt, and the door is teal.")
	msgs <- resultMsg("sess-1")
	go func() {
		<-ctx.Done()
		<-r.release
		msgs <- resultMsg("late-session")
		msgs <- assistantMsgUsage("It is cobalt.", 900000, 0, 0, 5)
		close(msgs)
		close(errs)
	}()
	return msgs, errs
}

// heldGrokRunner is heldClaudeRunner for Grok, whose SDK reports nothing once
// its request is stopped.
type heldGrokRunner struct {
	fakeGrokClient
	release chan struct{}
	calls   atomic.Int32
	resumes resumeLog
}

func (r *heldGrokRunner) StreamPrompt(ctx context.Context, _ string, opts *grok.RunOptions) (<-chan grok.Event, <-chan error) {
	r.resumes.add(opts.ResumeID)
	evs, errs := make(chan grok.Event, 1), make(chan error)
	if r.calls.Add(1) > 1 {
		evs <- grokTextEvent("Second answer.", "sess-2")
		close(evs)
		close(errs)
		return evs, errs
	}
	evs <- grokTextEvent("The vault code is cobalt, and the door is teal.", "sess-1")
	go func() {
		<-ctx.Done()
		<-r.release
		evs <- grokTextEvent("It is cobalt.", "late-session")
		close(evs)
		close(errs)
	}()
	return evs, errs
}

// heldCursorRunner is heldClaudeRunner for Cursor's print mode: the stopped
// request answers nothing until release closes, then reports success.
type heldCursorRunner struct {
	release, held chan struct{}
	calls         atomic.Int32
	resumes       resumeLog
}

func (r *heldCursorRunner) AskCtx(ctx context.Context, _ string, opts *cursor.AskOptions) (*cursor.AskResult, error) {
	r.resumes.add(opts.Resume)
	if r.calls.Add(1) > 1 {
		return &cursor.AskResult{Result: "Second answer.", SessionID: "sess-2", Usage: cursor.Usage{InputTokens: 100}}, nil
	}
	close(r.held)
	<-ctx.Done()
	<-r.release
	return &cursor.AskResult{Result: "It is cobalt.", SessionID: "late-session", Usage: cursor.Usage{InputTokens: 900000}}, nil
}

type sessionProvider interface {
	Provider
	SessionInfo() SessionState
}

// A request stopped by its caller may finish on the provider while the next
// request is already running on it. Whatever it reports after the stop, it
// must leave the provider's history and session alone, and report the stop.
// The unordered case lets the stopped request end while the next one runs,
// for the race detector; the ordered case ends it after the next one, so a
// late session id would be the last one written.
func TestStoppedStreamLeavesTheProviderAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens bool
		// start returns the provider, its requests' resume ids, and a wait
		// for the request that will be stopped to reach the CLI.
		start func(release chan struct{}) (sessionProvider, *resumeLog, func(*Stream))
	}{
		{name: "claude", tokens: true, start: func(release chan struct{}) (sessionProvider, *resumeLog, func(*Stream)) {
			r := &heldClaudeRunner{release: release}
			return newTestBrain(r), &r.resumes, func(s *Stream) { <-s.Chunks }
		}},
		{name: "grok", start: func(release chan struct{}) (sessionProvider, *resumeLog, func(*Stream)) {
			r := &heldGrokRunner{release: release}
			return newTestGrokBrain(r), &r.resumes, func(s *Stream) { <-s.Chunks }
		}},
		{name: "cursor", tokens: true, start: func(release chan struct{}) (sessionProvider, *resumeLog, func(*Stream)) {
			r := &heldCursorRunner{release: release, held: make(chan struct{})}
			return newTestCursorBrain(r), &r.resumes, func(*Stream) { <-r.held }
		}},
	} {
		for _, ordered := range []bool{false, true} {
			name := tc.name + "/ends while the next request runs"
			if ordered {
				name = tc.name + "/ends after the next request"
			}
			t.Run(name, func(t *testing.T) {
				release := make(chan struct{})
				p, resumes, started := tc.start(release)
				ctx, stop := context.WithCancel(t.Context())
				first, err := p.ThinkStream(ctx, "What is the vault code?", StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				started(first)
				stop()

				second, err := p.ThinkStream(t.Context(), "Try again.", StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if !ordered {
					close(release)
				}
				if text, result := collectStream(t, second); text != "Second answer." || result.Err != nil {
					t.Fatalf("second request = %q, %+v", text, result)
				}
				if ordered {
					close(release)
				}
				for range first.Chunks {
				}
				if result := <-first.Done; !errors.Is(result.Err, context.Canceled) {
					t.Fatalf("the stopped request reported %+v", result)
				}

				history := p.History()
				if len(history) != 3 || history[0].Content != "What is the vault code?" || history[1].Content != "Try again." || history[2].Content != "Second answer." {
					t.Fatalf("history = %+v", history)
				}
				info := p.SessionInfo()
				if info.ID != "sess-2" || (tc.tokens && info.PromptTokens != 100) {
					t.Fatalf("session = %+v, want sess-2 as the second request left it", info)
				}
				next, err := p.ThinkStream(t.Context(), "And now?", StreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
				collectStream(t, next)
				if got := resumes.last(); got != "sess-2" {
					t.Fatalf("the next request resumed %q, want sess-2", got)
				}
			})
		}
	}
}
