package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/internal/talkhotkey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio/playback"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

type screen int

const (
	screenLauncher screen = iota
	screenSettings
	screenConversation
	screenSessions
	screenMeetingSetup
	screenMeeting
	screenMeetingResults
	screenMeetingRoute
	screenAudiobook
	screenPickBook
	screenLibrary
	screenRemote
	screenPersonas
	screenWorkers
)

// meetingRoutePlan is the per-session routing choice from the start picker.
type meetingRoutePlan struct {
	Kind string // local | ask | dest | empty (use config mode)
	Dest meeting.Destination
}

// App is the top-level bubbletea model.
type App struct {
	screen screen
	// cfg is mutated only on the bubbletea Update goroutine (settings, audiobook,
	// library screens write its fields directly). It is never handed to a live
	// pipeline/brain goroutine: conversationRuntimeBuilder takes an independent
	// snapshot via config.Load() at conversation entry, so a running turn reads a
	// distinct *Config and never aliases these writes. Preserve that invariant —
	// pass a snapshot (config.Load or a value copy), not this pointer, to any
	// goroutine that reads config concurrently. See internal/config/config.go.
	cfg       *config.Config
	providers []discovery.ProviderInfo
	width     int
	height    int

	launcher       launcherModel
	settings       settingsModel
	conversation   conversationModel
	sessions       sessionsModel
	meetingSetup   meetingSetupModel
	meeting        meetingModel
	meetingResults meetingResultsModel
	meetingRoute   meetingRouteModel
	audiobook      audiobookModel
	pickBook       pickBookModel
	library        libraryModel
	remote         remoteModel
	personas       personasModel
	workers        workersModel

	launcherTicking bool
	launcherTickID  uint64

	// Conversation runtime wiring, set by Run before the program starts.
	builder RuntimeBuilder
	runCtx  context.Context
	// runCancel ends in-flight turns and builders. Quit calls it from the
	// update loop so shutdown starts before tea.Program.Run returns; run()
	// calls it again, which is a no-op.
	runCancel context.CancelFunc
	wg        *sync.WaitGroup
	progress  *eventBridge
	// slot owns the built runtime for shutdown cleanup even when a ready
	// message is dropped because the user quit mid-build.
	slot *runtimeSlot[*ConversationRuntime]
	// meetingSlot owns a recorder built after a quit, the same way slot owns a
	// conversation: Ctrl+C during the build means the ready message may never
	// be applied.
	meetingSlot *runtimeSlot[*MeetingRuntime]

	// Meeting recorder wiring (launcher → setup → recorder).
	meetingBuilder   MeetingBuilder
	meetingRT        *MeetingRuntime
	meetingRoutePlan meetingRoutePlan // chosen at setup; consumed after stop
	// Post-stop background state (WI-162bbb): a dest plan routes at capture
	// end, and diarization runs as a background job behind the results screen.
	meetingRouteAuto    bool   // dest plan consumed; route fired at capture end
	meetingRouteBanner  string // route result, shown on results and/or launcher
	meetingRouteErr     bool
	meetingAnalysisBusy bool // background diarization in flight
	// meetingSweep retries undelivered route plans at startup. Wired by run()
	// only, so unit-test Apps never touch the real meetings directory.
	meetingSweep tea.Cmd

	// Set once the conversation runtime is built; Run tears it down after
	// the program exits.
	runtime  *ConversationRuntime
	fatalErr error
	// hotkey is the native talk-key helper for the live conversation; nil
	// when disabled, unsupported, or not started yet.
	hotkey *talkhotkey.Client
	// control is the local endpoint a shortcut tool drives the talk key
	// through. It runs on every OS, alongside the helper where there is one.
	control *talkhotkey.Control

	// Settings can be opened from the launcher or from a live conversation.
	// Keep the origin so Esc/q returns to the screen the user came from.
	settingsReturnScreen screen
	settingsResumeVoice  bool
	settingsVoiceState   string
	personasResumeVoice  bool
	workersResumeVoice   bool

	// startInConversation skips the launcher (resume/continue).
	startInConversation bool
	// startingSessionID survives a recoverable persona-binding failure so the
	// chooser retries the same resumed session rather than creating a new one.
	startingSessionID string

	// clipboard is handed to the screens the app builds; nil → system clipboard.
	clipboard clipboardBackend

	quitting     bool
	returnToMenu bool
	menuDone     chan struct{}
}

