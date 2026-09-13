package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

const conversationInputHeight = 3

// voiceTickInterval drives the conversation meter animation (~10 fps).
const voiceTickInterval = 100 * time.Millisecond

// voicePanelRows is the vertical space reserved for the compact voice EQ strip
// under the header (not a tall art panel).
const voicePanelRows = 5

// voiceTickMsg advances ambient voice animations.
type voiceTickMsg time.Time

// conversationModel renders the live conversation screen: a scrollable
// transcript viewport, a persistent status indicator, and an always-focused
// input line. It renders purely from injected state — turn dispatch and event
// bus wiring are layered on by later slices.
type conversationModel struct {
	agentName         string
	cfg               *config.Config
	selection         events.PersonaSelection
	selectionRevision uint64
	selectionRuntime  *ConversationRuntime
	selectionConfig   func() *config.Config
	selectionCtx      context.Context
	selectionCancel   context.CancelFunc
	selectionSession  string
	selectionEpoch    uint64
	selectionRequest  uint64
	selectionBusy     bool
	selectionQueue    []string

	width  int
	height int
	ready  bool

	viewport         viewport.Model
	activityViewport viewport.Model
	input            textarea.Model

	transcript []string
	items      []chatItem
	// showThink keeps finalized thinking blocks in the thread. Live streaming
	// still paints thinking regardless; this flag only rebuilds stored turns.
	showThink bool
	activity  []activityEntry
	status    string
	statusErr bool
	jobStatus string
	// jobs is what the chip knows about each queued or running job, keyed by
	// id so a spawn queued behind a running worker cannot relabel it;
	// jobOrder keeps them in queue order.
	jobs     map[string]*jobView
	jobOrder []string
	// work folds a job's per-call tool events into rows; jobItems maps a job
	// to the one transcript item those rows rewrite. See work.go.
	work                    *workfold.Tracker
	jobItems                map[string]int
	completedJobItems       map[string]int
	workScope               work.WorkScope
	workDisplayHidden       bool
	workDisplay             workDisplayState
	workStripExpiryDeadline time.Time
	workStripExpiryScope    work.WorkScope
	workActivity            bool
	workFilter              string
	workStripReservedRows   int
	workStripRenderAt       time.Time
	workRevision            uint64
	lastWorkPoll            time.Time
	workPollGeneration      uint64
	journalIncomplete       bool
	workResultItems         map[string]int
	workResultJobs          map[string]string
	// startup is the waiting state while the runtime is built; see
	// conversation_startup.go.
	startup startupState
	// streamingThink accumulates ThinkingDelta text for the in-progress turn,
	// painted dim and labeled "thinking". Copied into the matching chatItem
	// on ResponseReady; hidden in the thread until showThink is on. Never
	// copied into clipboard yank or model history.
	streamingThink string
	// streamingAgent accumulates ResponseDelta text for the in-progress agent
	// turn, rendered live beneath the transcript until ResponseReady finalizes
	// it into transcript. Empty when no turn is streaming.
	streamingAgent string
	// liveThink splits <think> regions out of ResponseDelta when the brain
	// leaked them onto the speakable stream instead of ThinkingDelta.
	liveThink textclean.ThinkFilter
	// pendingUserEcho is the last optimistically rendered user turn text. When
	// the pipeline later emits matching UserInput, the transcript skip avoids
	// a duplicate bubble (typed submit shows immediately; voice still waits
	// for the bus event).
	pendingUserEcho string
	// lastAssistantText is the plain body of the latest agent reply (no bubble
	// chrome) for /copy and idle ctrl+c / ctrl+y yank.
	lastAssistantText string
	// plainTurns mirrors user/assistant bodies for /copy all (scrollback chrome
	// is ANSI-styled and not clipboard-friendly).
	plainTurns []plainChatTurn
	// lastIdleCopyAt arms a second idle Ctrl+C as quit after a yank (review:
	// first Ctrl+C must not remove the only non-slash exit forever).
	lastIdleCopyAt time.Time

	bridge      *eventBridge
	lastMetrics events.TurnMetrics

	deps       conversationDeps
	turnState  turnState
	turnCancel func()
	// canCancelVoice is true only while STT is still listening (before the
	// final transcript). Updated synchronously from the bus handler on the
	// pipeline goroutine so Enter cannot race the async bridge drain of
	// UserInput into turnVoiceResponding. Pointer so Bubble Tea model copies
	// share one gate with the bus subscription.
	canCancelVoice            *atomic.Bool
	pendingText               string
	pendingBackground         bool
	pendingPause              bool
	backgroundPauseGeneration uint64
	// backgroundPauseAcknowledged keeps recovery from requesting the same
	// capture pause repeatedly while the ordinary voice turn drains.
	backgroundPauseAcknowledged bool
	// pendingCompact queues a /compact requested while a cancelable turn was
	// in flight; drained by the turn-done handlers, winning over pendingText.
	pendingCompact  bool
	voiceEnabled    bool
	outputMuted     bool
	outputAvailable bool
	activityFocused bool
	// followChat / followActivity track whether the user is pinned to the
	// tail. bubbles/viewport AtBottom() alone is not enough: reflow (voice
	// panel open/close) shrinks height without adjusting YOffset, which
	// falsely reports "scrolled up" and freezes auto-scroll for the rest of
	// the session.
	followChat            bool
	followActivity        bool
	startedAt             time.Time
	sessionID             string
	inputDevice           string
	outputDevice          string
	voiceFailures         int
	quitting              bool
	liveSpeaker           LiveSpeakerController
	liveSpeakerStats      speaker.LiveStats
	liveSpeakerStatsKnown bool
	// stickyLive holds last good speaker-N across brief empty stats (chat parity).
	stickyLive stickyLiveLabel
	// speakerNames maps speaker-N → display name (session-local renames).
	speakerNames *speaker.NameMap
	// talk is the system-wide talk key for this session; nil when none.
	talk *talkState
	// nameSummon is this session's name gate; nil when name summon is off or
	// the front end refused it. See summon.go.
	nameSummon *summonGate
	// talkKeyTurn is true while the talk key owns the in-flight turn, so the
	// name gate lets it through. Pointer so Bubble Tea model copies and the
	// pipeline goroutine share one flag, same as canCancelVoice.
	talkKeyTurn *atomic.Bool

	commandQuery     string
	commandSelection int
	editor           editorBuffer
	vim              vimState

	// Voice meter animation (festival-style multi-frame art + level bar).
	voiceMode     anim.Mode
	voiceFrame    int
	inputLevel    float64 // smoothed mic energy 0..1
	outputLevel   float64 // reserved; speaking uses synthetic envelope for now
	reducedMotion bool
	voiceTicking  bool
}

func newConversation(agentName string) conversationModel {
	if agentName == "" {
		agentName = "Obey"
	}

	input := textarea.New()
	input.Placeholder = "Type a message or / for commands…"
	input.CharLimit = 1000
	input.ShowLineNumbers = false
	input.KeyMap.InsertNewline.SetKeys("ctrl+j", "ctrl+enter", "alt+enter", "shift+enter")
	input.KeyMap.InsertNewline.SetHelp("ctrl+j", "new line")
	input.SetHeight(conversationInputHeight)
	input.Focus()

	return conversationModel{
		agentName:         agentName,
		input:             input,
		canCancelVoice:    &atomic.Bool{},
		followChat:        true,
		followActivity:    true,
		work:              workfold.New(),
		jobItems:          make(map[string]int),
		completedJobItems: make(map[string]int),
		workResultItems:   make(map[string]int),
		workResultJobs:    make(map[string]string),
		startedAt:         time.Now(),
		reducedMotion:     anim.ReducedMotion(),
	}
}

