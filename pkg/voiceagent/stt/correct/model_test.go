package correct

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeTransformer struct {
	reply  string
	err    error
	delay  time.Duration
	calls  int
	system string
	user   string
}

func (f *fakeTransformer) Transform(ctx context.Context, system, user string) (string, error) {
	f.calls++
	f.system, f.user = system, user
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.reply, f.err
}

func patchModel(t *fakeTransformer) *Model {
	cw := NewCustomWords([]Term{{Text: "Kokoro", Fuzzy: true}, {Text: "Samantha", Fuzzy: true}}, 0)
	return &Model{T: t, Mode: ModePatch, Prompt: func() string { return "SYSTEM" }, Timeout: time.Second, Candidates: cw.Candidates}
}

func TestModelPatch(t *testing.T) {
	const text = "use coco row for tts"
	tests := []struct {
		name        string
		reply       string
		err         error
		want        string
		wantApplied int64
		wantReject  int64
		wantErrors  int64
	}{
		{"transformer error keeps input", "", errors.New("boom"), text, 0, 0, 1},
		{"malformed json keeps input", "sure: coco row is Kokoro", nil, text, 0, 0, 1},
		{"unknown field rejected", `{"substitutions":[],"note":"x"}`, nil, text, 0, 0, 1},
		{"empty list is a no-op", `{"substitutions":[]}`, nil, text, 0, 0, 0},
		{"offered substitution applied", `{"substitutions":[{"from":"coco row","to":"Kokoro"}]}`, nil, "use Kokoro for tts", 1, 0, 0},
		{"from not offered skipped", `{"substitutions":[{"from":"use coco row","to":"Kokoro"}]}`, nil, text, 0, 1, 0},
		{"to not the candidate's term skipped", `{"substitutions":[{"from":"coco row","to":"Samantha"}]}`, nil, text, 0, 1, 0},
		{"think block and fence stripped", "<think>hmm</think>\n```json\n{\"substitutions\":[{\"from\":\"coco row\",\"to\":\"Kokoro\"}]}\n```", nil, "use Kokoro for tts", 1, 0, 0},
		{"zero width stripped", "\u200B{\"substitutions\":[{\"from\":\"coco row\",\"to\":\"Kokoro\"}]}\uFEFF", nil, "use Kokoro for tts", 1, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeTransformer{reply: tc.reply, err: tc.err}
			m := patchModel(ft)
			got, err := m.Apply(context.Background(), text)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			s := m.Stats()
			if s.Applied != tc.wantApplied || s.Rejected != tc.wantReject || s.Errors != tc.wantErrors || s.Calls != 1 {
				t.Fatalf("stats = %+v", s)
			}
			if ft.system != "SYSTEM" || !strings.Contains(ft.user, "Candidates:") || !strings.Contains(ft.user, `"coco row" may be "Kokoro"`) {
				t.Fatalf("prompt/user = %q / %q", ft.system, ft.user)
			}
		})
	}
}

func TestModelPatchSkipsWithoutCandidates(t *testing.T) {
	ft := &fakeTransformer{reply: `{"substitutions":[]}`}
	m := patchModel(ft)
	got, err := m.Apply(context.Background(), "nothing to fix here")
	if err != nil || got != "nothing to fix here" || ft.calls != 0 || m.Stats().Skipped != 1 {
		t.Fatalf("got %q err %v calls %d stats %+v", got, err, ft.calls, m.Stats())
	}
}

