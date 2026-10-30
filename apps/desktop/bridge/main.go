package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	engine "github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

type request struct {
	Paths          []string                     `json:"paths"`
	Image          []byte                       `json:"image"`
	VoiceSelection engine.DesktopVoiceSelection `json:"voice_selection"`
	Config         json.RawMessage              `json:"config"`
	Revision       int64                        `json:"revision"`
	ID             uint64                       `json:"id"`
	Method         string                       `json:"method"`
	Text           string                       `json:"text"`
	Enabled        bool                         `json:"enabled"`
	SessionID      string                       `json:"session_id"`
	PersonaID      string                       `json:"persona_id"`
	TextOnly       bool                         `json:"text_only"`
	Silent         bool                         `json:"silent"`
	Resume         string                       `json:"resume"`
}
type output struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (o *output) send(v any)                  { o.mu.Lock(); defer o.mu.Unlock(); _ = o.enc.Encode(v) }
func (o *output) event(kind string, data any) { o.send(map[string]any{"event": kind, "data": data}) }
func (o *output) reply(r request, value any, err error) {
	msg := map[string]any{"id": r.ID, "result": value}
	if err != nil {
		msg["error"] = err.Error()
	}
	o.send(msg)
}
func main() {
	if len(os.Args) < 2 || os.Args[1] != "--bridge" {
		if err := engine.Execute(); err != nil {
			os.Exit(engine.ExitCode(err))
		}
		return
	}
	out, err := protocolOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Stdin, out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type host struct {
	preview      *voicePreview
	previewVoice func(context.Context, engine.DesktopVoiceSelection) error
	controller   *engine.DesktopController
	stateCancel  context.CancelFunc
	stateDone    chan struct{}
	meeting      *meetingHost
}

func run(parent context.Context, input io.Reader, writer io.Writer) error {
	return runWithCatalog(parent, input, writer, obeyCatalog)
}

func runWithCatalog(parent context.Context, input io.Reader, writer io.Writer, catalog func(context.Context, json.RawMessage) (any, error)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	o := &output{enc: json.NewEncoder(writer)}
	incoming := make(chan request, 32)
	go func() {
		defer close(incoming)
		scan := bufio.NewScanner(input)
		scan.Buffer(make([]byte, 4096), 12<<20)
		for scan.Scan() {
			var r request
			if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
				o.event("error", "Invalid request: "+err.Error())
				continue
			}
			if r.Method == "shutdown" {
				cancel()
				return
			}
			select {
			case incoming <- r:
			case <-ctx.Done():
				return
			}
		}
		if err := scan.Err(); err != nil {
			o.event("error", err.Error())
		}
		cancel()
	}()
	h := &host{}
	defer h.close(o)
	var catalogs sync.WaitGroup
	defer func() {
		cancel()
		catalogs.Wait()
	}()
	o.event("ready", map[string]any{"protocol": 1})
	for {
		select {
		case <-ctx.Done():
			return nil
		case r, ok := <-incoming:
			if !ok {
				return nil
			}
			if r.Method == "obey_catalog" {
				// Discovery is read-only and may wait on an unavailable daemon.
				// Keep live controls on the request loop and join canceled lookups
				// before closing the bridge output.
				catalogs.Go(func() {
					result, err := catalog(ctx, r.Config)
					o.reply(r, result, err)
				})
				continue
			}
			result, err := h.handle(ctx, r, o)
			o.reply(r, result, err)
		}
	}
}
func (h *host) close(o *output) {
	h.stopPreview()
	if h.preview != nil {
		<-h.preview.done
		h.preview = nil
	}
	if h.controller != nil {
		h.controller.Send(engine.DesktopAction{Kind: "quit"})
		if err := h.controller.Wait(); err != nil {
			o.event("error", err.Error())
		}
		h.controller = nil
		h.stateCancel()
		<-h.stateDone
		h.stateCancel = nil
		h.stateDone = nil
	}
	if h.meeting != nil {
		if _, err := h.meeting.stop(); err != nil {
			o.event("error", err.Error())
		}
		h.meeting = nil
	}
}
func (h *host) handle(ctx context.Context, r request, o *output) (any, error) {
	switch r.Method {
	case "persona_preview":
		h.startPreview(ctx, r, o)
		return nil, nil
	case "persona_preview_stop":
		h.stopPreview()
		return nil, nil
	case "setup_status":
		return setupStatus()
	case "setup_save":
		return saveSetup(setupPath(), r.Config)
	case "setup_provider":
		return setupProvider(ctx)
	case "worker_config_get":
		cfg, err := config.LoadRaw()
		if err != nil {
			return nil, err
		}
		if cfg.VoiceWorkers != nil {
			return cfg.VoiceWorkers, nil
		}
		return config.PreviewLegacyWorkerMigration(cfg)
	case "worker_config_save":
		var draft config.VoiceWorkers
		dec := json.NewDecoder(bytes.NewReader(r.Config))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&draft); err != nil {
			return nil, err
		}
		saved, err := config.CommitVoiceWorkersCAS(&draft, r.Revision)
		if err == nil && h.controller != nil {
			h.controller.Send(engine.DesktopAction{Kind: "worker_reload"})
		}
		return saved, err
	case "ping":
		return "pong", nil
	case "sessions":
		return session.DefaultStore().List(), nil
	case "session_delete":
		if h.controller != nil {
			return nil, errors.New("End the conversation before deleting a saved session")
		}
		return nil, session.DefaultStore().Delete(r.SessionID)
	case "start":
		h.stopPreview()
		if h.controller != nil || h.meeting != nil {
			return nil, errors.New("End the current conversation or meeting first")
		}
		rt, err := engine.BuildDesktopConversation(ctx, engine.DesktopOptions{SessionID: r.SessionID, PersonaID: r.PersonaID, TextOnly: r.TextOnly, Silent: r.Silent}, func(s string) { o.event("progress", s) })
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			rt.Cleanup()
			return nil, ctx.Err()
		}
		// The consumer owns the latest full snapshot; slow UI output cannot stall
		// the TUI state machine, microphone, or model event bus.
		states := make(chan engine.DesktopState, 1)
		stateCtx, stateCancel := context.WithCancel(ctx)
		h.stateCancel = stateCancel
		stateDone := make(chan struct{})
		h.stateDone = stateDone
		go func() {
			defer close(stateDone)
			for {
				select {
				case s := <-states:
					o.event("state", s)
				case <-stateCtx.Done():
					return
				}
			}
		}()
		h.controller = engine.StartDesktopController(ctx, rt, func(s engine.DesktopState) {
			select {
			case states <- s:
			default:
				select {
				case <-states:
				default:
				}
				select {
				case states <- s:
				default:
				}
			}
		})
		return map[string]any{"session_id": rt.SessionID}, nil
	case "stop":
		h.close(o)
		return nil, nil
	case "meeting_start":
		h.stopPreview()
		if h.controller != nil || h.meeting != nil {
			return nil, errors.New("End the current conversation or meeting first")
		}
		m, err := startMeeting(ctx, r, o)
		if err != nil {
			return nil, err
		}
		h.meeting = m
		return map[string]string{"path": m.rt.Path}, nil
	case "meeting_stop":
		if h.meeting == nil {
			return nil, errors.New("No meeting is recording")
		}
		m := h.meeting
		h.meeting = nil
		return m.stop()
	case "meeting_note", "meeting_bookmark", "meeting_pause", "meeting_resume":
		if h.meeting == nil {
			return nil, errors.New("No meeting is recording")
		}
		return nil, h.meeting.control(r)
	default:
		if h.controller == nil {
			return nil, errors.New("Start a conversation first")
		}
		h.controller.Send(engine.DesktopAction{Kind: r.Method, Text: r.Text, Enabled: r.Enabled, Paths: r.Paths, Image: r.Image})
		return nil, nil
	}
}