type personaSelectionRequestMsg struct{ id string }

type personaSelectionResultMsg struct {
	id              string
	selection       events.PersonaSelection
	cfg             *config.Config
	err             error
	runtime         *ConversationRuntime
	sessionID       string
	runtimeEpoch    uint64
	requestSequence uint64
}

// configureSelection makes the conversation owner the lifetime of selection
// work. The Personas screen is recreated when it is reopened, but this state
// remains attached to the live conversation and its runtime.
func (m *conversationModel) configureSelection(rt *ConversationRuntime) {
	if m.selectionCancel != nil {
		m.selectionCancel()
	}
	m.selectionRuntime = rt
	m.selection = events.PersonaSelection{}
	m.selectionRevision = 0
	m.selectionConfig = nil
	if rt != nil {
		m.selectionConfig = rt.SelectionConfig
	}
	m.selectionEpoch++
	ctx := m.deps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.selectionCtx, m.selectionCancel = context.WithCancel(ctx)
	m.selectionSession = m.sessionID
	m.selectionBusy = false
	m.selectionQueue = nil
}

func (m *conversationModel) cancelSelection() {
	if m.selectionCancel != nil {
		m.selectionCancel()
		m.selectionCancel = nil
	}
	m.selectionRuntime = nil
	m.selectionBusy = false
	m.selectionQueue = nil
	m.selectionEpoch++
}

func (m *conversationModel) selectionPending() bool {
	return m.selectionBusy || len(m.selectionQueue) > 0
}

// enqueuePersonaSelection is the only admission point for in-chat choices.
// It leaves the request queued while a turn owns the pipeline and starts it
// only after that turn has returned to idle.
func (m *conversationModel) enqueuePersonaSelection(id string) tea.Cmd {
	if m.selectionRuntime == nil || m.selectionRuntime.SelectPersona == nil {
		return nil
	}
	m.selectionQueue = append(m.selectionQueue, id)
	if m.turnState != turnIdle || m.selectionBusy {
		return nil
	}
	return m.startNextPersonaSelection()
}

func (m *conversationModel) startNextPersonaSelection() tea.Cmd {
	if m.selectionRuntime == nil || m.selectionRuntime.SelectPersona == nil || m.selectionBusy || m.turnState != turnIdle || len(m.selectionQueue) == 0 {
		return nil
	}
	m.selectionBusy = true
	m.selectionRequest++
	runtime, callback := m.selectionRuntime, m.selectionRuntime.SelectPersona
	epoch, request, session := m.selectionEpoch, m.selectionRequest, m.selectionSession
	ctx := m.selectionCtx
	if ctx == nil {
		ctx = context.Background()
	}
	id := m.selectionQueue[0]
	wg := m.deps.wg
	if wg != nil {
		wg.Add(1)
	}
	return func() tea.Msg {
		if wg != nil {
			defer wg.Done()
		}
		selection, cfg, err := callback(ctx, id)
		return personaSelectionResultMsg{id: id, selection: selection, cfg: cfg, err: err,
			runtime: runtime, sessionID: session, runtimeEpoch: epoch, requestSequence: request}
	}
}

// finishPersonaSelection accepts only the result for the current runtime,
// session, epoch, request, and FIFO head. It returns whether App may publish
// the result and the next selection command, if any.
func (m *conversationModel) finishPersonaSelection(msg personaSelectionResultMsg) (bool, tea.Cmd) {
	if !m.selectionBusy || m.selectionRuntime != msg.runtime || m.selectionSession != msg.sessionID || m.selectionEpoch != msg.runtimeEpoch || m.selectionRequest != msg.requestSequence || len(m.selectionQueue) == 0 || m.selectionQueue[0] != msg.id {
		return false, nil
	}
	m.selectionQueue = m.selectionQueue[1:]
	m.selectionBusy = false
	return true, m.startNextPersonaSelection()
}

// applySelectionSnapshot is called only by the Bubble Tea update goroutine.
// It advances display state monotonically so a late selection event cannot
// relabel the current session with an older binding.
func (m *conversationModel) applySelectionSnapshot(selection events.PersonaSelection) {
	if m.sessionID != "" && selection.SessionID != "" && selection.SessionID != m.sessionID {
		return
	}
	if selection.Revision < m.selectionRevision {
		return
	}
	if selection.Revision == m.selectionRevision && m.selection.PersonaID != "" && selection.BindingHash != "" && selection.BindingHash != m.selection.BindingHash {
		return
	}
	m.selection = selection
	m.selectionRevision = selection.Revision
	if selection.DisplayName != "" {
		m.agentName = selection.DisplayName
	}
}

func voiceTickCmd() tea.Cmd {
	return tea.Tick(voiceTickInterval, func(t time.Time) tea.Msg { return voiceTickMsg(t) })
}

type activityEntry struct {
	at      time.Duration
	stage   string
	detail  string
	elapsed time.Duration
}

// plainChatTurn is a clipboard-friendly transcript line without bubble styling.
type plainChatTurn struct {
	role    string // "user" or "assistant"
	text    string
	speaker string // optional display label for multi-speaker user turns
}

