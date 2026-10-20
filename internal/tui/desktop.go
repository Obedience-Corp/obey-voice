package tui

// This adapter runs the existing conversation state machine without terminal
// input or rendering. Native clients receive plain state and submit actions;
// voice admission, cancellation, hotkeys and background delivery stay here.
import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type DesktopAction struct {
	Kind, Text string
	Enabled    bool
}
type DesktopTurn struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	Name     string `json:"name"`
	Thinking string `json:"thinking,omitempty"`
}
type DesktopActivity struct {
	Stage        string `json:"stage"`
	Detail       string `json:"detail"`
	Milliseconds int64  `json:"milliseconds"`
}
type DesktopEnrollment struct {
	Active     bool    `json:"active"`
	Name       string  `json:"name"`
	Clip       int     `json:"clip"`
	Seconds    float64 `json:"seconds"`
	Processing bool    `json:"processing"`
}
type DesktopState struct {
	Notices         []string              `json:"notices"`
	WorkResults     []string              `json:"work_results"`
	SettingsMessage string                `json:"settings_message"`
	Enrollment      DesktopEnrollment     `json:"enrollment"`
	SpeakerNames    []string              `json:"speaker_names"`
	SessionID       string                `json:"session_id"`
	Name            string                `json:"name"`
	Status          string                `json:"status"`
	Error           bool                  `json:"error"`
	Mic             bool                  `json:"mic"`
	VoiceAvailable  bool                  `json:"voice_available"`
	Muted           bool                  `json:"muted"`
	Busy            bool                  `json:"busy"`
	Recording       string                `json:"recording"`
	Partial         string                `json:"partial"`
	Thinking        string                `json:"thinking"`
	Response        string                `json:"response"`
	InputLevel      float64               `json:"input_level"`
	OutputLevel     float64               `json:"output_level"`
	Turns           []DesktopTurn         `json:"turns"`
	Activity        []DesktopActivity     `json:"activity"`
	Workers         []pipeline.WorkerInfo `json:"workers"`
	WorkersEnabled  bool                  `json:"workers_enabled"`
}

type DesktopController struct {
	program *tea.Program
	done    chan error
}

func (d *DesktopController) Send(action DesktopAction) { d.program.Send(action) }
func (d *DesktopController) Wait() error               { return <-d.done }

type desktopTick struct{}
type desktopModel struct {
	app     App
	rt      *ConversationRuntime
	initial tea.Cmd
	publish func(DesktopState)
	last    time.Time
}

func (m desktopModel) Init() tea.Cmd { return tea.Batch(m.initial, desktopTimer()) }
func (m desktopModel) View() string  { return "" }
func desktopTimer() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return desktopTick{} })
}