type meetingHost struct {
	rt     *engine.DesktopMeeting
	ctx    context.Context
	cancel context.CancelFunc
	done   chan error
	o      *output
	paused bool
}

func startMeeting(ctx context.Context, r request, o *output) (*meetingHost, error) {
	rt, err := engine.BuildDesktopMeeting(ctx, r.Text, r.Resume, func(s string) { o.event("progress", s) })
	if err != nil {
		return nil, err
	}
	m := &meetingHost{rt: rt, ctx: ctx, o: o}
	m.listen()
	return m, nil
}
func (m *meetingHost) listen() {
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.done = make(chan error, 1)
	go func() {
		err := engine.RunDesktopMeeting(ctx, m.rt, m, engine.DesktopListenHooks{
			OnPhase: func(s string) { m.o.event("meeting_phase", s) }, OnPartial: func(s string) { m.o.event("meeting_partial", s) }, OnLevel: func(n float64) { m.o.event("meeting_level", n) }})
		if err != nil && !errors.Is(err, context.Canceled) {
			m.o.event("error", err.Error())
			m.o.event("meeting_stop_requested", nil)
		}
		m.done <- err
	}()
}
func (m *meetingHost) OnUtterance(u engine.DesktopUtterance) error {
	if engine.DesktopMeetingStopPhrase(u.Text) {
		m.cancel()
		m.o.event("meeting_stop_requested", nil)
		return nil
	}
	if err := m.rt.Writer.OnUtterance(u); err != nil {
		return err
	}
	m.o.event("meeting_utterance", u)
	return nil
}
func (m *meetingHost) OnTimeout() error { return nil }
func (m *meetingHost) OnError(err error) error {
	m.o.event("error", err.Error())
	return m.rt.Writer.OnError(err)
}
func (m *meetingHost) control(r request) error {
	switch r.Method {
	case "meeting_note":
		if strings.TrimSpace(r.Text) == "" {
			return errors.New("A note cannot be empty")
		}
		return m.rt.Writer.AddNote(r.Text)
	case "meeting_bookmark":
		return m.rt.Writer.AddBookmark("important", r.Text)
	case "meeting_pause":
		if !m.paused {
			m.cancel()
			<-m.done
			m.paused = true
			return m.rt.Writer.AppendControl("pause", time.Since(m.rt.Writer.StartedAt()).Milliseconds(), "", "")
		}
	case "meeting_resume":
		if m.paused {
			m.paused = false
			m.listen()
			return m.rt.Writer.AppendControl("resume", time.Since(m.rt.Writer.StartedAt()).Milliseconds(), "", "")
		}
	}
	return nil
}
func (m *meetingHost) stop() (any, error) {
	m.cancel()
	if !m.paused {
		<-m.done
	}
	if m.rt.ReleaseCapture != nil {
		m.rt.ReleaseCapture()
	}
	summary, err := m.rt.Writer.Close()
	// Preserve and expose the transcript before optional diarization.
	m.o.event("meeting_saved", summary)
	if m.rt.FinalizeSpeakers != nil {
		m.o.event("progress", "Analyzing meeting speakers")
		result, analysisErr := m.rt.FinalizeSpeakers(m.ctx)
		m.o.event("meeting_analysis", result)
		if analysisErr != nil {
			m.o.event("error", analysisErr.Error())
		}
	}
	m.rt.Cleanup()
	return summary, err
}
