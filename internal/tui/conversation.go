package tui

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/capture"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/workfold"
)

const conversationInputHeight = 3

// voiceTickInterval drives the conversation meter animation (~10 fps).
const voiceTickInterval = 100 * time.Millisecond

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

	bridge        *eventBridge
	lastMetrics   events.TurnMetrics
	contextBudget events.ContinuityBudget
	contextKnown  bool
	contextFill   float64
	compactStage  string

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
	followChat           bool
	followActivity       bool
	startedAt            time.Time
	sessionID            string
	workerRepliesSeen    map[string]bool
	capture              *capture.Session
	capturePending       captureSeal
	captureNextMode      string
	captureSealStarted   time.Time
	captureSealWarned    bool
	capturePollScheduled bool
	captureHealthLast    time.Time
	lastRecordingDir     string
	recordingTargets     map[string]string
	openRecording        func(context.Context, string) error
	inputDevice          string
	outputDevice         string
	voiceFailures        int
	// noticeEpoch retires admission manual-retry notices across clear/new-session/
	// shutdown boundaries. Late turn callbacks carrying older epochs cannot
	// re-emit retired guidance.
	noticeEpoch           uint64
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
	mentions         personaMentions
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