func (m conversationModel) Update(msg tea.Msg) (next conversationModel, cmd tea.Cmd) {
	// Schedule only after the returned state has captured its current scope and
	// deadline, including snapshots recovered while voice animation is idle.
	defer func() {
		if expiry := next.scheduleWorkStripExpiry(); expiry != nil {
			cmd = tea.Batch(cmd, expiry)
		}
	}()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, m.ensureVoiceTick()
	case tea.FocusMsg:
		m.recoverWork(time.Now(), true)
		return m, nil
	case workProjectionPollMsg:
		if msg.generation != m.workPollGeneration {
			return m, nil
		}
		m.recoverWork(time.Now(), false)
		return m, m.workProjectionPollCmd()

	case startupTickMsg:
		if !m.startup.tick() {
			return m, nil
		}
		return m, startupTickCmd(m.reducedMotion)

	case voiceTickMsg:
		m.recoverWork(time.Time(msg), false)
		m.voiceFrame++
		// Decay mic energy so the meter settles when speech pauses.
		m.inputLevel *= 0.82
		if m.inputLevel < 0.02 {
			m.inputLevel = 0
		}
		m.outputLevel *= 0.88
		if m.outputLevel < 0.02 {
			m.outputLevel = 0
		}
		if !m.shouldAnimateVoice() {
			m.voiceTicking = false
			return m, nil
		}
		return m, voiceTickCmd()

	case demoVoiceAnimStartedMsg, demoThinkStartedMsg:
		return m, m.ensureVoiceTick()

	case busEventMsg:
		m.handleEvent(msg.event)
		return m, tea.Batch(m.rearm(), m.ensureVoiceTick())

	case workStripExpiryMsg:
		if msg.scope != m.workDisplay.snapshot.Scope ||
			msg.deadline != m.workDisplay.terminalUntil ||
			time.Now().Before(msg.deadline) {
			return m, nil
		}
		m.reflowAt(msg.deadline)
		return m, nil

	case backgroundReadyMsg:
		// Listening remains owned by STT until its reply or no-speech timeout;
		// canceling it here could discard an utterance that is just beginning.
		m.pendingBackground = true
		return m, tea.Batch(m.resumeListening(), m.waitBackground())
	case backgroundRecoveryMsg:
		return m, tea.Batch(m.reconcileBackground(), m.backgroundRecoveryCmd())
	case backgroundPauseDoneMsg:
		if msg.generation != m.backgroundPauseGeneration {
			return m, nil
		}
		m.pendingPause = false
		if msg.err == nil && msg.acknowledged {
			m.backgroundPauseAcknowledged = true
		}
		return m, nil
	case voiceTurnDoneMsg:
		return m, tea.Batch(m.handleVoiceTurnDone(msg), m.ensureVoiceTick())

	case textTurnDoneMsg:
		return m, tea.Batch(m.handleTextTurnDone(msg), m.ensureVoiceTick())

	case compactDoneMsg:
		return m, tea.Batch(m.handleCompactDone(msg), m.ensureVoiceTick())

	case voiceRetryMsg:
		return m, tea.Batch(m.handleVoiceRetry(), m.ensureVoiceTick())

	case talkHotkeyEventMsg:
		return m, tea.Batch(m.handleTalkHotkey(msg.ev), m.ensureVoiceTick())

	case talkHotkeyExitedMsg:
		m.talkExited()
		return m, nil

	case liveSpeakerStatsMsg:
		m.liveSpeakerStats = msg.stats
		m.liveSpeakerStatsKnown = true
		return m, liveSpeakerStatsCmd(m.liveSpeaker)

	case clipboardPasteMsg:
		if msg.err != nil {
			m.commandError("paste failed: " + msg.err.Error())
			return m, nil
		}
		m.insertClipboardText(msg.text)
		return m, nil

	case tea.MouseMsg:
		// The viewport already implements wheel acceleration and bounds. Route
		// only vertical wheel events here so clicks and drags do not interfere
		// with the always-focused composer.
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			// Strip shift: the viewport reads it as horizontal scroll, which is
			// a no-op at the default horizontal step. Terminals that use shift
			// as the selection modifier would otherwise give the user neither
			// selection nor scrolling.
			msg.Shift = false
			return m.updateScroll(msg)
		}
		return m, nil

	case tea.KeyMsg:
		if m.startup.active {
			// Nothing is wired yet: a turn typed here would be dispatched at a
			// nil runner. ctrl+c still quits (the app handles it above).
			return m, nil
		}
		m.syncEditorFromTextarea()
		// Page keys scroll history. Editing keys stay with the always-focused
		// composer so multiline drafting never needs a mode switch.
		switch msg.String() {
		case "ctrl+v", "ctrl+shift+v", "shift+insert":
			return m, readClipboard(m.clipboard())
		case "ctrl+y":
			// Always available: yank last assistant reply without fighting quit.
			m.copyLastAssistant()
			return m, nil
		case "ctrl+a":
			m.selectAll()
			return m, nil
		case "ctrl+x":
			if m.editor.selectionActive() {
				m.cutSelection()
				return m, nil
			}
		case "ctrl+g":
			return m, m.toggleInputMuted()
		case "ctrl+o":
			m.toggleOutputMuted()
			return m, nil
		case "ctrl+t":
			if m.workActivity {
				m.workActivity = false
			}
			m.activityFocused = !m.activityFocused
			m.refreshActivity()
			return m, nil
		case "ctrl+k":
			m.toggleShowThink()
			return m, nil
		case "esc":
			if m.vim.mode == vimVisual {
				m.enterVimNormal()
				return m, nil
			}
			if m.editor.selectionActive() {
				m.editor.clearSelection()
				return m, nil
			}
			if m.activityFocused && m.workFilter != "" {
				m.workFilter = ""
				m.refreshActivity()
				return m, nil
			}
			if m.activityFocused {
				m.activityFocused = false
				m.workActivity = false
				m.refreshActivity()
				return m, nil
			}
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			return m.updateScroll(msg)
		case "ctrl+home":
			m.activeViewport().GotoTop()
			m.syncFollowFromViewports()
			return m, nil
		case "ctrl+end":
			m.activeViewport().GotoBottom()
			m.syncFollowFromViewports()
			return m, nil
		case "home":
			// Activity always jumps; Chat only when the composer is empty so
			// bare Home/End still navigate the transcript without fighting
			// line-start/end editing while drafting.
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoTop()
				m.syncFollowFromViewports()
				return m, nil
			}
		case "end":
			if m.activityFocused || m.input.Value() == "" {
				m.activeViewport().GotoBottom()
				m.syncFollowFromViewports()
				return m, nil
			}
		case "up", "down":
			// Same rule Home/End already follow: an empty composer scrolls the
			// transcript, a draft moves the cursor.
			//
			// This is what keeps the wheel usable now that Samantha leaves the
			// mouse to the terminal. In the alternate screen a terminal with no
			// mouse claim translates wheel turns into arrow keys, and an idle
			// composer is exactly the moment a wheel turn means "scroll the
			// chat" rather than "move my cursor".
			if m.activityFocused || m.input.Value() == "" {
				return m.updateScroll(msg)
			}
		}

		if handled, cmd := m.handleCommandPaletteKey(msg); handled {
			return m, cmd
		}
		if msg.String() == "enter" {
			return m, m.handleSubmit()
		}
		// Do not bind bare "y" for yank: empty-composer "y" steals the first
		// letter of ordinary messages (yes/you/yeah). Use Ctrl+Y, /copy, or
		// idle Ctrl+C instead. Vim NORMAL keeps its own y/yy operators below.
		if m.vim.enabled {
			switch m.vim.mode {
			case vimNormal:
				return m, m.handleVimNormalKey(msg)
			case vimVisual:
				return m, m.handleVimVisualKey(msg)
			case vimInsert:
				if msg.String() == "esc" {
					m.enterVimNormal()
					return m, nil
				}
			}
		}
		// Reaching here means the key is going into the composer as typed
		// text — the moment "type to barge in" promises silence.
		m.keystrokeBargeIn(msg)
		return m, m.updateComposer(msg)

	default:
		// Non-key messages (notably textarea.Blink ticks) must reach the
		// composer so the cursor blink chain stays alive.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m *conversationModel) setSize(width, height int) {
	m.width = width
	m.height = height
	m.reflow()
}

func (m *conversationModel) reflow() {
	m.reflowAt(time.Now())
}

func (m *conversationModel) reflowAt(now time.Time) {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Capture follow intent before height changes invalidate AtBottom().
	m.syncFollowFromViewports()
	inputHeight := conversationInputHeight
	if m.height < 12 {
		inputHeight = 1
	}
	// Header + rule + label + input border + footer consume six rows in
	// addition to the textarea's own height. An active voice panel adds a few
	// more. Command matches consume only the rows currently available above
	// the composer.
	m.workStripReservedRows = m.workStripRows(now)
	m.workStripRenderAt = now
	chrome := 6 + m.workStripReservedRows
	if m.voiceMode != anim.ModeIdle && m.height >= 14 {
		chrome += voicePanelRows
	}
	vpHeight := max(m.height-inputHeight-chrome-m.commandPaletteRows(), 1)
	if !m.ready {
		m.viewport = viewport.New(max(m.width, 1), vpHeight)
		m.activityViewport = viewport.New(max(m.width, 1), vpHeight)
		m.ready = true
		m.followChat = true
		m.followActivity = true
	} else {
		m.viewport.Width = max(m.width, 1)
		m.viewport.Height = vpHeight
		m.activityViewport.Width = max(m.width, 1)
		m.activityViewport.Height = vpHeight
	}
	m.input.SetWidth(max(m.width-4, 1))
	m.input.SetHeight(inputHeight)
	m.refreshContent()
	m.refreshActivity()
	m.applyFollow()
}