// discoverProviders is the startup provider scan. It starts every installed
// provider CLI, so this package's tests replace it.
var discoverProviders = discovery.DiscoverProviders

// NewApp creates the TUI application.
func NewApp(cfg *config.Config) App {
	return newApp(cfg, discoverProviders(cfg), session.List())
}

// newApp initializes the UI from supplied discovery and session snapshots.
func newApp(cfg *config.Config, providers []discovery.ProviderInfo, saved []session.Session) App {
	savedSessions := resumableSessions(saved)
	conversation := newConversation(cfg.AgentName)
	conversation.cfg = cfg

	return App{
		screen:       screenLauncher,
		cfg:          cfg,
		providers:    providers,
		launcher:     newLauncher(cfg, providers, savedSessions),
		settings:     newSettings(cfg, providers),
		conversation: conversation,
		sessions:     newSessions(savedSessions),
		audiobook:    newAudiobook(cfg),
		pickBook:     newPickBook(cfg),
		library:      newLibrary(cfg),
		personas:     newPersonas(cfg),
	}
}

// withClipboard routes every copy the app and its screens make through b.
func (a App) withClipboard(b clipboardBackend) App {
	a.clipboard = b
	a.launcher.clipboard = b
	a.remote.clipboard = b
	a.conversation.deps.clipboard = b
	return a
}

func (a App) Init() tea.Cmd {
	if a.startInConversation {
		if a.meetingSweep != nil {
			return tea.Batch(func() tea.Msg { return startPipelineMsg{} }, a.meetingSweep)
		}
		return func() tea.Msg { return startPipelineMsg{} }
	}
	return a.meetingSweep
}

// switchScreen is a message to change screens.
type switchScreenMsg screen

// openWorkerMsg enters the worker manager at a specific live job when there is
// exactly one; an empty ID opens the list for the user to choose.
type openWorkerMsg struct{ jobID string }

// settingsDoneMsg returns from settings to the screen that opened it.
type settingsDoneMsg struct{}

// openSettingsSectionMsg opens the Settings screen landed on a specific
// section (e.g. the launcher's "Meeting settings" entry).
type openSettingsSectionMsg struct{ section settingsSection }

// StartDefaultsID starts the complete saved default Obey persona. The sentinel
// remains stable for existing launcher/desktop clients.
const StartDefaultsID = ":defaults"

// startPipelineMsg enters the conversation screen and builds the pipeline
// there (D2) — the TUI no longer exits to hand off.
type startPipelineMsg struct {
	sessionID string
	// personaID selects the session binding; empty = configured active persona.
	// StartDefaultsID (launcher "Obey (default)") resolves the saved Obey profile.
	// The persona picker (launcher "New conversation") sets this.
	personaID string
	// personaName labels the starting screen. The binding's real identity is
	// not resolved until the build finishes, so the picker passes what the
	// user just chose rather than the screen guessing from live config.
	personaName string
}

// quitMsg signals the app should exit.
type quitMsg struct{}

func quitCmd() tea.Cmd {
	return func() tea.Msg { return quitMsg{} }
}

// returnToMenuMsg saves and drains the session inside the existing terminal UI.
type returnToMenuMsg struct{}

// Update wraps the screen dispatch so the mouse claim follows the active
// screen. Conversation is the only screen that routes wheel events, so it is
// the only one that should cost the user native text selection.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	a.conversation.workDisplayHidden = a.screen != screenConversation
	from := a.screen
	model, cmd := a.update(msg)
	next, ok := model.(App)
	if !ok {
		return model, cmd
	}
	if claim := next.mouseClaimCmd(from); claim != nil {
		cmd = tea.Batch(cmd, claim)
	}
	if tick := next.syncLauncherAnimation(); tick != nil {
		cmd = tea.Batch(cmd, tick)
	}
	return next, cmd
}

