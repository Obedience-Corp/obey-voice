package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

type imageRepairWorker struct {
	fakeBrain
	answers []string
	inputs  []string
	refs    []chatmedia.Ref
}

func (w *imageRepairWorker) ThinkFull(_ context.Context, input string, _ brain.StreamOptions) (string, error) {
	w.inputs = append(w.inputs, input)
	return w.answers[len(w.inputs)-1], nil
}

func (w *imageRepairWorker) SetReplyMedia(_ string, refs []chatmedia.Ref) error {
	w.refs = refs
	return nil
}

func TestWorkerRepairsRejectedImageReferences(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.png"), testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	const bad = "![Example](https://example.invalid/page)"
	const good = "![Example](fixture.png)"
	for _, tc := range []struct {
		name     string
		answers  []string
		wantRefs int
		wantErr  bool
	}{
		{"recovered", []string{bad, good}, 1, false},
		{"still invalid", []string{bad, bad}, 0, true},
		{"partial success", []string{bad + "\n" + good}, 1, false},
		{"honest failure", []string{bad, "I couldn't obtain the file."}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{WorkDir: dir, VoiceToolsEnabled: true}
			w := &imageRepairWorker{answers: tc.answers}
			jobs := job.NewTable(t.Context())
			defer jobs.Close()
			a := NewAgency(&Pipeline{Events: events.NewBus()}, jobs, w, cfg, nil)
			_, err := a.workerRun("Show the image", w, cfg)(t.Context())
			if (err != nil) != tc.wantErr || len(w.refs) != tc.wantRefs || len(w.inputs) != len(tc.answers) {
				t.Fatalf("inputs=%v refs=%v err=%v", w.inputs, w.refs, err)
			}
			if len(w.inputs) > 1 && !strings.Contains(w.inputs[1], "remote image URLs") {
				t.Fatalf("missing import reason: %q", w.inputs[1])
			}
			for _, ref := range w.refs {
				if _, err := chatmedia.DefaultStore().Preview(ref); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
