package personaruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestNativeCommittedSelectionPublishesDespiteDurabilityWarningOrLateCancel(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		warning, cancelAfterCommit bool
	}{
		{"durability warning", true, false},
		{"cancel after commit", false, true},
		{"warning and cancel", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeSelectorFixture(t)
			defer f.close(t)
			defer f.selector.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			warnings := 0
			f.pipeline.Events = events.NewBus()
			f.pipeline.Events.SubscribeAll(func(event events.Event) {
				if e, ok := event.(events.Error); ok && e.Stage == "session_durability" {
					warnings++
				}
			})
			persist := f.selector.opts.Persist
			f.selector.opts.Persist = func(turns []brain.Turn, selected events.PersonaSelection) error {
				if err := persist(turns, selected); err != nil {
					return err
				}
				// Real isolated Store.SaveSelectionState has already atomically published
				// mode/identity/history. Turn cancellation cannot roll that commit back.
				if tc.cancelAfterCommit {
					cancel()
				}
				if tc.warning {
					return &session.WriteCommittedError{Err: errors.New("fixture directory durability warning")}
				}
				return nil
			}
			selected, err := f.selector.Select(ctx, "native", "test")
			if err != nil {
				t.Fatalf("committed Native selection falsely rolled back: %v", err)
			}
			disk, err := f.store.Load(f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if disk.Selection == nil || disk.Selection.AudioMode != persona.AudioNative || disk.Selection.BindingHash != selected.BindingHash || disk.Selection.Revision != selected.Revision || disk.Selection.Voice != selected.Voice || disk.Selection.BrainModel != selected.BrainModel {
				t.Fatalf("saved identity differs from published selection: disk=%+v selected=%+v", disk.Selection, selected)
			}
			if selected.AudioMode != persona.AudioNative || f.pipeline.Native != f.candidate.Policy || f.pipeline.Brain != f.candidate.Brain || f.pipeline.TTS != f.candidate.Voice.Primary || f.selector.Snapshot().BindingHash != selected.BindingHash || f.inputReleased != 0 {
				t.Fatal("committed Native input/brain/voice publication was partial")
			}
			if f.candidate.Brain.(*selectorTestBrain).IsClosed() {
				t.Fatal("published candidate was retired by late cancellation")
			}
			if err := f.publisher(t.Context(), func() error { return nil }); err != nil {
				t.Fatal("committed caption publisher refused", err)
			}
			wantWarnings := 0
			if tc.warning {
				wantWarnings = 1
			}
			if warnings != wantWarnings {
				t.Fatalf("durability warnings=%d want%d", warnings, wantWarnings)
			}
			if tc.cancelAfterCommit && !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("fixture missed cancellation boundary")
			}
		})
	}
}
