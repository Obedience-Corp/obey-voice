package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/app"
	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// brainSessionReporter adapts an optional brain.SessionReporter into the
// conversationDeps callback, so /session degrades gracefully on providers
// that do not expose session state.
func brainSessionReporter(p brain.Provider) func() (brain.SessionState, bool) {
	r, ok := p.(brain.SessionReporter)
	if !ok {
		return nil
	}
	return func() (brain.SessionState, bool) { return r.SessionInfo(), true }
}

// turnRunner is the slice of pipeline.Pipeline the conversation driver uses.
type turnRunner interface {
	RunTurn(ctx context.Context) (string, error)
	RunTurnTextMode(ctx context.Context, input string) error
}

// turnState tracks the single turn allowed in flight: pipeline turn methods
// assume one turn owns the pipeline's shared state at a time.
type turnState int

const (
	turnIdle            turnState = iota
	turnVoiceListening            // voice turn in flight, no final transcript yet — cancelable
	turnVoiceResponding           // voice turn past transcription — text Enter barges in (cancels TTS/brain)
	turnVoiceCanceling            // canceled for a text submit, awaiting voiceTurnDoneMsg
	turnTextRunning               // text turn in flight
)

type voiceTurnDoneMsg struct {
	lifetime    context.Context
	text        string
	err         error
	noticeEpoch uint64
}

type textTurnDoneMsg struct {
	lifetime    context.Context
	input       string
	err         error
	noticeEpoch uint64
}

type compactDoneMsg struct {
	err error
}

type voiceRetryMsg struct{}

// conversationDeps wires the live pipeline into the conversation model.
type conversationDeps struct {
	runner         turnRunner
	bus            *events.Bus
	clearHistory   func()
	workScope      func() work.WorkScope
	workSnapshot   func() work.Snapshot
	journalHealth  func() events.WorkJournalHealth
	workersSummary func() string
	workers        func() []pipeline.WorkerInfo
	// compact runs the pipeline's conversation compaction; nil when the
	// runtime did not wire it (e.g. prompt resolution failed).
	compact func(ctx context.Context) error
	// brainSession reports the provider's live session state for /session;
	// nil (or ok=false) when the provider does not expose one.
	brainSession   func() (brain.SessionState, bool)
	voice          bool // STT is configured; voice turns may run
	output         bool // TTS/player are configured
	setOutputMuted func(bool)
	clipboard      clipboardBackend
	sessionID      string
	inputDevice    string
	outputDevice   string
	liveSpeaker    LiveSpeakerController
	speakerNames   *speaker.NameMap
	// endUtterance commits the utterance the recogniser is reading, for a
	// talk key released; nil when the audio source cannot be ended on demand.
	endUtterance func() bool
	ctx          context.Context // pipeline lifetime; parent of every turn ctx
	wg           *sync.WaitGroup // tracks in-flight turns so shutdown can drain them
}

// startConversation attaches the model to a live pipeline and kicks off the
// first turn. The returned Cmd must be handed to the program.
func (m *conversationModel) startConversation(deps conversationDeps) tea.Cmd {
	m.stopCapture()
	m.deps = deps
	m.retireAdmissionRetryNotices()
	m.workPollGeneration++
	m.backgroundPauseGeneration++
	m.pendingPause = false
	m.backgroundPauseAcknowledged = false
	m.startedAt = time.Now()
	m.activity = nil
	m.voiceEnabled = deps.voice
	m.outputAvailable = deps.output
	m.sessionID = deps.sessionID
	m.inputDevice = deps.inputDevice
	m.outputDevice = deps.outputDevice
	m.liveSpeaker = deps.liveSpeaker
	m.speakerNames = deps.speakerNames
	m.bridge = newEventBridge(0)
	m.bridge.attach(deps.bus)

	// Mark the voice turn non-cancelable the moment the final transcript is
	// emitted — synchronously on the pipeline goroutine, before the bridge
	// drains UserInput into Update. Without this, Enter can still cancel
	// mid-think while turnState is still turnVoiceListening.
	if m.canCancelVoice == nil {
		m.canCancelVoice = &atomic.Bool{}
	}
	if m.talkKeyTurn == nil {
		m.talkKeyTurn = new(atomic.Bool)
	}
	m.talkKeyTurn.Store(false)
	if deps.bus != nil {
		gate := m.canCancelVoice
		events.Subscribe(deps.bus, func(events.UserInput) {
			gate.Store(false)
		})
	}

	cmds := []tea.Cmd{m.bridge.waitContext(deps.ctx), textarea.Blink, m.waitBackground(), m.backgroundRecoveryCmd()}
	m.appendActivity("session", shortSessionID(deps.sessionID), 0)
	if deps.voice {
		m.appendActivity("input", deviceLabel(deps.inputDevice), 0)
	}
	if deps.output {
		m.appendActivity("output", deviceLabel(deps.outputDevice), 0)
	}
	m.recoverWork(time.Now(), true)
	cmds = append(cmds, m.workProjectionPollCmd(), m.ensureVoiceTick(), m.mediaCommand())
	if deps.liveSpeaker != nil {
		if speaker.FromAppConfig(m.cfg).LiveActive() {
			deps.liveSpeaker.SetEnabled(true)
		}
		m.liveSpeakerStats = deps.liveSpeaker.Stats()
		m.liveSpeakerStatsKnown = true
		cmds = append(cmds, liveSpeakerStatsCmd(deps.liveSpeaker))
	}
	// Scripted meter for VHS/termcast demos — skips real mic/TTS turns.
	if demoWorkCueEnabled() {
		cmds = append(cmds, startDemoWorkCue(deps.bus), m.ensureVoiceTick())
		return tea.Batch(cmds...)
	}
	if demoVoiceAnimEnabled() {
		cmds = append(cmds, startDemoVoiceAnim(deps.bus), m.ensureVoiceTick())
		return tea.Batch(cmds...)
	}
	if demoThinkEnabled() {
		cmds = append(cmds, startDemoThink(deps.bus), m.ensureVoiceTick())
		return tea.Batch(cmds...)
	}
	if demoCaptureEnabled() {
		cmds = append(cmds, startDemoCapture(deps.bus), m.ensureVoiceTick())
		return tea.Batch(cmds...)
	}
	if m.voiceOn() {
		cmds = append(cmds, m.dispatchVoiceTurn())
	}
	return tea.Batch(cmds...)
}

