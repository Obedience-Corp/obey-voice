package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/listen"
	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	meetinglog "github.com/Obedience-Corp/obey-voice/internal/meeting/log"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// ConversationRuntime is a live pipeline prepared for the conversation
// screen. The TUI owns its lifecycle from the moment the builder returns it:
// Cleanup runs exactly once, after the program exits and in-flight turns
// have drained.
type ConversationRuntime struct {
	Pipeline *pipeline.Pipeline
	Bus      *events.Bus
	Voice    bool // STT is configured; voice turns may run
	Output   bool // TTS and playback are configured
	// PersonaID and AgentName identify the session binding this runtime was
	// built from. The conversation renders this identity for its lifetime —
	// later persona switches or edits must not relabel an in-flight session.
	PersonaID string
	AgentName string
	// SessionCfg is the binding-derived config snapshot (provider, voice, model
	// routing). Conversation chrome such as the TTS badge must read this, not
	// the live global app config — starting uncle-fu while the active persona
	// is still samantha must not show kokoro/af_heart.
	// Updated only from ReloadVoice after a successful TTS install; consumed on
	// the Bubble Tea update thread after voiceReloadedMsg (no concurrent readers).
	SessionCfg   *config.Config
	SessionID    string
	InputDevice  string
	OutputDevice string
	LiveSpeaker  LiveSpeakerController
	// SpeakerNames maps speaker-N → display name for bubbles and model prompts.
	// Session-local; shared with the brain via brain.AttachSpeakerNames.
	SpeakerNames *speaker.NameMap
	Seed         []brain.Turn                // resumed history to pre-populate the viewport
	ReloadVoice  func(context.Context) error // applies Settings to subsequent utterances
	Cleanup      func()                      // tears down pipeline resources and saves the session
}

// BuildStage is one step of the conversation build, reported so the starting
// screen can name what it is waiting on. Model loads take seconds each; a
// screen that says nothing through them reads as a hang.
//
// Label is what to show. Pct is meaningful only for a download, where it is
// 0-100; every other stage has no measurable progress.
type BuildStage struct {
	Label    string
	Download bool
	Pct      float64
}

// BuildProgress receives stages from the builder. It is called on the
// builder's goroutine, never the update loop.
type BuildProgress func(BuildStage)

// Downloading reports an asset transfer at pct (0-100).
func (p BuildProgress) Downloading(name string, pct float64) {
	if p != nil {
		p(BuildStage{Label: "Downloading " + name, Download: true, Pct: pct})
	}
}

// Stage reports a step with no measurable progress.
func (p BuildProgress) Stage(label string) {
	if p != nil {
		p(BuildStage{Label: label})
	}
}

// Text renders the stage for the starting screen.
func (s BuildStage) Text() string {
	if s.Download && s.Pct > 0 {
		return fmt.Sprintf("%s: %d%%", s.Label, int(s.Pct))
	}
	if s.Download {
		return s.Label + "…"
	}
	return s.Label + "…"
}

// RuntimeBuilder constructs the runtime when the user enters the
// conversation screen (D2: the mic goes hot here, not in the launcher).
// Build progress is reported through progress and rendered in-screen.
// personaID selects the session's persona binding; empty resolves the
// configured active persona.
type RuntimeBuilder func(ctx context.Context, progress BuildProgress, sessionID, personaID string) (*ConversationRuntime, error)

// MeetingRuntime holds STT, the bundle writer, and optional speaker finalizer
// for the embedded meeting screen.
type MeetingRuntime struct {
	Capture          listen.Resetter
	Provider         stt.Provider
	Writer           *meetinglog.Writer
	FinalizeSpeakers func(context.Context) (meeting.AnalysisResult, error)
	SpeakerStatus    meeting.AnalysisStatus
	SpeakerError     string
	// LiveSpeaker drives provisional speaker-N glyphs on live transcript lines.
	LiveSpeaker LiveSpeakerController
	Description string
	Path        string
	StopPhrases map[string]bool
	// ReleaseCapture tears down mic/STT/live-label resources at capture stop,
	// while background diarization still owns the analyzer. Cleanup releases
	// the rest (the analyzer) once diarization is done. Both idempotent.
	ReleaseCapture func()
	Cleanup        func()
}

// MeetingBuilder constructs meeting resources when the user starts recording
// from the main launcher (assets + mic hot only after title confirm). Build
// progress is reported through progress and rendered in-screen, the same way
// the conversation reports its own.
type MeetingBuilder func(ctx context.Context, description string, progress BuildProgress) (*MeetingRuntime, error)

// meetingReadyMsg delivers a built meeting runtime (or error) into Update.
type meetingReadyMsg struct {
	rt  *MeetingRuntime
	err error
}

