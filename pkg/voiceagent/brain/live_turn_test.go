package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
	"github.com/ollama/ollama/api"
)

// liveDefaults is the brain configuration of a live persona at the shipped
// defaults, read from the configuration schema.
func liveDefaults(t *testing.T) *config.Config {
	t.Helper()
	value := func(key string) any {
		spec, ok := config.SpecFor(key)
		if !ok {
			t.Fatalf("no schema key %s", key)
		}
		return spec.Default
	}
	number := func(key string) int {
		switch v := value(key).(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
		t.Fatalf("%s default is not a number", key)
		return 0
	}
	flag := func(key string) bool {
		v, ok := value(key).(bool)
		if !ok {
			t.Fatalf("%s default is not a bool", key)
		}
		return v
	}
	ratio, _ := value("continuity_soft_limit_ratio").(float64)
	router, _ := value("voice_router_tools").(string)
	return &config.Config{
		MaxHistory:                  number("max_history"),
		OllamaNumCtx:                number("ollama_num_ctx"),
		OllamaResponseTokens:        number("ollama_response_tokens"),
		ContinuityIntegrityEnabled:  flag("continuity_integrity_enabled"),
		ContinuityProjectionEnabled: flag("continuity_projection_enabled"),
		ContinuityArchiveEnabled:    flag("continuity_archive_enabled"),
		ContinuitySoftLimitRatio:    ratio,
		ContinuityRecoveryMax:       number("continuity_recovery_max"),
		ContinuityNumCtxCeil:        number("continuity_num_ctx_ceil"),
		ToolCommandTimeout:          number("tool_command_timeout"),
		VoiceToolsEnabled:           flag("voice_tools_enabled"),
		VoiceRouterTools:            router,
	}
}

// liveTurns drives an Ollama brain's conversational turns against a fake
// server. reply receives the 1-based number of the chat request within the
// current turn.
type liveTurns struct {
	t     *testing.T
	o     *OllamaBrain
	mu    sync.Mutex
	reqs  []api.ChatRequest
	turn  int
	reply func(n int, req api.ChatRequest) api.ChatResponse
	opts  StreamOptions
}

func newLiveTurns(t *testing.T, cfg *config.Config, workDir string) *liveTurns {
	t.Helper()
	l := &liveTurns{t: t, opts: liveOptions()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var req api.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		l.mu.Lock()
		l.reqs = append(l.reqs, req)
		l.turn++
		n, reply := l.turn, l.reply
		l.mu.Unlock()
		_ = json.NewEncoder(w).Encode(reply(n, req))
	}))
	t.Cleanup(server.Close)
	host, _ := url.Parse(server.URL)
	if workDir == "" {
		workDir = t.TempDir()
	}
	l.o = &OllamaBrain{client: api.NewClient(host, server.Client()), model: "fixture", workDir: workDir, cfg: cfg}
	return l
}

func liveOptions() StreamOptions {
	return StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}
}