func (m *conversationModel) voiceOn() bool {
	return m.deps.runner != nil && m.deps.voice && m.voiceEnabled
}

func (m *conversationModel) emit(e events.Event) {
	if m.deps.bus != nil {
		m.deps.bus.Emit(e)
	}
}

// toggleInputMuted flips voice-input pause for Ctrl+G. Absolute /mute and
// /unmute use setInputMuted so repeated commands do not invert state.
func (m *conversationModel) toggleInputMuted() tea.Cmd {
	return m.setInputMuted(m.voiceEnabled)
}

// setInputMuted pauses or resumes background voice turns. muted=true forces
// listening off; muted=false forces it on. Capture hardware may still be open
// while paused — only dispatch is gated. If the pipeline is only listening,
// muting cancels that turn immediately; a response that already owns the
// pipeline is allowed to finish and listening stays off.
func (m *conversationModel) setInputMuted(muted bool) tea.Cmd {
	defer func() {
		if m.capture != nil {
			m.capture.SetInputEnabled(m.voiceEnabled)
		}
	}()
	if !m.deps.voice {
		m.setStatus("Microphone unavailable", true)
		return nil
	}
	if muted {
		if !m.voiceEnabled {
			return nil
		}
		m.voiceEnabled = false
		// Stop the listening/hearing meter while input is paused.
		if m.voiceMode == anim.ModeListening || m.voiceMode == anim.ModeHearing || m.voiceMode == anim.ModeTranscribing {
			m.setVoiceMode(anim.ModeIdle)
			m.setStatus("", false)
		}
		m.emit(events.Info{Message: "Voice input paused."})
		if m.turnState == turnVoiceListening && m.canCancelVoice != nil && m.canCancelVoice.Load() {
			m.turnState = turnVoiceCanceling
			if m.turnCancel != nil {
				m.turnCancel()
			}
		}
		return nil
	}
	if m.voiceEnabled {
		return nil
	}
	m.voiceEnabled = true
	m.voiceFailures = 0
	m.emit(events.Info{Message: "Voice input resumed."})
	return m.reconcileBackground()
}

func (m *conversationModel) toggleOutputMuted() {
	if !m.outputAvailable {
		m.setStatus("Voice output unavailable", true)
		return
	}
	m.outputMuted = !m.outputMuted
	if m.deps.setOutputMuted != nil {
		m.deps.setOutputMuted(m.outputMuted)
	}
	state := "unmuted"
	if m.outputMuted {
		state = "muted"
	}
	m.emit(events.Info{Message: "Voice output " + state + "."})
}

// dispatchVoiceTurn starts one voice turn under a per-turn cancel context
// owned by the model (D1): submitting text while this turn is listening
// cancels it.
func (m *conversationModel) dispatchVoiceTurn() tea.Cmd {
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnVoiceListening
	m.backgroundPauseGeneration++
	m.backgroundPauseAcknowledged = false
	if m.canCancelVoice != nil {
		m.canCancelVoice.Store(true)
	}

	runner, wg := m.deps.runner, m.deps.wg
	noticeEpoch := m.noticeEpoch
	lifetime := m.deps.ctx
	return trackedCommand(m.deps.ctx, wg, func() tea.Msg {
		defer cancel()
		// A Cmd can execute after shutdown began (cancel happens before the
		// runtime waits on wg); never enter the pipeline on a dead context.
		if ctx.Err() != nil {
			return voiceTurnDoneMsg{lifetime: lifetime, err: ctx.Err(), noticeEpoch: noticeEpoch}
		}
		text, err := runner.RunTurn(ctx)
		return voiceTurnDoneMsg{lifetime: lifetime, text: text, err: err, noticeEpoch: noticeEpoch}
	})
}

