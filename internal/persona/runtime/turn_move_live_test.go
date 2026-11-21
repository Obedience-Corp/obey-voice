package personaruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// tokenLog passes chat requests through to Ollama and records when each
// streamed line arrived, per model.
type tokenLog struct {
	mu       sync.Mutex
	lines    map[string][]time.Time
	requests []string
}

func (l *tokenLog) note(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, time.Now().Format("15:04:05.000")+" "+fmt.Sprintf(format, args...))
}

func (l *tokenLog) serve(t *testing.T, upstream string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req struct{ Model string }
		_ = json.Unmarshal(body, &req)
		l.note("%s %s model=%q sent", r.Method, r.URL.Path, req.Model)
		lines := 0
		defer func() {
			l.note("%s %s model=%q ended after %d lines (%v)", r.Method, r.URL.Path, req.Model, lines, r.Context().Err())
		}()
		out, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(out)
		if err != nil {
			l.note("%s %s model=%q failed: %v", r.Method, r.URL.Path, req.Model, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		l.note("%s %s model=%q answered %s", r.Method, r.URL.Path, req.Model, resp.Status)
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				lines++
				if r.URL.Path == "/api/chat" {
					l.mu.Lock()
					l.lines[req.Model] = append(l.lines[req.Model], time.Now())
					l.mu.Unlock()
				}
				_, _ = w.Write(line)
				w.(http.Flusher).Flush()
			}
			if err != nil {
				return
			}
		}
	})
}

func (l *tokenLog) between(model string, from, to time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, at := range l.lines[model] {
		if at.After(from) && at.Before(to) {
			n++
		}
	}
	return n
}

func (l *tokenLog) span(model string) (time.Time, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := l.lines[model]
	if len(lines) == 0 {
		return time.Time{}, time.Time{}
	}
	return lines[0], lines[len(lines)-1]
}

