package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Opt in explicitly. This spends real inference turns using the installed
// Codex login; it never records audio or changes personal Obey/Codex settings.
func TestCodexLiveConversation(t *testing.T) {
	if os.Getenv("OBEY_VOICE_CODEX_LIVE") != "1" {
		t.Skip("set OBEY_VOICE_CODEX_LIVE=1 to use the installed Codex login")
	}
	cfg := &config.Config{BrainProvider: "codex", AgentName: "Obey", MaxHistory: 10, CodexModel: os.Getenv("OBEY_VOICE_CODEX_MODEL"), CodexReasoningEffort: "low"}
	cfg.WorkDir = t.TempDir()
	cfg.WorkDirSource = "fixture"
	c, err := NewCodex(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for i, input := range []string{"Remember the synthetic test phrase cobalt peach 731. Reply with that phrase only. Do not use tools.", "What synthetic test phrase did I ask you to remember? Reply with it only. Do not use tools.", "Recall the synthetic test phrase from the historical conversation records. Reply with it only. Do not use tools."} {
		if i == 2 {
			// Exercise persisted-transcript replay, not only native thread resume.
			c.LoadHistory(c.History())
		}
		started := time.Now()
		stream, err := c.ThinkStream(ctx, input, StreamOptions{OmitTurnInstruction: true})
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		first := time.Duration(0)
		for chunk := range stream.Chunks {
			if first == 0 {
				first = time.Since(started)
			}
			text.WriteString(chunk)
		}
		if result := <-stream.Done; result.Err != nil {
			t.Fatal(result.Err)
		}
		if !strings.Contains(strings.ToLower(text.String()), "cobalt peach 731") {
			t.Fatalf("turn %d continuity failed: %q", i+1, text.String())
		}
		t.Logf("turn=%d first_text=%s total=%s thread=%s", i+1, first, time.Since(started), c.SessionInfo().ID)
	}
	if len(c.History()) != 6 {
		t.Fatal("three turns missing from canonical history")
	}
}

func TestCodexLiveToolsDisabled(t *testing.T) {
	if os.Getenv("OBEY_VOICE_CODEX_LIVE") != "1" {
		t.Skip("explicit live opt-in required")
	}
	workspace := t.TempDir()
	cfg := &config.Config{BrainProvider: "codex", AgentName: "Obey", CodexReasoningEffort: "low", WorkDir: workspace, WorkDirSource: "fixture", CodexModel: os.Getenv("OBEY_VOICE_CODEX_MODEL")}
	c, err := NewCodex(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	calls := 0
	_, err = c.ThinkFull(ctx, "Use a shell or file editing tool to create a file named must-not-exist.txt in the current directory containing test. If no tools are available, reply that tools are unavailable. Do not just say you created it.", StreamOptions{OmitTurnInstruction: true, OnToolStart: func(ToolCall) { calls++ }})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("tools-disabled conversation executed a tool")
	}
	if _, err = os.Stat(filepath.Join(workspace, "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatal("tools-disabled conversation wrote a file")
	}
}
