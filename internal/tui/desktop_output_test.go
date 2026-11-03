package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type desktopOutputPlayer struct {
	*fakePreviewPlayer
	err     error
	resets  int
	capture audio.DiagnosticCaptureStatus
}

func (p *desktopOutputPlayer) ResetOutput(context.Context) error { p.resets++; return p.err }
func (p *desktopOutputPlayer) OutputStatus() audio.OutputStatus {
	return audio.OutputStatus{State: "idle"}
}
func (p *desktopOutputPlayer) StartDiagnosticCapture(context.Context) (audio.DiagnosticCaptureStatus, error) {
	p.capture.Active = true
	return p.capture, p.err
}
func (p *desktopOutputPlayer) StopDiagnosticCapture() audio.DiagnosticCaptureStatus {
	p.capture.Active = false
	return p.capture
}
func (p *desktopOutputPlayer) DiagnosticCaptureStatus() audio.DiagnosticCaptureStatus {
	return p.capture
}

func TestDesktopOutputResetPreservesConversationAndRetries(t *testing.T) {
	rt := fakeRuntime()
	rt.SessionID, rt.PersonaID = "same-session", "same-persona"
	rt.SessionCfg = &config.Config{TTSProvider: "openai-speech"}
	player := &desktopOutputPlayer{fakePreviewPlayer: newFakePreviewPlayer(), err: errors.New("device unavailable")}
	rt.Pipeline.Player = player
	a := App{cfg: rt.SessionCfg, conversation: newConversation("Same persona")}
	a.conversation.rememberUser("Keep this history")
	turnCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.conversation.turnCancel = cancel
	m := desktopModel{app: a, rt: rt}
	for _, fail := range []bool{true, false} {
		if !fail {
			player.err = nil
		}
		next, cmd := m.Update(DesktopAction{Kind: "audio_reset"})
		m = next.(desktopModel)
		if cmd == nil || !m.state().OutputActionBusy || player.resets != map[bool]int{true: 0, false: 1}[fail] {
			t.Fatal("reset must run asynchronously")
		}
		if turnCtx.Err() == nil {
			t.Fatal("foreground speech was not canceled")
		}
		_, duplicate := m.Update(DesktopAction{Kind: "audio_reset"})
		if duplicate != nil {
			t.Fatal("duplicate UI action dispatched")
		}
		next, _ = m.Update(cmd())
		m = next.(desktopModel)
		state := m.state()
		if state.OutputActionBusy || state.OutputError != fail {
			t.Fatalf("reset result: %+v", state)
		}
		if state.SessionID != "same-session" || rt.PersonaID != "same-persona" || rt.SessionCfg != a.cfg || len(state.Turns) != 1 || rt.Pipeline.Player != player {
			t.Fatal("reset replaced conversation state")
		}
		if !fail && !strings.Contains(state.OutputMessage, "next spoken reply") {
			t.Fatal("lazy reset must not claim successful reopen")
		}
	}
}

func TestDesktopOutputCaptureStatusAndUnavailableOutput(t *testing.T) {
	rt := fakeRuntime()
	m := desktopModel{app: App{conversation: newConversation("Obey")}, rt: rt}
	next, cmd := m.Update(DesktopAction{Kind: "audio_reset"})
	m = next.(desktopModel)
	if cmd != nil || !m.state().OutputError {
		t.Fatal("missing output was not reported")
	}
	player := &desktopOutputPlayer{fakePreviewPlayer: newFakePreviewPlayer(), capture: audio.DiagnosticCaptureStatus{Directory: "/temporary/capture"}}
	rt.Pipeline.Player = player
	for _, kind := range []string{"audio_capture_start", "audio_capture_stop"} {
		next, cmd = m.Update(DesktopAction{Kind: kind})
		m = next.(desktopModel)
		if cmd == nil {
			t.Fatal("capture action missing")
		}
		next, _ = m.Update(cmd())
		m = next.(desktopModel)
		state := m.state()
		if state.OutputCapture == nil || state.OutputCapture.Active != (kind == "audio_capture_start") || state.OutputCapture.Directory != "/temporary/capture" {
			t.Fatalf("capture state: %+v", state.OutputCapture)
		}
	}
}