// run sends one live turn and returns what it answered and the chat requests
// it made.
func (l *liveTurns) run(ctx context.Context, streaming bool, input string, reply func(n int, req api.ChatRequest) api.ChatResponse) (string, []api.ChatRequest, error) {
	l.t.Helper()
	l.mu.Lock()
	l.reply, l.turn = reply, 0
	start := len(l.reqs)
	l.mu.Unlock()
	var answer string
	var err error
	if streaming {
		stream, startErr := l.o.ThinkStream(ctx, input, l.opts)
		if startErr != nil {
			l.t.Fatal(startErr)
		}
		answer, _ = drainStream(l.t, stream)
		err = (<-stream.Done).Err
	} else {
		answer, err = l.o.ThinkFull(ctx, input, l.opts)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return answer, append([]api.ChatRequest(nil), l.reqs[start:]...), err
}

func readFileCalls(round, calls int) []api.ToolCall {
	out := make([]api.ToolCall, 0, calls)
	for c := range calls {
		out = append(out, readFileCall(fmt.Sprintf("notes-%d-%d.txt", round, c))...)
	}
	return out
}

func hasMessage(req api.ChatRequest, role, text string) bool {
	for _, m := range req.Messages {
		if m.Role == role && strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

// The end-of-turn trim keeps a window of max_history messages. A turn that
// alone is longer than that is kept whole, wherever the window would open in
// it.
func TestLongLiveTurnKeepsItsRequestAndAnswerInContext(t *testing.T) {
	const (
		request = "Check every notes file."
		answer  = "All notes files were checked."
	)
	cases := []struct {
		name          string
		rounds, calls int
	}{
		{"window opens on a tool result", 45, 1},
		{"window opens on a tool call", 45, 2},
		{"several calls a round, window opens on a tool result", 10, 8},
		{"several calls a round, window opens on a tool call", 7, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachMode(t, func(t *testing.T, streaming bool) {
				l := newLiveTurns(t, liveDefaults(t), "")
				hello := func(int, api.ChatRequest) api.ChatResponse { return answerResponse("Hello there.") }
				if _, _, err := l.run(t.Context(), streaming, "Hello.", hello); err != nil {
					t.Fatal(err)
				}
				work := func(n int, req api.ChatRequest) api.ChatResponse {
					if n <= tc.rounds && len(req.Tools) > 0 {
						return toolResponse(readFileCalls(n, tc.calls))
					}
					return answerResponse(answer)
				}
				got, reqs, err := l.run(t.Context(), streaming, request, work)
				if err != nil || !strings.Contains(got, answer) || len(reqs) != tc.rounds+1 {
					t.Fatalf("answer=%q err=%v requests=%d", got, err, len(reqs))
				}
				_, next, err := l.run(t.Context(), streaming, "Thanks.", hello)
				if err != nil || len(next) == 0 {
					t.Fatalf("follow-up turn: err=%v", err)
				}
				if !hasMessage(next[0], "user", request) || !hasMessage(next[0], "assistant", answer) {
					t.Fatalf("the long turn left the model's context: %d messages", len(next[0].Messages))
				}
			})
		})
	}
}

// A live turn whose tool results fill the context window answers from them
// in a tools-free round instead of failing the request.
func TestLiveTurnThatFillsTheWindowAnswers(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		dir := t.TempDir()
		for i := range 120 {
			body := strings.Repeat(fmt.Sprintf("line %d of a long report\n", i), 400)
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("report-%d.txt", i)), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		l := newLiveTurns(t, liveDefaults(t), dir)
		next := 0
		reply := func(_ int, req api.ChatRequest) api.ChatResponse {
			if len(req.Tools) == 0 {
				return answerResponse("I read most of the reports; the rest remain.")
			}
			calls := make([]api.ToolCall, 0, 8)
			for range 8 {
				calls = append(calls, readFileCall(fmt.Sprintf("report-%d.txt", next))...)
				next++
			}
			return toolResponse(calls)
		}
		answer, reqs, err := l.run(t.Context(), streaming, "Read every report.", reply)
		if err != nil || !strings.Contains(answer, "the rest remain") {
			t.Fatalf("answer=%q err=%v", answer, err)
		}
		last := reqs[len(reqs)-1]
		if len(last.Tools) != 0 || !hasInstruction(last, toolContextAnswerInstruction) {
			t.Fatalf("the answer did not come from the context-full answer round: tools=%d", len(last.Tools))
		}
		limit := l.o.effectiveNumCtx() * 9 / 10
		for i, req := range reqs {
			if est := estimateMessageTokens(req.Messages, req.Tools); est > limit {
				t.Fatalf("request %d estimated %d tokens over the %d limit", i+1, est, limit)
			}
		}
	})
}

// Reading the same skill again in one turn does not add its text again.
func TestRepeatedSkillReadAddsItsTextOnce(t *testing.T) {
	const tail = "Finish with a one-line summary of the garden plan."
	eachMode(t, func(t *testing.T, streaming bool) {
		l := newLiveTurns(t, liveDefaults(t), "")
		l.o.skills = []skills.Skill{{Name: "garden-planner", Body: strings.Repeat("Plan beds by sunlight. ", 40) + tail}}
		readSkill := []api.ToolCall{{Function: api.ToolCallFunction{Name: "read_skill", Arguments: mustArgs(map[string]any{"name": "garden-planner"})}}}
		listFiles := func(path string) []api.ToolCall {
			return []api.ToolCall{{Function: api.ToolCallFunction{Name: "list_files", Arguments: mustArgs(map[string]any{"path": path})}}}
		}
		script := [][]api.ToolCall{readSkill, listFiles("."), readSkill, listFiles("beds"), readSkill}
		reply := func(n int, req api.ChatRequest) api.ChatResponse {
			if n <= len(script) && len(req.Tools) > 0 {
				return toolResponse(script[n-1])
			}
			return answerResponse("The garden plan is ready.")
		}
		_, reqs, err := l.run(t.Context(), streaming, "Plan the garden.", reply)
		if err != nil || len(reqs) != len(script)+1 {
			t.Fatalf("err=%v requests=%d", err, len(reqs))
		}
		copies := 0
		for _, m := range reqs[len(reqs)-1].Messages {
			if m.Role != "tool" {
				copies += strings.Count(m.Content, tail)
			}
		}
		if copies != 1 {
			t.Fatalf("skill text appears %d times in the turn's context, want 1", copies)
		}
	})
}