// syncFollowFromViewports updates the sticky follow flags from the current
// scroll position. Call before mutating content or viewport height so a
// reflow cannot flip "at bottom" into a permanent freeze.
func (m *conversationModel) syncFollowFromViewports() {
	if !m.ready {
		m.followChat = true
		m.followActivity = true
		return
	}
	m.followChat = m.viewport.AtBottom()
	m.followActivity = m.activityViewport.AtBottom()
}

// applyFollow pins each pane to the tail when its follow flag is set.
func (m *conversationModel) applyFollow() {
	if !m.ready {
		return
	}
	if m.followChat {
		m.viewport.GotoBottom()
	}
	if m.followActivity {
		m.activityViewport.GotoBottom()
	}
	m.markVisibleWorkResults()
}

func (m *conversationModel) activeViewport() *viewport.Model {
	if m.activityFocused {
		return &m.activityViewport
	}
	return &m.viewport
}

func (m conversationModel) updateScroll(msg tea.Msg) (conversationModel, tea.Cmd) {
	var cmd tea.Cmd
	if m.activityFocused {
		m.activityViewport, cmd = m.activityViewport.Update(msg)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
	}
	// Manual scroll owns follow: pgup freezes the tail; jumping to bottom
	// (End / ctrl+end) re-enables auto-follow for new messages.
	m.syncFollowFromViewports()
	m.markVisibleWorkResults()
	return m, cmd
}

// appendTranscript adds rendered lines to the transcript, following the tail
// when followChat is set. Sticky flags (not live AtBottom) own follow intent
// so a prior reflow cannot freeze the chat mid-session.
func (m *conversationModel) appendTranscript(lines ...string) {
	for _, line := range lines {
		m.items = append(m.items, chatItem{kind: chatItemSys, sys: line})
	}
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) clearTranscript() {
	m.transcript = nil
	m.items = nil
	m.jobItems = nil
	m.completedJobItems = nil
	m.workResultItems = nil
	m.workResultJobs = nil
	m.workScope = work.WorkScope{}
	m.workRevision = 0
	m.pendingUserEcho = ""
	m.lastAssistantText = ""
	m.plainTurns = nil
	m.streamingThink = ""
	m.streamingAgent = ""
	m.liveThink = textclean.ThinkFilter{}
	m.refreshContent()
	if m.followChat {
		m.viewport.GotoBottom()
	}
}

// rememberAssistant keeps the latest plain agent reply for clipboard yank.
func (m *conversationModel) rememberAssistant(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.lastAssistantText = text
	m.plainTurns = append(m.plainTurns, plainChatTurn{role: "assistant", text: text})
}

// rememberUser keeps a plain user turn for /copy all. speaker is an optional
// display label (renamed speaker-N); empty falls back to "You:" when formatting.
func (m *conversationModel) rememberUser(text string, speaker ...string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	label := ""
	if len(speaker) > 0 {
		label = strings.TrimSpace(speaker[0])
	}
	m.plainTurns = append(m.plainTurns, plainChatTurn{role: "user", text: text, speaker: label})
}

// copyLastAssistant writes the latest agent reply to the system clipboard.
// When fromIdleCtrlC is true, arm a second Ctrl+C within idleCopyQuitWindow as quit.
func (m *conversationModel) copyLastAssistant(fromIdleCtrlC ...bool) {
	text := m.latestReadableWorkResult()
	if text == "" {
		text = strings.TrimSpace(m.lastAssistantText)
	}
	if text == "" {
		text = strings.TrimSpace(m.streamingAgent)
	}
	if text == "" {
		m.commandError("nothing to copy yet")
		return
	}
	if err := m.clipboard().WriteAll(text); err != nil {
		m.commandError("copy failed: " + err.Error())
		return
	}
	if len(fromIdleCtrlC) > 0 && fromIdleCtrlC[0] {
		m.lastIdleCopyAt = time.Now()
		m.commandNotice(fmt.Sprintf("Copied last reply (%d characters) · Ctrl+C again to quit · /quit also exits", runeLen(text)))
		return
	}
	m.lastIdleCopyAt = time.Time{}
	m.commandNotice(fmt.Sprintf("Copied last reply (%d characters)", runeLen(text)))
}

// idleCopyQuitWindow is how long a second idle Ctrl+C counts as quit after yank.
const idleCopyQuitWindow = 1500 * time.Millisecond

