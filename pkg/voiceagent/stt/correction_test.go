package stt

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt/correct"
)

type fakeSession struct {
	events chan Event
	once   sync.Once
}

func (s *fakeSession) Events() <-chan Event { return s.events }
func (s *fakeSession) Close() error {
	s.once.Do(func() { close(s.events) })
	return nil
}

type fakeProvider struct {
	session  *fakeSession
	startErr error
}

func (p *fakeProvider) Start(context.Context) (Session, error) {
	if p.startErr != nil {
		return nil, p.startErr
	}
	return p.session, nil
}
func (p *fakeProvider) Available() bool { return true }

type funcCorrector func(ctx context.Context, text string) (string, error)

func (f funcCorrector) Correct(ctx context.Context, text string) (string, error) { return f(ctx, text) }

func upper(_ context.Context, text string) (string, error) { return strings.ToUpper(text), nil }

func collect(t *testing.T, s Session) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("events channel did not close; got %v", out)
		}
	}
}

func TestWithCorrectionRewritesFinalTranscriptsOnly(t *testing.T) {
	inner := &fakeSession{events: make(chan Event, 8)}
	inner.events <- PartialTranscript{Text: "veronika"}
	inner.events <- InputLevel{Level: 0.5}
	inner.events <- FinalTranscript{Text: "tell veronika", AcousticName: "Obey"}
	inner.events <- Timeout{}
	inner.Close()

	p := WithCorrection(&fakeProvider{session: inner}, funcCorrector(upper), nil)
	s, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, s)
	want := []Event{PartialTranscript{Text: "veronika"}, InputLevel{Level: 0.5}, FinalTranscript{Text: "TELL VERONIKA", AcousticName: "Obey"}, Timeout{}}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestWithCorrectionFailsOpen(t *testing.T) {
	inner := &fakeSession{events: make(chan Event, 8)}
	inner.events <- FinalTranscript{Text: "raw"}
	inner.Close()
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, strings.TrimSpace(f)) }
	boom := funcCorrector(func(context.Context, string) (string, error) { return "", errors.New("boom") })

	s, err := WithCorrection(&fakeProvider{session: inner}, boom, logf).Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, s)
	if len(got) != 1 || got[0] != (FinalTranscript{Text: "raw"}) {
		t.Fatalf("events = %v", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "stt correction skipped") {
		t.Fatalf("logged = %q", logged)
	}
}

func TestWithCorrectionCancelledContextForwardsRaw(t *testing.T) {
	inner := &fakeSession{events: make(chan Event, 8)}
	inner.events <- FinalTranscript{Text: "veronika"}
	inner.Close()
	cw := correct.NewCustomWords([]correct.Term{{Text: "Veronica", Fuzzy: true}}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s, err := WithCorrection(&fakeProvider{session: inner}, correct.New(cw), nil).Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := collect(t, s); len(got) != 1 || got[0] != (FinalTranscript{Text: "veronika"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestWithCorrectionSkipsEmptyFinal(t *testing.T) {
	inner := &fakeSession{events: make(chan Event, 8)}
	inner.events <- FinalTranscript{}
	inner.Close()
	called := false
	c := funcCorrector(func(_ context.Context, text string) (string, error) { called = true; return text, nil })
	s, err := WithCorrection(&fakeProvider{session: inner}, c, nil).Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	collect(t, s)
	if called {
		t.Fatal("corrector ran on an empty transcript")
	}
}

func TestWithCorrectionCloseStopsAnUnreadSession(t *testing.T) {
	inner := &fakeSession{events: make(chan Event, 32)}
	for i := 0; i < 32; i++ {
		inner.events <- FinalTranscript{Text: "x"}
	}
	s, err := WithCorrection(&fakeProvider{session: inner}, funcCorrector(upper), nil).Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	collect(t, s) // fails the test if the forwarder never closes its channel
}

func TestWithCorrectionStartError(t *testing.T) {
	boom := errors.New("no mic")
	_, err := WithCorrection(&fakeProvider{startErr: boom}, funcCorrector(upper), nil).Start(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestWithCorrectionNilArguments(t *testing.T) {
	p := &fakeProvider{}
	if got := WithCorrection(p, nil, nil); got != Provider(p) {
		t.Fatal("nil corrector should return the provider unchanged")
	}
	if got := WithCorrection(nil, funcCorrector(upper), nil); got != nil {
		t.Fatal("nil provider should stay nil")
	}
}
