package pipeline

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestMuseSparkCompactPreservesPersistedTranscript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture CLI")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' '{"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":"shared summary"}}'
`
	if err := os.WriteFile(filepath.Join(binDir, "muse"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := brain.NewMuseSpark(&config.Config{BrainProvider: "muse-spark", AgentName: "Obey", MaxHistory: 2, PromptsDir: t.TempDir(), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var original []brain.Turn
	for range 3 {
		original = append(original, brain.Turn{Role: "user", Content: "remember cobalt"}, brain.Turn{Role: "assistant", Content: "cobalt", PersonaID: "jarvis", PersonaDisplayName: "Jarvis", ResponseID: "r1"})
	}
	b.LoadHistory(original)
	store := session.NewStore(t.TempDir())
	saved := session.New("muse-spark", "test")
	saves := 0
	p := &Pipeline{Brain: b, CompactPrompt: "summarize", OnTurn: func() {
		if err := store.Save(saved, b.History()); err != nil {
			t.Fatal(err)
		}
		saves++
	}}
	if err := p.CompactConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Turns, original) || !reflect.DeepEqual(b.History(), original) || saves != 1 {
		t.Fatalf("compaction lost transcript: persisted=%+v", loaded.Turns)
	}
	view := b.ContextHistory()
	if len(view) != 3 || view[0].Content != "shared summary" || view[2].ResponseID != "r1" {
		t.Fatalf("bad summary/tail: %+v", view)
	}
	for _, turn := range b.History() {
		if strings.Contains(turn.Content, "shared summary") {
			t.Fatal("summary persisted as conversation")
		}
	}
	p.AutoCompactAfter = 4
	if err := p.autoCompactIfNeeded(t.Context()); err != nil {
		t.Fatal(err)
	}
	if saves != 1 {
		t.Fatal("unbounded transcript triggered repeated compaction")
	}
}