// mouseClaimCmd enables cell-motion reporting on entering the conversation and
// releases it on leaving, so selection works unmodified everywhere else. Nil
// when the claim does not change. Bubble Tea disables the mouse on exit
// regardless, so a program that quits from conversation still restores it.
func (a App) mouseClaimCmd(from screen) tea.Cmd {
	was, now := from == screenConversation, a.screen == screenConversation
	if was == now {
		return nil
	}
	if now {
		if a.cfg == nil || !a.cfg.TUIMouseEnabled {
			return nil
		}
		return tea.EnableMouseCellMotion
	}
	// Release unconditionally: disabling a mouse that was never claimed is a
	// no-op, and it keeps the claim from outliving a mid-session opt-out.
	return tea.DisableMouse
}

func (a App) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if event, ok := msg.(busEventMsg); ok && event.source != nil && event.source != a.conversation.bridge {
		return a, nil
	}
	if a.returnToMenu {
		switch msg := msg.(type) {
		case menuReadyMsg, tea.WindowSizeMsg, quitMsg:
		case tea.KeyMsg:
			key := msg.String()
			if key != "ctrl+c" && key != "q" {
				return a, nil
			}
		default:
			return a, nil
		}
	}
	switch msg := msg.(type) {
	case menuReadyMsg:
		return a.onMenuReady(msg)
	case launcherTickMsg:
		if a.launcherTicking && msg.id == a.launcherTickID {
			a.launcherTicking = false
			a.launcher.logoFrame = (a.launcher.logoFrame + 1) % 3600
		}
		return a, nil
	case tea.KeyMsg:
		// A false third result means fall through to delegate; cmd is nil on
		// every such path and would be dropped here if that ever changed.
		next, cmd, done := a.onKey(msg)
		if done {
			return next, cmd
		}
		a = next
	case tea.WindowSizeMsg:
		a = a.onWindowSize(msg)
	case switchScreenMsg:
		return a.onSwitchScreen(msg)
	case openWorkerMsg:
		model, cmd := a.onSwitchScreen(switchScreenMsg(screenWorkers))
		app := model.(App)
		app.workers.openDetail(msg.jobID)
		return app, cmd
	case openSettingsSectionMsg:
		return a.onOpenSettingsSection(msg)
	case startMeetingMsg:
		return a.onStartMeeting(msg)
	case meetingReadyMsg:
		return a.onMeetingReady(msg)
	case meetingDoneMsg:
		return a.onMeetingDone(msg)
	case meetingResultsDoneMsg:
		return a.onMeetingResultsDone(msg)
	case meetingRouteReadyMsg:
		return a.onMeetingRouteReady(msg)
	case meetingRouteResultMsg:
		return a.onMeetingRouteResult(msg)
	case meetingAnalysisDoneMsg:
		return a.onMeetingAnalysisDone(msg)
	case meetingSweepDoneMsg:
		return a.onMeetingSweepDone(msg)
	case bookPickedMsg:
		return a.onBookPicked(msg)
	case libraryAudiobookMsg:
		return a.onLibraryAudiobook(msg)
	case workerSettingsSavedMsg:
		return a.onWorkerSettingsSaved(msg)
	case settingsDoneMsg:
		return a.onSettingsDone(msg)
	case microphoneSwitchedMsg:
		return a.onMicrophoneSwitched(msg)
	case voiceReloadedMsg:
		return a.onVoiceReloaded(msg)
	case startPipelineMsg:
		return a.onStartPipeline(msg)
	case buildStageMsg:
		return a.onBuildStage(msg)
	case progressClosedMsg:
		return a.onProgressClosed(msg)
	case runtimeReadyMsg:
		return a.onRuntimeReady(msg)
	case personaSelectionRequestMsg:
		return a.onPersonaSelectionRequest(msg)
	case personaSelectionResultMsg:
		return a.onPersonaSelectionResult(msg)
	case talkHotkeyEventMsg:
		return a.onTalkHotkeyEvent(msg)
	case workProjectionPollMsg, tea.FocusMsg:
		return a.onWorkProjectionPoll(msg)
	case talkHotkeyExitedMsg:
		return a.onTalkHotkeyExited(msg)
	case talkControlClosedMsg:
		return a.onTalkControlClosed(msg)
	case returnToMenuMsg:
		return a.onReturnToMenu(msg)
	case quitMsg:
		return a.onQuit(msg)
	case storageUsageMsg, storageMoveProgressMsg, storageMoveDoneMsg, storagePlanMsg, storageHangupMsg:
		return a.onStorage(msg)
	}

	return a.delegate(msg)
}