func TestLiveTurnOfFortySmallRoundsAnswers(t *testing.T) {
	const rounds = 40
	eachMode(t, func(t *testing.T, streaming bool) {
		l := newLiveTurns(t, liveDefaults(t), "")
		reply := func(n int, req api.ChatRequest) api.ChatResponse {
			if n <= rounds {
				r := toolResponse(readFileCall(fmt.Sprintf("chapter-%d.txt", n)))
				r.Metrics.EvalCount = 300
				return r
			}
			return answerResponse("None of the forty chapters exist yet.")
		}
		answer, reqs, err := l.run(t.Context(), streaming, "Read all forty chapters.", reply)
		if err != nil || !strings.Contains(answer, "forty chapters") || len(reqs) != rounds+1 {
			t.Fatalf("answer=%q err=%v requests=%d", answer, err, len(reqs))
		}
		share := float64(l.o.cfg.OllamaResponseTokens / 2)
		for i, req := range reqs {
			if len(req.Tools) == 0 {
				t.Fatalf("request %d had tools withheld", i+1)
			}
			if req.Options["num_predict"] != share {
				t.Fatalf("request %d cap = %v, want %v", i+1, req.Options["num_predict"], share)
			}
		}
	})
}

func TestLiveTurnRepeatingOneCallIsStoppedByTheGuard(t *testing.T) {
	const answer = "plan.txt does not exist, so I stopped trying to read it."
	eachMode(t, func(t *testing.T, streaming bool) {
		l := newLiveTurns(t, liveDefaults(t), "")
		reply := func(_ int, req api.ChatRequest) api.ChatResponse {
			if len(req.Tools) > 0 {
				return toolResponse(readFileCall("plan.txt"))
			}
			return answerResponse(answer)
		}
		got, reqs, err := l.run(t.Context(), streaming, "Read the plan.", reply)
		if err != nil || !strings.Contains(got, answer) || len(reqs) != maxRepeatedToolRounds+1 {
			t.Fatalf("answer=%q err=%v requests=%d", got, err, len(reqs))
		}
		last := reqs[len(reqs)-1]
		if len(last.Tools) != 0 || !hasInstruction(last, toolRepeatAnswerInstruction) {
			t.Fatalf("the answer did not come from the guard's answer round: tools=%d", len(last.Tools))
		}
		history := l.o.History()
		if reply := history[len(history)-1]; reply.Role != "assistant" || reply.Content != answer || reply.Disposition != "" {
			t.Fatalf("the forced answer is not an ordinary reply: %+v", reply)
		}
	})
}

// A request that generates past ollama_response_tokens, after a tool round,
// ends the turn with the output-limit result and is not continued.
func TestLiveTurnRunawayRequestEndsWithOutputLimit(t *testing.T) {
	eachMode(t, func(t *testing.T, streaming bool) {
		l := newLiveTurns(t, liveDefaults(t), "")
		reply := func(n int, _ api.ChatRequest) api.ChatResponse {
			if n == 1 {
				return toolResponse(readFileCall("draft.txt"))
			}
			r := answerResponse("The draft is missing and the draft is missing and")
			r.DoneReason, r.Metrics.EvalCount = "length", l.o.cfg.OllamaResponseTokens+1
			return r
		}
		_, reqs, err := l.run(t.Context(), streaming, "Summarize the draft.", reply)
		if !errors.Is(err, ErrResponseLimit) || len(reqs) != 2 {
			t.Fatalf("err=%v requests=%d", err, len(reqs))
		}
	})
}

// Stopping a turn while a round's first call runs leaves the rest of that
// round unrun and sends nothing more; stopping during its last call sends no
// further request.
func TestStoppedLiveTurnRunsNoFurtherCallOrRequest(t *testing.T) {
	for _, stopAt := range []int{1, 3} {
		t.Run(fmt.Sprintf("stopped during call %d", stopAt), func(t *testing.T) {
			eachMode(t, func(t *testing.T, streaming bool) {
				dir := t.TempDir()
				l := newLiveTurns(t, liveDefaults(t), dir)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := 0
				l.opts.OnToolStart = func(ToolCall) {
					if started++; started == stopAt {
						cancel()
					}
				}
				write := func(name string) []api.ToolCall {
					return []api.ToolCall{{Function: api.ToolCallFunction{Name: "write_file", Arguments: mustArgs(map[string]any{"path": name, "content": "seedling notes"})}}}
				}
				var calls []api.ToolCall
				for _, name := range []string{"bed-1.txt", "bed-2.txt", "bed-3.txt"} {
					calls = append(calls, write(name)...)
				}
				reply := func(int, api.ChatRequest) api.ChatResponse { return toolResponse(calls) }
				_, reqs, err := l.run(ctx, streaming, "Write the bed notes.", reply)
				if !errors.Is(err, context.Canceled) || len(reqs) != 1 || started != stopAt {
					t.Fatalf("err=%v requests=%d calls started=%d", err, len(reqs), started)
				}
				for i, name := range []string{"bed-1.txt", "bed-2.txt", "bed-3.txt"} {
					_, statErr := os.Stat(filepath.Join(dir, name))
					if ran := statErr == nil; ran != (i < stopAt) {
						t.Fatalf("%s written=%v after a stop during call %d", name, ran, stopAt)
					}
				}
				if !streaming {
					return
				}
				results := 0
				for _, turn := range l.o.History() {
					if turn.Role == "tool" {
						results++
					}
				}
				if results != len(calls) {
					t.Fatalf("%d tool results for %d calls", results, len(calls))
				}
			})
		})
	}
}