// Against installed local models: Anika is asked for a long answer, and a
// typed "Hey Obey, …" arrives while she is still writing it. Obey answers
// while her request is open; her reply is delivered later under her name,
// once, after his in the shared history. Nothing is captured or played.
func TestLiveTypedMessageToAnotherPersonaWhileOneIsWriting(t *testing.T) {
	modelA, modelB := os.Getenv("OBEY_VOICE_LIVE_MOVE_MODEL_A"), os.Getenv("OBEY_VOICE_LIVE_MOVE_MODEL_B")
	if modelA == "" || modelB == "" {
		t.Skip("set OBEY_VOICE_LIVE_MOVE_MODEL_A and OBEY_VOICE_LIVE_MOVE_MODEL_B to installed Ollama models")
	}
	numCtx := 32768
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	tokens := &tokenLog{lines: map[string][]time.Time{}}
	host := httptest.NewServer(tokens.serve(t, "http://localhost:11434"))
	defer host.Close()

	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	prompts := filepath.Join(root, "prompts")
	for _, entry := range []struct{ id, name, model string }{{"anika", "Anika", modelA}, {"obey", "Obey", modelB}} {
		p := testProfile(entry.id, entry.name, entry.id)
		p.Brain.Model = entry.model
		writeTestProfile(t, catalog.Dir, p)
		writeTestPrompt(t, prompts, entry.id, "You are {agent_name}. Speak only as yourself.")
	}
	base := &config.Config{ActivePersona: "anika", BrainProvider: "ollama", OllamaModel: modelA, OllamaHost: host.URL, PromptsDir: prompts, WorkDir: root, MaxHistory: 30, OllamaNumCtx: numCtx}
	binding, err := catalog.ResolveBinding(base, "anika")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := brain.NewProvider(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline.Pipeline{Brain: provider, Events: events.NewBus()}
	t.Cleanup(func() {
		if err := p.CloseBrains(); err != nil {
			t.Error(err)
		}
	})
	if err := p.BeginConversationIdentity("live-move"); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", modelA)
	selector, err := NewSelector(SelectionOptions{Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: binding,
		PersistContexts: func(turns []brain.Turn, s events.PersonaSelection, contexts brain.PersonaContexts) error {
			return store.SaveSelectionState(sess, turns, s, contexts)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(selector.Close)
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(p, jobs, nil, base, nil).Wire()
	p.AdmitInput = selector.Route
	p.NamesOtherPersona = selector.NamesOtherPersona

	var mu sync.Mutex
	var aStart, bFirst, bDone, aEnd time.Time
	var aRead strings.Builder
	var bReply string
	finished := make(chan events.JobFinished, 1)
	events.Subscribe(p.Events, func(e events.ThinkingStarted) {
		mu.Lock()
		defer mu.Unlock()
		if e.Envelope != nil && e.Envelope.PersonaID == "anika" && aStart.IsZero() {
			aStart = time.Now()
		}
	})
	events.Subscribe(p.Events, func(e events.ResponseStreamingStarted) {
		mu.Lock()
		defer mu.Unlock()
		if e.Envelope != nil && e.Envelope.PersonaID == "obey" && bFirst.IsZero() {
			bFirst = time.Now()
		}
	})
	events.Subscribe(p.Events, func(e events.ResponseDelta) {
		mu.Lock()
		defer mu.Unlock()
		if e.Envelope != nil && e.Envelope.PersonaID == "anika" {
			aRead.WriteString(e.Text)
		}
	})
	events.Subscribe(p.Events, func(e events.ResponseReady) {
		mu.Lock()
		defer mu.Unlock()
		if e.Envelope != nil && e.Envelope.PersonaID == "obey" && !e.Interrupted {
			bDone, bReply = time.Now(), e.Response
		}
	})
	events.Subscribe(p.Events, func(e events.JobFinished) {
		mu.Lock()
		aEnd = time.Now()
		mu.Unlock()
		finished <- e
	})
	events.Subscribe(p.Events, func(e events.JobFailed) { t.Errorf("moved job failed: %s", e.Err) })

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	turn := make(chan error, 1)
	go func() {
		turn <- p.RunTurnTextMode(ctx, "Anika, write me a detailed essay of about six hundred words on the history of lighthouses, in plain paragraphs.")
	}()
	for {
		mu.Lock()
		read := aRead.Len()
		mu.Unlock()
		if read >= 200 {
			break
		}
		select {
		case err := <-turn:
			t.Fatalf("Anika's turn ended before the move: %v", err)
		case <-ctx.Done():
			t.Fatal("Anika's reply never started")
		case <-time.After(50 * time.Millisecond):
		}
	}
	moveAt := time.Now()
	go func() {
		select {
		case <-time.After(60 * time.Second):
		case <-ctx.Done():
			return
		}
		mu.Lock()
		waiting := bFirst.IsZero()
		mu.Unlock()
		if dump := os.Getenv("OBEY_VOICE_LIVE_MOVE_DUMP"); waiting && dump != "" {
			if f, err := os.Create(dump); err == nil {
				_ = pprof.Lookup("goroutine").WriteTo(f, 2)
				_ = f.Close()
			}
		}
	}()
	if !p.MoveTurnToBackground("Hey Obey, what is two plus two?") {
		t.Fatal("the running turn was not moved")
	}
	if err := <-turn; err != nil {
		t.Fatalf("moved turn returned %v", err)
	}
	if err := p.RunTurnTextMode(ctx, "Hey Obey, what is two plus two?"); err != nil {
		t.Fatal(err)
	}
	var result events.JobFinished
	select {
	case result = <-finished:
	case <-ctx.Done():
		t.Fatal("Anika's moved reply never finished")
	}
	if err := p.RunBackgroundTurn(ctx); err != nil {
		t.Fatal(err)
	}

	tokens.mu.Lock()
	for _, line := range tokens.requests {
		t.Logf("ollama %s", line)
	}
	tokens.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	aFirstLine, aLastLine := tokens.span(modelA)
	bFirstLine, bLastLine := tokens.span(modelB)
	at := func(when time.Time) string { return when.Sub(aStart).Round(time.Millisecond).String() }
	t.Logf("A request start      %s", at(aStart))
	t.Logf("A first stream line  %s", at(aFirstLine))
	t.Logf("move                 %s", at(moveAt))
	t.Logf("B first token        %s (first stream line %s)", at(bFirst), at(bFirstLine))
	t.Logf("B reply done         %s (last stream line %s)", at(bDone), at(bLastLine))
	t.Logf("A stream end         %s (job finished %s)", at(aLastLine), at(aEnd))
	t.Logf("A stream lines while B was streaming: %d", tokens.between(modelA, bFirstLine, bLastLine))
	t.Logf("B replied %q", bReply)
	t.Logf("A's moved reply: %d characters", len(result.Summary))

	if bDone.IsZero() || strings.TrimSpace(bReply) == "" {
		t.Fatal("Obey did not reply")
	}
	if !aLastLine.After(bDone) {
		t.Errorf("Anika's request ended at %s, before Obey's reply was done at %s", at(aLastLine), at(bDone))
	}
	if result.Envelope == nil || result.Envelope.PersonaID != "anika" || strings.TrimSpace(result.Summary) == "" {
		t.Fatalf("Anika's reply was delivered as %+v", result)
	}
	turns := selector.Transcript()
	var order []string
	for _, turn := range turns {
		switch {
		case turn.Role == "user":
			order = append(order, "user")
		case turn.Role == "assistant":
			order = append(order, turn.PersonaID)
		}
	}
	want := []string{"user", "anika", "user", "obey", "anika"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("shared history order %v, want %v: %+v", order, want, turns)
	}
	if last := turns[len(turns)-1]; !strings.Contains(last.Content, strings.TrimSpace(result.Summary)[:min(40, len(strings.TrimSpace(result.Summary)))]) {
		t.Fatalf("the last turn is not Anika's delivered reply: %+v", last)
	}
	delivered := countTurns(turns, func(turn brain.Turn) bool {
		return turn.Role == "assistant" && turn.PersonaID == "anika" && strings.Contains(turn.Content, strings.TrimSpace(result.Summary)[:min(40, len(strings.TrimSpace(result.Summary)))])
	})
	if delivered != 1 {
		t.Fatalf("Anika's reply is in the shared history %d times", delivered)
	}
}