func (a App) applyPlaybackVolume(gain float64) {
	if a.runtime != nil && a.runtime.Pipeline != nil {
		playback.ApplyEngineVolume(a.runtime.Pipeline.Player, gain)
	}
}

func (a App) applyWorkersEnabled(enabled bool) {
	if a.runtime != nil && a.runtime.Pipeline != nil {
		a.runtime.Pipeline.SetWorkersEnabled(enabled)
	}
}

// syncRuntimeSelection applies a published control event only after the
// conversation model accepted its revision. SelectionSnapshot/SelectionConfig
// are read on the update thread; the event bridge never touches runtime
// identity or config directly.
func (a *App) syncRuntimeSelection(event events.PersonaSelection) {
	if a.runtime == nil || a.conversation.selectionRevision != event.Revision || a.conversation.selection.PersonaID != event.PersonaID {
		return
	}
	if a.runtime.SelectionSnapshot != nil {
		snapshot := a.runtime.SelectionSnapshot()
		if snapshot.Revision < event.Revision || snapshot.PersonaID != event.PersonaID {
			return
		}
	}
	a.runtime.PersonaID = event.PersonaID
	if event.DisplayName != "" {
		a.runtime.AgentName = event.DisplayName
	}
	if a.runtime.SelectionConfig != nil {
		if cfg := a.runtime.SelectionConfig(); cfg != nil {
			a.runtime.SessionCfg = cfg
			a.conversation.cfg = cfg
		}
	}
}

func voiceSettingsState(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	// Where speech runs is part of the voice state: switching computers has to
	// rebuild the TTS set (and its connection) exactly like switching engines.
	// The token is fingerprinted, never embedded: this string is app state that
	// any debug print or crash dump could carry.
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t",
		cfg.TTSProvider, cfg.TTSFallbackProvider, cfg.TTSVoice,
		cfg.QwenTTSMode, cfg.QwenTTSVoice, cfg.QwenTTSLanguage,
		config.TTSPlaceOf(cfg), cfg.TTSWorkerURL, secretFingerprint(cfg.TTSWorkerToken), cfg.TTSLanStrict)
}

// secretFingerprint reduces a secret to something that changes when the secret
// does and reveals nothing about it. A rotated token still triggers a voice
// reload; the token itself never leaves config.
func secretFingerprint(secret string) string {
	if secret == "" {
		return "unset"
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:8])
}

func (a App) View() string {
	switch a.screen {
	case screenLauncher:
		return a.launcher.View()
	case screenSettings:
		return a.settings.View()
	case screenConversation:
		return a.conversation.View()
	case screenSessions:
		return a.sessions.View()
	case screenMeetingSetup:
		return a.meetingSetup.View()
	case screenMeeting:
		return a.meeting.View()
	case screenMeetingResults:
		return a.meetingResults.View()
	case screenMeetingRoute:
		return a.meetingRoute.View()
	case screenAudiobook:
		return a.audiobook.View()
	case screenPickBook:
		return a.pickBook.View()
	case screenLibrary:
		return a.library.View()
	case screenRemote:
		return a.remote.View()
	case screenPersonas:
		return a.personas.View()
	case screenWorkers:
		return a.workers.View()
	default:
		return ""
	}
}