func (m *conversationModel) dispatchTextTurn(text string) tea.Cmd {
	refs := append([]chatmedia.Ref(nil), m.media.pending...)
	m.media.pending = nil
	ctx, cancel := context.WithCancel(m.deps.ctx)
	m.turnCancel = cancel
	m.turnState = turnTextRunning

	runner, wg := m.deps.runner, m.deps.wg
	noticeEpoch := m.noticeEpoch
	lifetime := m.deps.ctx
	return trackedCommand(m.deps.ctx, wg, func() tea.Msg {
		defer cancel()
		if ctx.Err() != nil {
			return textTurnDoneMsg{lifetime: lifetime, input: text, err: ctx.Err(), noticeEpoch: noticeEpoch}
		}
		return textTurnDoneMsg{lifetime: lifetime, input: text, err: runner.RunTurnTextMode(chatmedia.WithInput(ctx, refs), text), noticeEpoch: noticeEpoch}
	})
}

func (m *conversationModel) retireAdmissionRetryNotices() {
	m.noticeEpoch++
}

func (m *conversationModel) emitAdmissionRetryNotice(err error, originalInput string, epoch uint64) {
	if epoch != 0 && epoch != m.noticeEpoch {
		return
	}
	notice, ok := app.BuildAdmissionRetryNotice(err, originalInput)
	if !ok {
		return
	}
	m.emit(events.Info{Message: notice.Message})
}

// resumeListening restarts the background voice turn when nothing else owns
// the pipeline.
func (m *conversationModel) resumeListening() tea.Cmd {
	// A queued or preparing persona selection owns the next pipeline boundary.
	// Do not let background capture, pending text, or compaction start around
	// it; the completion path calls resumeListening again after publication.
	if m.selectionPending() {
		if m.turnState == turnIdle {
			return m.startNextPersonaSelection()
		}
		return nil
	}
	// BackgroundReady is only a prompt. Every idle decision reads the current
	// inbox level so a lost edge, or a stale edge after another claimant, cannot
	// strand or replay work.
	if runner, ok := m.deps.runner.(backgroundRunner); ok {
		m.pendingBackground = runner.BackgroundPending()
	}
	if m.turnState == turnVoiceListening {
		// A canceled turn may have queued typed input or a talk-key turn. Its
		// done handler normally drains these first; do not ask capture to yield
		// while one of those user-owned turns is queued.
		if m.pendingText != "" || m.pendingCompact || (m.talk != nil && m.talk.pendingTurn) {
			return nil
		}
		if m.pendingBackground && !m.backgroundPauseAcknowledged {
			return m.requestBackgroundPause()
		}
		return nil
	}
	if m.turnState != turnIdle {
		return nil
	}
	// Captured speech owns the next pipeline turn even when typed input was
	// queued behind an earlier cancellation. Keep that text queued until the
	// retained voice turn completes.
	if m.voiceOn() {
		if m.hasCapturedInput() {
			return m.dispatchVoiceTurn()
		}
	}
	if m.pendingCompact {
		m.pendingCompact = false
		return m.dispatchCompact()
	}
	if m.pendingText != "" {
		text := m.pendingText
		m.pendingText = ""
		return m.submitText(text)
	}
	if m.pendingBackground {
		if cmd := m.dispatchBackground(); cmd != nil {
			m.pendingBackground = false
			return cmd
		}
	}
	if m.turnState == turnIdle && m.voiceOn() {
		return m.dispatchVoiceTurn()
	}
	return nil
}

// recoverTurnState unsticks cancel/response bookkeeping after screen changes
// drop async turn-done messages (e.g. /settings while a cancel was in flight).
func (m *conversationModel) recoverTurnState() tea.Cmd {
	switch m.turnState {
	case turnVoiceCanceling, turnVoiceResponding, turnTextRunning:
		// Drop orphaned cancel bookkeeping; a live pipeline turn may still be
		// finishing, but the TUI must accept input again. The next resume or
		// text submit is gated by turnIdle.
		if m.turnCancel != nil {
			m.turnCancel()
			m.turnCancel = nil
		}
		m.pendingText = ""
		m.pendingCompact = false
		m.turnState = turnIdle
		if m.canCancelVoice != nil {
			m.canCancelVoice.Store(false)
		}
	}
	return nil
}