// copyPlainChat writes the full plain-text conversation to the clipboard.
func (m *conversationModel) copyPlainChat() {
	if len(m.plainTurns) == 0 && len(m.workResultItems) == 0 {
		// Fall back to last reply alone when history was not tracked (edge).
		m.copyLastAssistant()
		return
	}
	var b strings.Builder
	for i, turn := range m.plainTurns {
		if i > 0 {
			b.WriteString("\n\n")
		}
		switch turn.role {
		case "assistant":
			name := m.agentName
			if name == "" {
				name = "Assistant"
			}
			b.WriteString(name)
			b.WriteString(": ")
		default:
			if turn.speaker != "" {
				b.WriteString(turn.speaker)
				b.WriteString(": ")
			} else {
				b.WriteString("You: ")
			}
		}
		b.WriteString(turn.text)
	}
	for _, text := range m.readableWorkResults() {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	text := b.String()
	if err := m.clipboard().WriteAll(text); err != nil {
		m.commandError("copy failed: " + err.Error())
		return
	}
	m.commandNotice(fmt.Sprintf("Copied conversation (%d characters)", runeLen(text)))
}

// composerIdle reports an empty draft with no active selection — used by
// idle yank bindings so they never steal text the user is still editing.
func (m conversationModel) composerIdle() bool {
	return strings.TrimSpace(m.input.Value()) == "" && !m.editor.selectionActive()
}

// echoUserTurn renders the user's message into Chat immediately. Typed
// submits call this so clearing the composer never looks like the message
// vanished while a voice turn cancels or the brain is reached. Matching
// UserInput events skip a second bubble via pendingUserEcho.
func (m *conversationModel) echoUserTurn(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.activityFocused = false
	m.followChat = true
	m.pendingUserEcho = text
	m.rememberUser(text)
	m.appendTranscript(renderUserTurn(text))
}

func (m *conversationModel) setStatus(text string, isErr bool) {
	m.status = text
	m.statusErr = isErr
}

// setVoiceMode updates the animated meter state and reflows when the panel
// appears or disappears so the viewport height stays correct.
func (m *conversationModel) setVoiceMode(mode anim.Mode) {
	prev := m.voiceMode
	m.voiceMode = mode
	if mode == anim.ModeIdle {
		m.inputLevel = 0
	}
	if (prev == anim.ModeIdle) != (mode == anim.ModeIdle) && m.ready {
		m.reflow()
	}
}

func (m *conversationModel) shouldAnimateVoice() bool {
	if m.reducedMotion {
		return false
	}
	return m.voiceMode != anim.ModeIdle
}

// ensureVoiceTick starts the animation loop once while a voice mode is active.
func (m *conversationModel) ensureVoiceTick() tea.Cmd {
	if !m.shouldAnimateVoice() || m.voiceTicking {
		return nil
	}
	m.voiceTicking = true
	return voiceTickCmd()
}

func (m conversationModel) animStyles() anim.Styles {
	return voiceAnimStyles()
}

func (m conversationModel) voiceLevel() float64 {
	switch m.voiceMode {
	case anim.ModeHearing, anim.ModeListening, anim.ModeTranscribing:
		return m.inputLevel
	case anim.ModeSpeaking, anim.ModeSynthesizing:
		if m.outputLevel > 0 {
			return m.outputLevel
		}
		return 0
	default:
		return 0
	}
}

func (m conversationModel) inputBorderColor() lipgloss.Color {
	switch m.voiceMode {
	case anim.ModeHearing:
		return colorHearing
	case anim.ModeListening:
		return colorAccent
	case anim.ModeSpeaking, anim.ModeSynthesizing:
		return colorSpeak
	case anim.ModeError:
		return colorError
	case anim.ModeThinking, anim.ModeTranscribing:
		return colorStatus
	default:
		return colorAccent
	}
}

func (m *conversationModel) refreshContent() {
	if !m.ready {
		return
	}
	parts := make([]string, 0, len(m.items))
	for _, it := range m.items {
		if s := it.render(m.showThink); s != "" {
			parts = append(parts, s)
		}
	}
	m.transcript = parts
	content := strings.Join(parts, "\n")
	// Render the in-progress turn live beneath the finalized transcript so
	// thinking and speakable stream in before ResponseReady lands.
	if m.streamingThink != "" || m.streamingAgent != "" {
		live := renderAgentBubble(m.agentName, m.streamingThink, m.streamingAgent, m.streamingThink != "")
		if content != "" {
			content += "\n" + live
		} else {
			content = live
		}
	}
	// lipgloss wraps to width so long turns don't overflow the viewport.
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
}

// appendStreamingDelta grows the in-progress agent turn and re-renders,
// following the tail when followChat is set.
func (m *conversationModel) appendStreamingDelta(text string) {
	m.streamingAgent += text
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) appendStreamingThink(text string) {
	first := m.streamingThink == ""
	m.streamingThink += text
	if first {
		m.appendActivity("model", "thinking", 0)
	}
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) appendActivity(stage, detail string, elapsed time.Duration) {
	m.activity = append(m.activity, activityEntry{
		at: time.Since(m.startedAt), stage: stage, detail: detail, elapsed: elapsed,
	})
	if len(m.activity) > 500 {
		m.activity = append([]activityEntry(nil), m.activity[len(m.activity)-500:]...)
	}
	m.refreshActivity()
	m.applyFollow()
}

func (m *conversationModel) refreshActivity() {
	if !m.ready {
		return
	}
	if m.workActivity {
		m.activityViewport.SetContent(lipgloss.NewStyle().Width(max(m.activityViewport.Width, 1)).Render(m.renderWorkActivity()))
		return
	}
	var lines []string
	for _, entry := range m.activity {
		// Session offset as a clock (20:50.5), not raw seconds (1250.5s) —
		// long conversations make absolute seconds unreadable at a glance.
		when := formatActivityAt(entry.at)
		line := when + "  " + entry.stage
		if entry.detail != "" {
			line += "  " + entry.detail
		}
		if entry.elapsed > 0 {
			line += "  " + formatSeconds(entry.elapsed)
		}
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n")
	m.activityViewport.SetContent(lipgloss.NewStyle().Width(max(m.activityViewport.Width, 1)).Render(content))
}

// seedTranscript pre-populates the viewport from persisted session turns.
// Roles are the normalized on-disk scheme ("user"/"assistant"); anything
// else (e.g. "tool") is dropped, matching brain.LoadHistory. Rendering goes
// through the same functions live events use so the two paths cannot drift.
func (m *conversationModel) seedTranscript(turns []brain.Turn) {
	for _, t := range turns {
		switch t.Role {
		case "user":
			m.rememberUser(t.Content, m.displaySpeakerLabel(t.Speaker))
			m.appendTranscript(renderSpeakerUserTurn(m.displaySpeakerLabel(t.Speaker), t.Content))
		case "assistant":
			item := agentItemFromStored(m.agentName, t.Content)
			if item.spoken != "" {
				m.rememberAssistant(item.spoken)
			}
			m.appendAgentTurn(item)
		}
	}
}

// rearm re-issues the bridge drain Cmd; it must follow every consumed
// busEventMsg or the model stops receiving bus events.
func (m conversationModel) rearm() tea.Cmd {
	if m.bridge == nil {
		return nil
	}
	return m.bridge.waitContext(m.deps.ctx)
}

// handleEvent maps one bus event onto viewport/status state. The mapping
// mirrors internal/ui's stdout renderer, minus the per-stage timing noise.
func (m *conversationModel) handleEvent(e events.Event) {
	// A final transcript means the brain now owns this turn: a text submit
	// past this point would kill a response in flight, so Enter waits.
	if _, ok := e.(events.UserInput); ok && m.turnState == turnVoiceListening {
		m.turnState = turnVoiceResponding
	}

	switch e := e.(type) {
	case events.RouteDecided:
		m.routeDecided(e)
	case events.PersonaSelection:
		m.applySelectionSnapshot(e)
	case events.AudioLevel:
		// Peak-hold + light smoothing so quiet frames don't flatten the meter.
		level := e.Level
		if level < 0 {
			level = 0
		}
		if level > 1 {
			level = 1
		}
		switch e.Source {
		case "output":
			if level > m.outputLevel {
				m.outputLevel = level
			} else {
				m.outputLevel = m.outputLevel*0.6 + level*0.4
			}
		default:
			if level > m.inputLevel {
				m.inputLevel = level
			} else {
				m.inputLevel = m.inputLevel*0.55 + level*0.45
			}
			// Meter only. Hearing you is Silero (STTPhase), not RMS — a fan
			// must not look like a turn that will never end.
		}

	case events.STTPhase:
		// Skip consecutive "listening" ticks from the no-speech restart loop.
		if e.Phase == "listening" && len(m.activity) > 0 {
			last := m.activity[len(m.activity)-1]
			if last.stage == "input" && last.detail == "listening" {
				m.setVoiceMode(anim.ModeListening)
				m.setStatus("Listening", false)
				break
			}
		}
		m.appendActivity("input", e.Phase, e.Elapsed)
		switch e.Phase {
		case "listening":
			m.setVoiceMode(anim.ModeListening)
			m.setStatus("Listening", false)
		case "hearing":
			m.setVoiceMode(anim.ModeHearing)
			m.setStatus("Hearing you", false)
		case "transcribing":
			m.setVoiceMode(anim.ModeTranscribing)
			m.setStatus("Transcribing", false)
		}

	case events.TranscriptPartial:
		if m.voiceMode != anim.ModeHearing && m.voiceMode != anim.ModeTranscribing {
			m.setVoiceMode(anim.ModeHearing)
		}
		m.setStatus(e.Text, false)

	case events.UserInput:
		m.appendActivity("input", "final", 0)
		// Typed submits already echoed the bubble; voice transcripts still need
		// one. Dedupe on exact text so a delayed cancel path cannot double-post.
		if m.pendingUserEcho != "" && m.pendingUserEcho == e.Text {
			m.pendingUserEcho = ""
			break
		}
		m.pendingUserEcho = ""
		m.activityFocused = false
		m.followChat = true
		label := e.Speaker
		if label == "" {
			label = m.currentLiveSpeakerLabel()
		}
		m.rememberUser(e.Text, m.displaySpeakerLabel(label))
		m.appendTranscript(renderSpeakerUserTurn(m.displaySpeakerLabel(label), e.Text))

	case events.ThinkingStarted:
		// Start a fresh streaming buffer; a prior turn's leftover (e.g. after an
		// interrupt that skipped ResponseReady) must not bleed into this reply.
		// Re-render so any stale live partial leaves the viewport immediately.
		m.liveThink = textclean.ThinkFilter{}
		if m.streamingAgent != "" || m.streamingThink != "" {
			m.streamingAgent = ""
			m.streamingThink = ""
			m.refreshContent()
			m.applyFollow()
		}
		m.appendActivity("model", "started", 0)
		m.setVoiceMode(anim.ModeThinking)
		m.setStatus(m.agentName+" thinking", false)

	case events.ResponseStreamingStarted:
		m.appendActivity("model", "first token", e.Elapsed)

	case events.ResponseSpeakableStarted:
		m.appendActivity("model", "first speakable", e.Elapsed)

	case events.ThinkingDelta:
		if e.Text == "" {
			break
		}
		m.appendStreamingThink(e.Text)

	case events.ResponseDelta:
		m.ingestSpeakableDelta(e.Text)

	case events.ThinkingComplete:
		m.appendActivity("model", "complete", e.Elapsed)

	case events.TokenUsage:
		m.appendActivity("model", fmt.Sprintf("prefill %d tok · gen %d tok", e.Prefill, e.Gen), 0)

	case events.SessionWarning:
		m.appendActivity("model", fmt.Sprintf("session prompt %d tok ≥ warn %d — consider clearing the conversation", e.PromptTokens, e.Threshold), 0)

	case events.SpeechSegmentReady:
		if e.Elapsed > 0 {
			m.appendActivity("voice", "first admit", e.Elapsed)
		} else {
			m.appendActivity("voice", "segment ready", 0)
		}

	case events.GeneratingVoice:
		m.appendActivity("voice", "synthesizing", 0)
		m.setVoiceMode(anim.ModeSynthesizing)
		// A degraded turn synthesizes its recovery line right after the Error
		// event; the error status outranks voice-progress chatter (F1.2).
		if !m.statusErr {
			m.setStatus("Synthesizing voice", false)
		}

	case events.VoiceGenerated:
		m.appendActivity("voice", "synth first frame", e.Elapsed)

	case events.SpeakingStarted:
		label := "playing"
		if e.AudibleSink == "none" {
			label = "synth"
		} else if e.Elapsed > 0 && e.AudibleSink == "stream_client" {
			label = "audible_tx"
		} else if e.Elapsed > 0 {
			label = "audible"
		}
		m.appendActivity("output", label, e.Elapsed)
		m.setVoiceMode(anim.ModeSpeaking)
		if !m.statusErr {
			m.setStatus("Speaking", false)
		}

	case events.SpeakingComplete:
		label := "complete"
		if e.AudibleSink == "none" {
			label = "synth drain (no sink)"
		} else if e.AudibleSink == "stream_client" {
			label = "audible_tx done"
		}
		m.appendActivity("output", label, e.Elapsed)
		// A degraded turn speaks its recovery line after the Error event; the
		// error status must survive that playback finishing. Still leave
		// ModeSpeaking so the EQ/label does not stay "Speaking" forever.
		if m.statusErr {
			m.setVoiceMode(anim.ModeError)
		} else {
			m.setVoiceMode(anim.ModeIdle)
			m.setStatus("", false)
		}

	case events.SpeakingInterrupted:
		m.appendActivity("output", "interrupted: "+e.Reason, 0)
		m.setVoiceMode(anim.ModeIdle)
		m.setStatus("speech interrupted ("+e.Reason+")", false)

	case events.TurnInterrupted:
		m.appendActivity("turn", "interrupted: "+e.Reason, 0)
		m.setVoiceMode(anim.ModeIdle)
		m.setStatus("turn interrupted ("+e.Reason+")", false)

	case events.ResponseReady:
		m.appendActivity("turn", "response ready", 0)
		// Text-only / no-TTS turns never emit SpeakingComplete; clear the
		// thinking status here so it does not stick after a successful reply.
		// An error status is exempt: recoverTurn emits Error then ResponseReady
		// back-to-back, and clearing here erased the error one frame after it
		// rendered. Errors persist until the next turn's activity writes over
		// them.
		if m.voiceMode != anim.ModeSpeaking && m.voiceMode != anim.ModeSynthesizing && !m.statusErr {
			m.setVoiceMode(anim.ModeIdle)
			m.setStatus("", false)
		}
		// Finalize the streamed turn: clear the live buffers first so they are
		// not rendered twice. Thinking is stored on the agent item but hidden
		// until showThink; the spoken bubble comes from e.Response when present.
		thought, liveSpoken := m.takeStreamingTurn()
		spoken := strings.TrimSpace(e.Response)
		if spoken == "" {
			spoken = liveSpoken
		} else if t, b := textclean.SplitThink(spoken); t != "" {
			if thought == "" {
				thought = t
			}
			spoken = b
		}
		name := m.agentName
		if e.Envelope != nil && e.Envelope.DisplayName != "" {
			name = e.Envelope.DisplayName
		}
		if spoken != "" {
			m.rememberAssistant(spoken)
			m.appendAgentTurn(chatItem{name: name, think: thought, spoken: spoken})
		} else if e.Interrupted {
			m.refreshContent()
			m.applyFollow()
		} else if thought != "" {
			// Thinking-only empty: keep the thought for Ctrl+K; stay off the
			// thread while showThink is false so we do not paint a blank bubble.
			m.appendActivity("model", fmt.Sprintf("thought %d chars", len(thought)), 0)
			m.appendAgentTurn(chatItem{name: name, think: thought})
		} else {
			// Tool-only or empty model finishes must still leave a visible
			// trail so "looking into it" never ends in total silence.
			m.appendTranscript(dimStyle.Render("  (no reply — model finished without text)"))
		}

	case events.ConversationCleared:
		m.workActivity = false
		m.workFilter = ""
		m.clearTranscript()
		m.followChat = true
		m.appendTranscript(dimStyle.Render("  Conversation cleared."))

	case events.WorkStateChanged:
		m.workStateChanged(e)

	case events.ConversationCompacted:
		// Annotate, don't clear: the scrollback stays for the user, but the
		// model now continues from the summary instead of the full history.
		m.appendActivity("model", fmt.Sprintf("compacted %d turns into a summary", e.TurnsBefore), 0)
		m.appendTranscript(dimStyle.Render(fmt.Sprintf("  Conversation compacted (%d turns summarized — the model continues from the summary).", e.TurnsBefore)))

	case events.TurnMetrics:
		m.lastMetrics = e
		// Terminal event with the live stream buffer still set means the turn
		// ended on a path that skipped ResponseReady (cancel/failure). Fold the
		// partial into the transcript instead of leaving it pinned beneath it,
		// where it reads as a duplicated fragment of the reply (WI-dc9e33 B1).
		// Clear the live buffer *before* appendTranscript: that helper refreshes
		// the viewport immediately, and leaving streamingAgent set would render
		// the same text twice (transcript bubble + live buffer) with no later
		// refresh on a bare interrupted metrics event.
		thought, partial := m.takeStreamingTurn()
		if partial != "" {
			m.rememberAssistant(partial)
			m.appendAgentTurn(chatItem{name: m.agentName, think: thought, spoken: partial})
		} else if thought != "" {
			m.appendAgentTurn(chatItem{name: m.agentName, think: thought})
		}
		// Idle no-speech timeouts restart listening every ~listen_timeout
		// seconds. Logging each one floods Activity without helping the user.
		if e.Outcome == "timed_out" {
			break
		}
		outcome := e.Outcome
		if e.Degraded {
			outcome += " (degraded)"
		}
		m.appendActivity("turn", outcome, e.PlaybackCompleteElapsed)
		if line := formatTurnMetrics(e); line != "" {
			m.appendTranscript(dimStyle.Render("    " + line))
		}

	case events.Error:
		// The Activity pane is the durable surface — it must carry the real
		// message, not just the stage name. The status line clears on the next
		// event; an opaque "error brain" entry is how degraded qwen turns went
		// undiagnosable for weeks (WI-dc9e33 B1).
		detail := strings.TrimSpace(strings.TrimPrefix(e.Stage+": "+e.Message, ": "))
		m.appendActivity("error", detail, 0)
		msg := e.Message
		if e.Stage != "" {
			msg = "[" + e.Stage + "] " + e.Message
		}
		m.setVoiceMode(anim.ModeError)
		m.setStatus("Error: "+msg, true)

	case events.Info:
		m.appendActivity("info", e.Message, 0)
		m.appendTranscript(dimStyle.Render("  " + e.Message))

	case events.SpeechDiagnostic:
		// Diagnostics are routine Activity records. Only an applicable action
		// is rendered into the transcript, and it is emitted once per actual
		// provider configuration by the pipeline's bounded advice window.
		m.appendActivity("voice", e.Message, 0)
		if e.ActionCode != "" && e.Message != "" {
			m.appendTranscript(dimStyle.Render("  " + e.Message))
		}

	case events.VoiceGateStripped:
		// Voice-only filter: the raw text is already in the transcript above;
		// this entry records that it was not spoken.
		m.appendActivity("voice", fmt.Sprintf("gate stripped %d tool-syntax line(s) from speech", e.Lines), 0)

	case events.ToolCallStarted:
		if e.JobID != "" {
			m.jobToolStarted(e)
			break
		}
		msg := "tool " + e.Name
		if e.Summary != "" {
			msg += " (" + e.Summary + ")"
		}
		m.appendActivity("tool", msg, 0)
		m.setStatus("🔧 "+msg+"...", false)
		m.appendTranscript(dimStyle.Render("  🔧 " + msg))

	case events.ToolCallFinished:
		if e.JobID != "" {
			m.jobToolFinished(e)
			break
		}
		msg := "tool " + toolOutcomeLabel(e.OutcomeStatus())
		if e.Name != "" {
			msg = "tool " + e.Name + " " + toolOutcomeLabel(e.OutcomeStatus())
		}
		if preview := e.ResultPreview(); preview != "" {
			msg += " → " + preview
		}
		if reason := e.ResultReason(); reason != "" {
			msg += ": " + reason
		}
		if e.Outcome != nil {
			if e.Outcome.ExitCode != nil {
				msg += fmt.Sprintf(" (exit code %d)", *e.Outcome.ExitCode)
			}
			if e.Outcome.Signal != "" {
				msg += " (signal " + e.Outcome.Signal + ")"
			}
		}
		if elapsed := e.ResultElapsed(); elapsed > 0 {
			msg += " in " + elapsed.String()
		}
		if status := e.OutcomeStatus(); status == events.ToolOutcomeFailed || status == events.ToolOutcomeTimedOut || status == events.ToolOutcomeCanceled {
			m.setVoiceMode(anim.ModeError)
			m.setStatus("✗ "+msg, true)
		} else {
			m.setVoiceMode(anim.ModeThinking)
			m.setStatus(m.agentName+" thinking", false)
		}
		prefix := "✓ "
		if e.OutcomeStatus() == events.ToolOutcomeFailed || e.OutcomeStatus() == events.ToolOutcomeTimedOut || e.OutcomeStatus() == events.ToolOutcomeCanceled {
			prefix = "✗ "
		} else if e.OutcomeStatus() == events.ToolOutcomeUnknown {
			prefix = "? "
		}
		m.appendTranscript(dimStyle.Render("  " + prefix + msg))
		m.appendActivity("tool", msg, 0)

	case events.JobQueued:
		m.jobQueued(e)

	case events.JobStarted:
		m.jobStarted(e)

	case events.JobFinished:
		m.jobFinished(e)

	case events.JobFailed:
		m.jobFailed(e)

	case events.JobAborted:
		m.jobAborted(e)
	}
}

// formatTurnMetrics compacts a turn's latency milestones into one dim
// trailing line under the turn (the status bar is overwritten by the next
// listening state almost immediately, so timings live in the transcript).
func formatTurnMetrics(e events.TurnMetrics) string {
	var parts []string
	origin := "since turn start"
	model, ready, complete := e.ModelCompleteElapsed, e.FirstAudioReadyElapsed, e.PlaybackCompleteElapsed
	if e.STTFinalElapsed > 0 {
		origin = "since input"
		model = max(0, model-e.STTFinalElapsed)
		ready = max(0, ready-e.STTFinalElapsed)
	}
	if e.Timing != nil {
		origin = "since turn start"
		if e.Timing.Origin == "finalized_input" {
			origin = "since input"
		}
		duration := func(ms *float64) time.Duration {
			if ms == nil {
				return 0
			}
			return time.Duration(*ms * float64(time.Millisecond))
		}
		model = duration(e.Timing.Normalized.ModelCompleteMS)
		ready = duration(e.Timing.Normalized.FirstAudioReadyMS)
		complete = duration(e.Timing.Normalized.PlaybackCompleteMS)
	}
	if model > 0 {
		parts = append(parts, "model "+formatSeconds(model))
	}
	if ready > 0 {
		parts = append(parts, "first audio "+formatSeconds(ready))
	}
	if complete > 0 && e.AudibleSink != "none" && e.AudibleSink != "stream_client" {
		parts = append(parts, "playback done "+formatSeconds(complete))
	}
	if len(parts) > 0 {
		parts = append([]string{origin}, parts...)
	}
	if e.Degraded {
		parts = append(parts, "degraded")
	}
	return strings.Join(parts, " · ")
}

func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
}

// formatActivityAt renders a session-relative timestamp for the Activity feed
// (ctrl+t). Prefer m:ss.s / h:mm:ss.s over raw "1250.5s" so long sessions stay
// scannable. Fixed-ish width keeps the stage column aligned.
func formatActivityAt(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	// Round to 0.1s so float formatting does not jitter at .999 boundaries.
	totalMs := d.Round(100 * time.Millisecond).Milliseconds()
	if totalMs < 0 {
		totalMs = 0
	}
	tenths := (totalMs / 100) % 10
	totalSec := totalMs / 1000
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	s := totalSec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%d", h, m, s, tenths)
	}
	return fmt.Sprintf("%2d:%02d.%d", m, s, tenths)
}

