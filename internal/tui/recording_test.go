package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestRecordingReviewUsesTrackedForegroundAndBlocksLifecycle(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.deps.ctx = t.Context()
	called := false
	m.deps.replayRecording = func(ctx context.Context, id string, epoch uint64) error {
		called = true
		if id != "row" || epoch != 9 {
			t.Fatal(id, epoch)
		}
		return nil
	}
	m.plainTurns = []plainChatTurn{{role: "user", input: &utterance.Finalized{ID: "row", CaptureEpoch: 9, Audio: &utterance.AudioRef{}}}}
	cmd := m.recordingCommand([]string{"replay", "1"})
	if cmd == nil || m.turnState != turnTextRunning || !m.reviewingRecording {
		t.Fatal("not foreground-owned")
	}
	cleared := false
	m.deps.clearHistory = func() { cleared = true }
	m.executeSlashCommand(slashCommand{id: commandClear}, nil)
	if cleared {
		t.Fatal("Clear crossed active replay")
	}
	if m.requestRecording("transcribe", "row", 9) != nil {
		t.Fatal("overlapping review admitted")
	}
	msg := cmd().(recordingDoneMsg)
	m.finishRecording(msg)
	if !called || m.turnState != turnIdle || m.reviewingRecording {
		t.Fatal("review failed to finish")
	}
}

func TestRecordingReviewCancellationFinishesBeforeNextTurn(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.deps.ctx = t.Context()
	started := make(chan struct{})
	m.deps.transcribeRecording = func(ctx context.Context, _ string, _ uint64) error { close(started); <-ctx.Done(); return ctx.Err() }
	cmd := m.requestRecording("transcribe", "row", 1)
	done := make(chan recordingDoneMsg, 1)
	go func() { done <- cmd().(recordingDoneMsg) }()
	<-started
	m.turnCancel()
	msg := <-done
	if !errors.Is(msg.err, context.Canceled) || m.turnState != turnTextRunning {
		t.Fatal(msg, m.turnState)
	}
	m.finishRecording(msg)
	if m.turnState != turnIdle {
		t.Fatal("cancel did not retire review")
	}
}
