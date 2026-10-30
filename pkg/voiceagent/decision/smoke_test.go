package decision

import (
	"context"
	"io"
	"testing"
	"time"
)

type smokeCloser struct{ countCloser }

func (*smokeCloser) RuntimeInfo() string { return "fixture · cpu" }

func TestSmokeIsLocalSyntheticAndGenerationFenced(t *testing.T) {
	var c Controller
	m := NewManager(&c)
	defer m.Close()
	entered := make(chan struct{})
	provider := evaluatorFunc(func(ctx context.Context, r Request) (Response, error) {
		if len(r.Questions) != 1 || r.Questions["positive"].Type != "choice" {
			t.Error("not the synthetic request")
		}
		close(entered)
		<-ctx.Done()
		return Response{Answers: map[string]Answer{"positive": {Choice: "yes"}}}, nil
	})
	m.Configure(Options{Budget: time.Second, VoiceWork: true}, func(context.Context) (Evaluator, io.Closer, error) { return provider, &smokeCloser{}, nil })
	waitPhase(t, m, "ready")
	done := make(chan error, 1)
	go func() { _, err := m.Test(t.Context()); done <- err }()
	<-entered
	if _, err := m.Test(t.Context()); err == nil {
		t.Fatal("overlapping test admitted")
	}
	m.Configure(Options{}, nil)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale success accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("test not canceled")
	}
}

func TestSmokeRejectsHostedAndMalformedAnswers(t *testing.T) {
	for _, local := range []bool{false, true} {
		var c Controller
		m := NewManager(&c)
		m.Configure(Options{Budget: time.Second, VoiceWork: true}, func(context.Context) (Evaluator, io.Closer, error) {
			var closer io.Closer
			if local {
				closer = &smokeCloser{}
			}
			return evaluatorFunc(func(context.Context, Request) (Response, error) {
				if !local {
					t.Error("hosted request sent")
				}
				return Response{}, nil
			}), closer, nil
		})
		waitPhase(t, m, "ready")
		if _, err := m.Test(t.Context()); err == nil {
			t.Fatal("invalid test passed")
		}
		m.Close()
	}
}