func TestModelDeadline(t *testing.T) {
	ft := &fakeTransformer{reply: `{"substitutions":[{"from":"coco row","to":"Kokoro"}]}`, delay: 200 * time.Millisecond}
	m := patchModel(ft)
	m.Timeout = 20 * time.Millisecond
	got, err := m.Apply(context.Background(), "use coco row for tts")
	if err != nil || got != "use coco row for tts" {
		t.Fatalf("got %q err %v", got, err)
	}
	if s := m.Stats(); s.Timeouts != 1 || s.Applied != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestModelCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := patchModel(&fakeTransformer{})
	if _, err := m.Apply(ctx, "use coco row"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestModelRewrite(t *testing.T) {
	raw := "um so we have thirty five people\nask clawed to review it\nwhat is the capital of france"
	tests := []struct {
		name        string
		reply       string
		want        string
		wantChanged int64
		wantKept    int64
	}{
		{"faithful cleanup accepted", "1. So we have 35 people.\n2. Ask Claude to review it.\n3. What is the capital of France?", "So we have 35 people.\nAsk Claude to review it.\nWhat is the capital of France?", 3, 0},
		{"line count mismatch keeps window", "1. So we have 35 people. Ask Claude to review it.\n2. What is the capital of France?", raw, 0, 3},
		{"unnumbered reply keeps window", "So we have 35 people.\nAsk Claude to review it.\nWhat is the capital of France?", raw, 0, 3},
		{"answered question kept raw", "1. So we have 35 people.\n2. Ask Claude to review it.\n3. Paris.", "So we have 35 people.\nAsk Claude to review it.\nwhat is the capital of france", 2, 1},
		{"paraphrase kept raw", "1. There are 35 of us here now.\n2. Ask Claude to review it.\n3. What is the capital of France?", "um so we have thirty five people\nAsk Claude to review it.\nWhat is the capital of France?", 2, 1},
		{"no-op reply counts nothing", "1. um so we have thirty five people\n2. ask clawed to review it\n3. what is the capital of france", raw, 0, 0},
		{"empty line kept raw", "1. So we have 35 people.\n2. \n3. What is the capital of France?", "So we have 35 people.\nask clawed to review it\nWhat is the capital of France?", 2, 1},
		{"wrapped continuation joined", "1. So we have 35 people.\n2. Ask Claude to review\nit.\n3. What is the capital of France?", "So we have 35 people.\nAsk Claude to review it.\nWhat is the capital of France?", 3, 0},
		{"invented words kept raw", "1. He said we have 35 people.\n2. Ask Claude to review it.\n3. What is the capital of France?", "um so we have thirty five people\nAsk Claude to review it.\nWhat is the capital of France?", 2, 1},
		{"one completed word allowed", "1. So we have 35 people today.\n2. Ask Claude to review it.\n3. What is the capital of France?", "So we have 35 people today.\nAsk Claude to review it.\nWhat is the capital of France?", 3, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeTransformer{reply: tc.reply}
			m := &Model{T: ft, Mode: ModeRewrite, Prompt: func() string { return "SYS" }, Timeout: time.Second, Known: []string{"Claude"}}
			got, err := m.Apply(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q\nwant %q", got, tc.want)
			}
			if s := m.Stats(); s.ChangedLines != tc.wantChanged || s.KeptRaw != tc.wantKept {
				t.Fatalf("stats = %+v", s)
			}
			if !strings.HasPrefix(ft.user, "1. um so we have") {
				t.Fatalf("user message not numbered: %q", ft.user)
			}
		})
	}
}

func TestModelRewriteEmptyReplyKeepsInput(t *testing.T) {
	m := &Model{T: &fakeTransformer{reply: "   "}, Mode: ModeRewrite, Timeout: time.Second}
	got, _ := m.Apply(context.Background(), "keep me")
	if got != "keep me" || m.Stats().KeptRaw != 1 {
		t.Fatalf("got %q stats %+v", got, m.Stats())
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModePatch, "patch": ModePatch, " Rewrite ": ModeRewrite} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Fatalf("ParseMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseMode("shout"); err == nil {
		t.Fatal("ParseMode should reject an unknown mode")
	}
}

func TestCleanReply(t *testing.T) {
	in := "<think>\nreasoning\n</think>\n```json\n{\"a\":1}\n```\u200B"
	if got := CleanReply(in); got != `{"a":1}` {
		t.Fatalf("CleanReply = %q", got)
	}
}