// renderUserTurn and renderAgentTurn paint finalized turns (ResponseReady and
// session replay). Live streaming uses renderAgentBubble so think and speakable
// stay on separate accumulators.
func renderUserTurn(text string) string {
	body := userStyle.Render("› You") + "\n" + normalStyle.Render(text)
	return userBubbleStyle.Render(body)
}

func renderAgentTurn(name, text string) string {
	think, spoken := textclean.SplitThink(text)
	showThink := think != ""
	// Tag-only leftover on ResponseReady / session replay: both halves empty
	// means the stored text is think markup with no inner body. Paint the dim
	// state rather than the raw tags. Live frames no longer call this function.
	if !showThink && spoken == "" && strings.TrimSpace(text) != "" {
		showThink = true
	}
	return renderAgentBubble(name, think, spoken, showThink)
}

func renderAgentBubble(name, think, spoken string, showThink bool) string {
	var b strings.Builder
	b.WriteString(agentStyle.Render("● " + name))
	if showThink {
		b.WriteByte('\n')
		b.WriteString(thinkLabelStyle.Render("thinking"))
		if think != "" {
			b.WriteByte('\n')
			b.WriteString(thinkBodyStyle.Render(think))
		}
	}
	if spoken != "" {
		b.WriteByte('\n')
		b.WriteString(normalStyle.Render(spoken))
	}
	return agentBubbleStyle.Render(b.String())
}

