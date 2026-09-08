package brain

import (
	"slices"
	"strings"
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// A worker job is one conversation. The sibling a brain hands out for the
// next job shares the expensive parts and none of the talk.
func TestGrokFreshStartsClean(t *testing.T) {
	cfg := &config.Config{AgentName: "Sam"}
	g := &GrokBrain{cfg: cfg, systemPrompt: "persona", turnInstruction: "turn", sessionID: "sess-1",
		history: []Turn{{Role: "user", Content: "job one"}}}
	f, ok := g.Fresh().(*GrokBrain)
	if !ok {
		t.Fatal("Fresh must return a GrokBrain")
	}
	if f.sessionID != "" || len(f.history) != 0 {
		t.Fatalf("fresh grok = session %q, %d turns; want none", f.sessionID, len(f.history))
	}
	if f.cfg != cfg || f.systemPrompt != "persona" || f.turnInstruction != "turn" {
		t.Fatal("fresh grok lost its prompts or config")
	}
	if g.sessionID != "sess-1" || len(g.history) != 1 {
		t.Fatal("the original must be untouched")
	}
	if f.runOptions(grok.PlainOutput, true).ResumeID != "" {
		t.Fatal("a fresh worker must not resume")
	}
}

func TestClaudeFreshStartsCleanAndNeverPersistsAWorkerSession(t *testing.T) {
	cfg := &config.Config{AgentName: "Sam", IsWorker: true}
	b := &Brain{cfg: cfg, systemPrompt: "persona", sessionID: "sess-1", promptTokens: 4000, sessionWarned: true,
		history: []Turn{{Role: "user", Content: "job one"}}}
	f, ok := b.Fresh().(*Brain)
	if !ok {
		t.Fatal("Fresh must return a Brain")
	}
	if f.sessionID != "" || len(f.history) != 0 || f.promptTokens != 0 || f.sessionWarned {
		t.Fatal("fresh claude carried session state over")
	}
	opts := f.runOptions(claude.JSONOutput, true)
	if opts.ResumeID != "" || !opts.NoSessionPersistence {
		t.Fatalf("worker run options = resume %q, persist %v; want no resume and no persistence", opts.ResumeID, !opts.NoSessionPersistence)
	}
	if args := claude.BuildArgs("prompt", opts); !slices.Contains(args, "--no-session-persistence") {
		t.Fatalf("args %v lack --no-session-persistence", args)
	}
	live := &Brain{cfg: &config.Config{}, systemPrompt: "persona"}
	if live.runOptions(claude.JSONOutput, true).NoSessionPersistence {
		t.Fatal("the live brain must keep persisting its sessions")
	}
}

func TestOllamaFreshStartsClean(t *testing.T) {
	cfg := &config.Config{AgentName: "Sam"}
	o := &OllamaBrain{cfg: cfg, model: "qwen3:8b", workDir: "/camp", systemPrompt: "persona",
		fullSystemPrompt: "persona + env", turnInstruction: "turn", skillCallSeq: 3, contextHighWater: 900,
		history: []api.Message{{Role: "user", Content: "job one"}}}
	f, ok := o.Fresh().(*OllamaBrain)
	if !ok {
		t.Fatal("Fresh must return an OllamaBrain")
	}
	if len(f.history) != 0 || f.skillCallSeq != 0 || f.contextHighWater != 0 {
		t.Fatal("fresh ollama carried conversation state over")
	}
	if f.model != "qwen3:8b" || f.workDir != "/camp" || f.fullSystemPrompt != "persona + env" || f.cfg != cfg {
		t.Fatal("fresh ollama lost its shared parts")
	}
	if len(o.history) != 1 {
		t.Fatal("the original must be untouched")
	}
	if !strings.Contains(f.fullSystemPrompt, "persona") {
		t.Fatal("prompt shared")
	}
}