func buildMeeting(build MeetingBuilder, ctx context.Context, feed *eventBridge, slot *runtimeSlot[*MeetingRuntime], description string) tea.Cmd {
	if build == nil {
		return func() tea.Msg {
			return meetingReadyMsg{err: fmt.Errorf("meeting builder not configured")}
		}
	}
	return func() tea.Msg {
		rt, err := build(ctx, description, func(stage BuildStage) {
			feed.send(buildStageMsg{stage: stage})
		})
		feed.send(progressClosedMsg{})
		if err != nil {
			return meetingReadyMsg{err: err}
		}
		if rt != nil && slot != nil {
			// Claim ownership before returning so run() can always clean up,
			// even if the ready message is never applied (quit mid-build).
			slot.store(rt)
			if ctx.Err() != nil {
				slot.cleanup()
				return meetingReadyMsg{err: ctx.Err()}
			}
		}
		return meetingReadyMsg{rt: rt, err: err}
	}
}

// runtimeSlot owns a built runtime across the race between the builder Cmd
// finishing and the Bubble Tea program quitting. Without it, a ready message
// that never reaches Update would leak whatever the build opened: a hot mic,
// a loaded model, an open meeting bundle.
//
// Both builds need one. Ctrl+C has always been able to quit during a
// conversation build, and quits during a meeting build too now that the
// recorder has a waiting screen instead of owning the key as "stop".
type runtimeSlot[T comparable] struct {
	mu sync.Mutex
	rt T
	// release tears the runtime down. Held as a function so one slot serves
	// both runtimes, which share the shape but not a type.
	release func(T)
}

// newConversationSlot owns a ConversationRuntime's teardown.
func newConversationSlot() *runtimeSlot[*ConversationRuntime] {
	return &runtimeSlot[*ConversationRuntime]{release: func(rt *ConversationRuntime) {
		if rt.Cleanup != nil {
			rt.Cleanup()
		}
	}}
}

// newMeetingSlot owns a MeetingRuntime's teardown. MeetingRuntime.Cleanup is
// idempotent by contract, so a recorder stopped through its own lifecycle is
// safe to release again here.
func newMeetingSlot() *runtimeSlot[*MeetingRuntime] {
	return &runtimeSlot[*MeetingRuntime]{release: func(rt *MeetingRuntime) {
		if rt.Cleanup != nil {
			rt.Cleanup()
		}
	}}
}

func (s *runtimeSlot[T]) store(rt T) {
	var zero T
	s.mu.Lock()
	old := s.rt
	s.rt = rt
	s.mu.Unlock()
	if old != zero && old != rt {
		s.free(old)
	}
}

func (s *runtimeSlot[T]) cleanup() {
	var zero T
	s.mu.Lock()
	rt := s.rt
	s.rt = zero
	s.mu.Unlock()
	if rt != zero {
		s.free(rt)
	}
}

func (s *runtimeSlot[T]) free(rt T) {
	if s.release != nil {
		s.release(rt)
	}
}

// buildStageMsg carries a build stage into the update loop.
type buildStageMsg struct {
	stage BuildStage
}

// progressClosedMsg tells the update loop to stop draining the progress feed.
type progressClosedMsg struct{}

// runtimeReadyMsg delivers the built runtime (or the fatal build error).
type runtimeReadyMsg struct {
	rt  *ConversationRuntime
	err error
}

type voiceReloadedMsg struct {
	err         error
	resumeVoice bool
}

func reloadVoice(ctx context.Context, reload func(context.Context) error, resumeVoice bool) tea.Cmd {
	return func() tea.Msg {
		return voiceReloadedMsg{err: reload(ctx), resumeVoice: resumeVoice}
	}
}

// buildRuntime runs the builder off the update loop, streaming progress
// through the feed so the conversation screen can render it.
func buildRuntime(build RuntimeBuilder, ctx context.Context, feed *eventBridge, slot *runtimeSlot[*ConversationRuntime], sessionID, personaID string) tea.Cmd {
	return func() tea.Msg {
		rt, err := build(ctx, func(stage BuildStage) {
			feed.send(buildStageMsg{stage: stage})
		}, sessionID, personaID)
		feed.send(progressClosedMsg{})
		if err != nil {
			return runtimeReadyMsg{err: err}
		}
		if rt != nil && slot != nil {
			// Claim ownership before returning so run() can always clean up,
			// even if the ready message is never applied (quit mid-build).
			slot.store(rt)
			if ctx.Err() != nil {
				slot.cleanup()
				return runtimeReadyMsg{err: ctx.Err()}
			}
		}
		return runtimeReadyMsg{rt: rt, err: err}
	}
}

// drainTimeout caps how long shutdown waits for an in-flight turn after its
// context is canceled, mirroring cmd/obey-voice's forceQuitTimeout.
const drainTimeout = 3 * time.Second

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}