func (m conversationModel) View() string {
	if m.startup.active {
		return m.startupView()
	}
	if !m.ready {
		return "\n  " + headerStyle.Render("Preparing conversation…") + "\n"
	}

	if m.width < 20 || m.height < 6 {
		return "Resize to 20×6 or larger"
	}
	now := m.workStripRenderAt
	if now.IsZero() {
		now = time.Now()
	}
	styles := m.animStyles()
	w := max(m.width, 1)

	// Clean header: name, tabs, compact EQ chip.
	left := headerStyle.Render(m.agentName)
	if badge := ttsBadgeLabel(m.cfg); badge != "" {
		left += "  " + chipMutedStyle.Render(badge)
	}
	left += "  " + m.renderTabs()
	if m.sessionID != "" {
		left += "  " + dimStyle.Render(shortSessionID(m.sessionID))
	}
	right := ""
	if m.voiceMode != anim.ModeIdle {
		right = anim.CompactMeter(m.voiceMode, m.voiceFrame, m.voiceLevel(), m.status, styles, m.reducedMotion)
	} else if m.status != "" {
		style := statusStyle
		if m.statusErr {
			style = errorStyle
		}
		right = style.Render(m.status)
	}
	headerInner := left
	if right != "" {
		pad := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
		if pad < 1 {
			pad = 1
		}
		headerInner = left + strings.Repeat(" ", pad) + right
	}
	header := ansi.Truncate(headerInner, w, "…")

	rule := lipgloss.NewStyle().Foreground(m.inputBorderColor()).Render(strings.Repeat("─", w))

	voiceStrip := ""
	if m.voiceMode != anim.ModeIdle && m.height >= 14 {
		voiceStrip = anim.Stage(m.voiceMode, m.voiceFrame, m.voiceLevel(), w, m.status, styles, m.reducedMotion)
		if voiceStrip != "" {
			voiceStrip += "\n"
		}
	}

	// Capture stays armed while voice input is paused; wording must not claim
	// the OS microphone was released.
	micChip := chipMutedStyle.Render("mic off")
	if m.voiceOn() {
		micChip = chipStyle.Render("mic on")
		if m.voiceMode == anim.ModeHearing {
			micChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorHearing).Bold(true).Padding(0, 1).Render("recording")
		}
	}
	outChip := chipMutedStyle.Render("audio n/a")
	if m.outputAvailable {
		if m.outputMuted {
			outChip = chipMutedStyle.Render("audio off")
		} else if m.voiceMode == anim.ModeSpeaking {
			outChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorSpeak).Bold(true).Padding(0, 1).Render("speaking")
		} else {
			outChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorAgent).Bold(true).Padding(0, 1).Render("audio on")
		}
	}
	footerLeft := "  " + micChip + " " + outChip
	if m.journalIncomplete {
		footerLeft += " " + chipMutedStyle.Render("Diagnostic history incomplete")
	}
	if hint := m.talkHint(); hint != "" && !m.vim.enabled {
		footerLeft += " " + dimStyle.Render(hint)

	}
	workers := m.jobStatus
	if m.deps.workersSummary != nil {
		workers = m.deps.workersSummary()
	}
	if workers == "" {
		workers = workersCountLabel(0, 0, m.cfg == nil || !m.cfg.VoiceWorkersDisabled)
	}
	footerLeft += " " + dimStyle.Render(workerFooterLabel(workers, max(0, w-lipgloss.Width(footerLeft)-1)))
	if m.showThink {
		footerLeft += " " + chipStyle.Render("think")
	}
	if m.liveSpeakerStatsKnown {
		footerLeft += " " + renderLiveSpeakerFooterNamed(m.liveSpeakerStats, m.displaySpeakerLabel(m.liveSpeakerStats.LastLabel))
	}
	activeViewport := m.activeViewport()
	if activeViewport.TotalLineCount() > activeViewport.VisibleLineCount() {
		footerLeft += " " + chipMutedStyle.Render(fmt.Sprintf("%d%%", int(activeViewport.ScrollPercent()*100)))
	}
	footerHelp := m.vimFooterHelp()
	if !m.vim.enabled {
		footerHelp = strings.ReplaceAll(footerHelp, "/menu back  ", "")
	}
	footerText := footerLeft
	switch {
	case m.vim.enabled:
		// Modal controls are the primary interaction contract. Keep them visible
		// at medium widths, then include device state when the terminal has room.
		compactHelp := "  " + m.vimCompactFooterHelp()
		footerText = compactHelp
		if m.width >= lipgloss.Width(compactHelp)+lipgloss.Width(footerLeft)+4 {
			footerText += strings.Repeat(" ", m.width-lipgloss.Width(compactHelp)-lipgloss.Width(footerLeft)) + footerLeft
		}
	case m.width >= lipgloss.Width(footerLeft)+lipgloss.Width(footerHelp)+4:
		footerText += strings.Repeat(" ", m.width-lipgloss.Width(footerLeft)-lipgloss.Width(footerHelp)) + dimStyle.Render(footerHelp)
	case m.width >= 60:
		compact := "  ·  ^G mic  ^O audio  ^T switch  ^K think"
		footerText += dimStyle.Render(compact)
	default:
		footerText += dimStyle.Render("  ^G  ^O  ^T  ^K")
	}
	footer := ansi.Truncate(footerText, w, "…")

	content := m.viewport.View()
	if m.activityFocused {
		content = m.activityViewport.View()
	}

	inputLabel := "Your message"
	switch {
	case m.voiceMode == anim.ModeHearing:
		inputLabel = hearingStyle.Render("● Hearing you — type to interrupt")
	case m.voiceMode == anim.ModeListening:
		inputLabel = headerStyle.Render("◎ Listening — type anytime")
	case m.voiceMode == anim.ModeSpeaking || m.voiceMode == anim.ModeSynthesizing:
		inputLabel = speakStyle.Render("◉ Speaking — type to barge in")
	case m.turnState == turnVoiceListening:
		inputLabel = headerStyle.Render("🎙 Listening — type to interrupt")
	case m.turnState == turnVoiceResponding || m.turnState == turnTextRunning:
		inputLabel = thinkStyle.Render("✦ Responding — type to barge in")
	case m.turnState == turnVoiceCanceling:
		inputLabel = thinkStyle.Render("✦ Interrupting…")
	default:
		inputLabel = dimStyle.Render(inputLabel)
	}
	inputLabel = m.vimInputLabel(inputLabel)
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.inputBorderColor()).
		Padding(0, 1).
		Render(m.input.View())

	palette := m.renderCommandPalette()
	if palette != "" {
		content += "\n" + palette
	}

	return header + "\n" + rule + "\n" +
		voiceStrip +
		content + "\n" +
		m.renderWorkStrip(now) +
		ansi.Truncate(inputLabel, w, "…") + "\n" +
		inputBox + "\n" +
		footer
}

func (m conversationModel) renderTabs() string {
	inactive := lipgloss.NewStyle().Foreground(colorDim).Padding(0, 1)
	active := lipgloss.NewStyle().Foreground(colorBg).Background(colorSelect).Bold(true).Padding(0, 1)
	chat := inactive.Render("Chat")
	activity := inactive.Render("Activity")
	if m.activityFocused {
		activity = active.Render("Activity")
	} else {
		chat = active.Render("Chat")
	}
	return chat + " " + activity
}

func shortSessionID(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}

func truncateJobLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 48 {
		return s
	}
	return s[:45] + "..."
}