// StartDesktopController takes ownership of rt. Wait returns only after turns
// have received a bounded drain and Cleanup has run. publish must return
// promptly and never call Send synchronously. No terminal escape sequences or terminal reads are used.
func StartDesktopController(ctx context.Context, rt *ConversationRuntime, publish func(DesktopState)) *DesktopController {
	ctx, cancel := context.WithCancel(ctx)
	cfg := *rt.SessionCfg
	a := newApp(&cfg, nil, nil)
	a.screen = screenConversation
	a.runCtx, a.runCancel, a.wg = ctx, cancel, &sync.WaitGroup{}
	a.width, a.height = 100, 40
	a = a.onWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	ready, initial := a.onRuntimeReady(runtimeReadyMsg{rt: rt})
	model := desktopModel{app: ready.(App), rt: rt, initial: initial, publish: publish}
	p := tea.NewProgram(model, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	d := &DesktopController{program: p, done: make(chan error, 1)}
	go func() {
		final, err := p.Run()
		m, ok := final.(desktopModel)
		if !ok {
			m = model
		}
		_, _ = m.app.onQuit(quitMsg{})
		cancel()
		// Save before waiting on hardware or helper teardown, as the TUI does.
		rt.Persist()
		waitQuitHelpers(m.app, model.app, cancel)
		rt.Cleanup()
		d.done <- err
		close(d.done)
	}()
	go func() { <-ctx.Done(); p.Send(quitMsg{}) }()
	return d
}

func (m desktopModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if action, ok := msg.(DesktopAction); ok {
		c := &m.app.conversation
		switch action.Kind {
		case "text":
			c.input.SetValue(action.Text)
			cmd = c.handleSubmit()
		case "mic":
			cmd = c.setInputMuted(!action.Enabled)
		case "audio":
			if c.outputMuted == action.Enabled {
				c.toggleOutputMuted()
			}
		case "talk_down":
			if m.app.screen == screenConversation {
				cmd = c.beginTalkTurn()
			}
		case "talk_up":
			cmd = c.endTalkTurn()
		case "interrupt":
			if c.turnCancel != nil {
				c.turnCancel()
			}
			m.rt.Pipeline.StopPlayback()
		case "persona":
			if m.rt.SelectPersona == nil {
				c.setStatus("This provider does not support live persona selection", true)
			} else {
				cmd = c.enqueuePersonaSelection(action.Text)
			}
		case "manager":
			target := screenPersonas
			if action.Text == "settings" {
				// The native settings form edits app defaults (config get/set),
				// while the conversation retains its session binding. Capture
				// the settings baseline from that same editing scope.
				cfg, err := config.LoadRaw()
				if err != nil {
					c.setStatus(err.Error(), true)
					break
				}
				m.app.cfg = cfg
				target = screenSettings
			}
			if action.Text == "workers" {
				target = screenWorkers
			}
			next, nextCmd := m.app.onSwitchScreen(switchScreenMsg(target))
			m.app, cmd = next.(App), nextCmd
		case "return":
			if m.app.screen == screenSettings {
				if cfg, err := config.LoadRaw(); err == nil {
					m.app.cfg = cfg
				} else {
					c.setStatus(err.Error(), true)
				}
				next, nextCmd := m.app.onSettingsDone(settingsDoneMsg{})
				m.app, cmd = next.(App), nextCmd
			} else {
				next, nextCmd := m.app.onSwitchScreen(switchScreenMsg(screenConversation))
				m.app, cmd = next.(App), nextCmd
			}
		case "settings_refresh":
			if m.app.screen == screenSettings {
				cfg, err := config.LoadRaw()
				if err != nil {
					m.app.settings.message = err.Error()
				} else {
					// A successful native config save updates the editing
					// snapshot without replacing the entry fingerprint.
					m.app.settings.closePreview()
					// The default player factory closes over the prior settings
					// model; recreate it for changed device/volume settings.
					m.app.settings.preview.newPreviewPlayer = nil
					m.app.cfg, m.app.settings.cfg = cfg, cfg
				}
			}
		case "enroll", "enroll_cancel", "enroll_remove", "preview", "preview_stop":
			if m.app.screen != screenSettings {
				c.setStatus("Open Settings first", true)
				break
			}
			s := &m.app.settings
			switch action.Kind {
			case "enroll":
				if strings.TrimSpace(action.Text) == "" {
					s.message = "Enter a name before recording"
					break
				}
				if !s.enroll.active {
					cmd = s.startEnroll(strings.TrimSpace(action.Text), action.Enabled)
				}
			case "enroll_cancel":
				s.cancelEnroll()
			case "enroll_remove":
				s.removeVoice(action.Text)
			case "preview":
				cmd = s.startSettingsPreview(tts.Voice{Name: action.Text})
			case "preview_stop":
				s.cancelPreview()
			}
		case "worker_reload":
			cfg, err := config.LoadRaw()
			if err != nil {
				c.setStatus(err.Error(), true)
				break
			}
			next, nextCmd := m.app.Update(workerSettingsSavedMsg{workers: cfg.VoiceWorkers})
			m.app, cmd = next.(App), nextCmd
		case "workers_enabled":
			if err := saveWorkersEnabled(m.app.cfg, action.Enabled, nil, m.rt.Pipeline.SetWorkersEnabled); err != nil {
				c.setStatus(err.Error(), true)
			}
		case "worker_stop":
			if err := m.rt.Pipeline.CancelWork(action.Text); err != nil {
				c.setStatus(err.Error(), true)
			}
		case "workers_stop":
			if err := m.rt.Pipeline.StopWorkers(); err != nil {
				c.setStatus(err.Error(), true)
			}
		case "quit":
			next, nextCmd := m.app.onQuit(quitMsg{})
			m.app, cmd = next.(App), nextCmd
		default:
			c.setStatus(fmt.Sprintf("Unknown desktop action: %s", action.Kind), true)
		}
	} else if _, ok := msg.(desktopTick); ok {
		cmd = desktopTimer()
	} else {
		next, nextCmd := m.app.Update(msg)
		m.app, cmd = next.(App), nextCmd
	}
	if time.Since(m.last) >= 50*time.Millisecond {
		m.last = time.Now()
		if m.publish != nil {
			m.publish(m.state())
		}
	}
	return m, cmd
}

func (m desktopModel) state() DesktopState {
	c := m.app.conversation
	state := DesktopState{SessionID: m.rt.SessionID, Name: c.agentName, Status: ansi.Strip(c.status), Error: c.statusErr, Mic: c.voiceEnabled,
		VoiceAvailable: m.rt.Voice, Muted: c.outputMuted, Busy: c.turnState != turnIdle, Recording: c.recordMode(), Thinking: c.streamingThink,
		Response: c.streamingAgent, InputLevel: c.inputLevel, OutputLevel: c.outputLevel, Workers: m.rt.Pipeline.Workers(), WorkersEnabled: m.rt.Pipeline.WorkersEnabled()}
	agentIndex := 0
	for _, turn := range c.plainTurns {
		out := DesktopTurn{Role: turn.role, Content: turn.text, Name: turn.speaker}
		if turn.role == "assistant" {
			for agentIndex < len(c.items) {
				item := c.items[agentIndex]
				agentIndex++
				if item.kind == chatItemAgent && item.spoken == turn.text {
					out.Name = item.name
					out.Thinking = item.think
					break
				}
			}
		}
		state.Turns = append(state.Turns, out)
	}
	for _, item := range c.items {
		if item.notice != "" {
			state.Notices = append(state.Notices, item.notice)
		}
	}
	if len(state.Notices) > 20 {
		state.Notices = state.Notices[len(state.Notices)-20:]
	}
	state.WorkResults = c.readableWorkResults()
	s := m.app.settings
	state.SettingsMessage = ansi.Strip(s.message)
	state.Enrollment = DesktopEnrollment{Active: s.enroll.active, Name: s.enroll.name, Clip: s.enroll.clip, Seconds: s.enroll.elapsed.Seconds(), Processing: s.enroll.enrolling}
	for _, p := range s.enrolledProfiles {
		state.SpeakerNames = append(state.SpeakerNames, p.Name)
	}
	start := max(0, len(c.activity)-200)
	for _, item := range c.activity[start:] {
		state.Activity = append(state.Activity, DesktopActivity{Stage: item.stage, Detail: ansi.Strip(item.detail), Milliseconds: item.elapsed.Milliseconds()})
	}
	return state
}
