//go:build !integration

package cmd

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/internal/listen"
	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
)

// DesktopConversation is the same runtime owned by the terminal interface.
// A host must serialize turns and selection, cancel/drain in-flight operations,
// call Persist before shutdown, and call Cleanup exactly once afterwards.
// This adapter is experimental; desktop consumers pin an obey-voice revision.
type DesktopConversation = appTUI.ConversationRuntime

// DesktopOptions selects one conversation without mutating saved preferences.
type DesktopOptions struct {
	SessionID string
	PersonaID string
	TextOnly  bool
	Silent    bool
}

// BuildDesktopConversation reuses the TUI's asset preparation, session binding,
// work journal, speaker recognition, persona selection and persistence.
func BuildDesktopConversation(ctx context.Context, opts DesktopOptions, progress func(string)) (*DesktopConversation, error) {
	return conversationRuntimeBuilderFor(nil, opts.TextOnly, opts.Silent)(ctx, desktopProgress(progress), opts.SessionID, opts.PersonaID)
}

type DesktopMeeting = appTUI.MeetingRuntime
type DesktopUtterance = listen.Utterance
type DesktopListenHooks = listen.Hooks
type DesktopMeetingSink = listen.Sink

// BuildDesktopMeeting opens the same STT-only recorder and durable bundle as
// the launcher. ReleaseCapture follows a drained listen loop; Cleanup follows
// speaker finalization. Writer.Close seals the recording before finalization.
func BuildDesktopMeeting(ctx context.Context, title, resume string, progress func(string)) (*DesktopMeeting, error) {
	return meetingRuntimeBuilder()(ctx, appTUI.MeetingSpec{Description: title, ResumePath: resume}, desktopProgress(progress))
}

func RunDesktopMeeting(ctx context.Context, rt *DesktopMeeting, sink DesktopMeetingSink, hooks DesktopListenHooks) error {
	return listen.LoopWithHooks(ctx, rt.Capture, rt.Provider, sink, hooks)
}

func desktopProgress(fn func(string)) appTUI.BuildProgress {
	return func(stage appTUI.BuildStage) {
		if fn != nil {
			fn(stage.Text())
		}
	}
}

// DesktopController uses the terminal's turn controller with native actions
// and plain snapshots. It owns cleanup of the supplied runtime.
type DesktopController = appTUI.DesktopController
type DesktopAction = appTUI.DesktopAction
type DesktopState = appTUI.DesktopState

func StartDesktopController(ctx context.Context, rt *DesktopConversation, publish func(DesktopState)) *DesktopController {
	return appTUI.StartDesktopController(ctx, rt, publish)
}

// DesktopMeetingStopPhrase shares the launcher's exact normalized stop commands.
func DesktopMeetingStopPhrase(text string) bool {
	return meeting.StopPhraseSet(nil)[meeting.NormalizeStopPhrase(text)]
}
