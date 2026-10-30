package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/charmbracelet/lipgloss"
)

type imageTurnRunner struct {
	fakeTurnRunner
	validationErr error
	images        []chatmedia.Ref
}

func (r *imageTurnRunner) ValidateImages(context.Context, []chatmedia.Ref) error {
	return r.validationErr
}
func (r *imageTurnRunner) RunTurnTextMode(ctx context.Context, text string) error {
	r.images = chatmedia.Input(ctx)
	return r.fakeTurnRunner.RunTurnTextMode(ctx, text)
}
func mediaTestModel(t *testing.T) (conversationModel, *imageTurnRunner) {
	t.Helper()
	config.SetConfigDirForTest(t, t.TempDir())
	r := &imageTurnRunner{}
	m := sizedConversation(t, 100, 40)
	m.deps = conversationDeps{runner: r, ctx: t.Context()}
	m.cfg = &config.Config{BrainProvider: "ollama"}
	cmd := m.importMedia(nil, testmedia.PNG())
	if cmd == nil {
		t.Fatal("missing import")
	}
	m.updateMedia(cmd())
	return m, r
}
func TestMediaFailedAdmissionRetainsDraft(t *testing.T) {
	m, r := mediaTestModel(t)
	r.validationErr = errors.New("model does not support images")
	m.input.SetValue("Look at this")
	cmd := m.handleSubmit()
	if cmd == nil {
		t.Fatal("missing validation")
	}
	m.updateMedia(cmd())
	if m.input.Value() != "Look at this" || len(m.media.draft) != 1 || len(r.texts()) != 0 || m.media.err == "" {
		t.Fatal("failed admission discarded/sent the draft")
	}
	for _, turn := range m.plainTurns {
		if turn.role == "user" {
			t.Fatal("rejected attachment was echoed as submitted")
		}
	}
}
func TestMediaImageOnlySubmissionAndBargeInKeepTheirAttachments(t *testing.T) {
	for _, barge := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "barge"}[barge], func(t *testing.T) {
			m, r := mediaTestModel(t)
			id := m.media.draft[0].ID
			if barge {
				m.turnState = turnVoiceResponding
				m.turnCancel = func() {}
			}
			validate := m.handleSubmit()
			cmd, _ := m.updateMedia(validate())
			if barge {
				if m.pendingText == "" || len(m.media.pending) != 1 {
					t.Fatal("cancel drain lost image")
				}
				cmd = m.handleVoiceTurnDone(voiceTurnDoneMsg{lifetime: m.deps.ctx})
			}
			if cmd == nil {
				t.Fatal("missing text dispatch")
			}
			cmd()
			if len(r.images) != 1 || r.images[0].ID != id || len(m.media.draft) != 0 {
				t.Fatal("model input lost or duplicated attachment")
			}
			if len(m.plainTurns) != 1 || len(m.plainTurns[0].attachments) != 1 || len(m.items[0].attachments) != 1 {
				t.Fatal("thread lost attachment")
			}
		})
	}
}
func TestMediaStaleImportAndChangedDraftCannotSend(t *testing.T) {
	m, _ := mediaTestModel(t)
	cmd := m.importMedia(nil, testmedia.GIF())
	m.clearTranscript()
	m.updateMedia(cmd())
	if len(m.media.draft) != 0 {
		t.Fatal("stale import crossed clear boundary")
	}
	m, _ = mediaTestModel(t)
	m.input.SetValue("Before")
	cmd = m.handleSubmit()
	m.input.SetValue("After")
	m.updateMedia(cmd())
	if m.input.Value() != "After" || len(m.media.draft) != 1 || len(m.plainTurns) != 0 {
		t.Fatal("validation sent a changed draft")
	}
}
func TestMediaTypedEscapedPathImportsWithoutSendingPath(t *testing.T) {
	m, r := mediaTestModel(t)
	m.media.draft = nil
	path := filepath.Join(t.TempDir(), "Screenshot with spaces.png")
	if err := os.WriteFile(path, testmedia.PNG(), 0600); err != nil {
		t.Fatal(err)
	}
	m.input.SetValue("\"" + path + "\"")
	cmd := m.handleSubmit()
	m.updateMedia(cmd())
	if m.input.Value() != "" || len(m.media.draft) != 1 || len(r.texts()) != 0 {
		t.Fatal("image filename was sent as prose")
	}
}
func TestDesktopProjectionIncludesDraftAndThreadMedia(t *testing.T) {
	m, _ := mediaTestModel(t)
	app := newApp(m.cfg, nil, nil)
	app.conversation = m
	desktop := desktopModel{app: app, rt: fakeRuntime()}
	state := desktop.state()
	if len(state.Attachments) != 1 || state.Attachments[0].Path == "" {
		t.Fatal("desktop draft missing asset")
	}
	m.media.pending = m.media.draft
	m.echoUserTurn("Look")
	desktop.app.conversation = m
	state = desktop.state()
	if len(state.Turns) != 1 || len(state.Turns[0].Attachments) != 1 {
		t.Fatal("desktop turn missing media")
	}
}

func TestCompactionRestoresUnsentImagesWithoutLeakingToNextTurn(t *testing.T) {
	m, r := mediaTestModel(t)
	m.turnState = turnVoiceResponding
	m.turnCancel = func() {}
	m.input.SetValue("Look at this")
	validate := m.handleSubmit()
	m.updateMedia(validate())
	m.discardDraft(m.pendingText)
	if len(m.media.pending) != 0 || len(m.media.draft) != 1 {
		t.Fatal("canceled barge-in did not restore image draft")
	}
	m.removeMedia("")
	m.dispatchTextTurn("Different message")()
	if len(r.images) != 0 {
		t.Fatal("old image leaked into unrelated turn")
	}
}

func TestMediaRejectedDraftShowsOneInlineError(t *testing.T) {
	m, r := mediaTestModel(t)
	r.validationErr = errors.New("model lfm3:1.7b does not support images; select a vision-capable Ollama model")
	m.input.SetValue("Inspect this screenshot")
	for range 4 {
		cmd := m.handleSubmit()
		if cmd == nil {
			t.Fatal("missing validation")
		}
		m.updateMedia(cmd())
	}
	if len(m.items) != 0 || len(r.texts()) != 0 {
		t.Fatal("rejected draft entered conversation")
	}
	if got := strings.Count(stripANSI(m.View()), "does not support images"); got != 1 {
		t.Fatalf("error shown %d times", got)
	}
	if len(m.media.draft) != 1 || m.input.Value() != "Inspect this screenshot" {
		t.Fatal("draft lost")
	}
	r.validationErr = nil
	cmd, _ := m.updateMedia(m.handleSubmit()())
	if cmd == nil {
		t.Fatal("corrected recipient did not accept retry")
	}
	cmd()
	if len(r.images) != 1 || m.media.err != "" {
		t.Fatal("retry lost image or retained error")
	}
}

func TestMediaStaleValidationDoesNotReportPreviousModelError(t *testing.T) {
	m, r := mediaTestModel(t)
	m.cfg.OllamaModel = "old"
	r.validationErr = errors.New("old model does not support images")
	cmd := m.handleSubmit()
	result := cmd()
	m.cfg.OllamaModel = "new"
	m.updateMedia(result)
	if m.media.err != "" || strings.Contains(stripANSI(m.View()), r.validationErr.Error()) {
		t.Fatal("old model error shown after model switch")
	}
}

func TestMediaRemovingRejectedAttachmentClearsError(t *testing.T) {
	m, r := mediaTestModel(t)
	r.validationErr = errors.New("model does not support images")
	m.updateMedia(m.handleSubmit()())
	m.removeMedia("")
	if m.media.err != "" || len(m.media.draft) != 0 {
		t.Fatal("removed draft retained error")
	}
}

func TestMediaDraftFitsResizedTerminal(t *testing.T) {
	for _, size := range [][2]int{{100, 40}, {40, 24}, {80, 16}} {
		for _, count := range []int{1, 4} {
			t.Run(fmt.Sprintf("%dx%d/%d", size[0], size[1], count), func(t *testing.T) {
				m, _ := mediaTestModel(t)
				ref := m.media.draft[0]
				for len(m.media.draft) < count {
					m.media.draft = append(m.media.draft, ref)
				}
				m.media.err = "model does not support images; select a vision-capable Ollama model"
				m.setSize(size[0], size[1])
				if got := lipgloss.Width(m.draftMediaView()); got > size[0] {
					t.Fatalf("draft width %d exceeds terminal width %d", got, size[0])
				}
				if m.viewport.Height < 1 {
					t.Fatal("draft hid the transcript")
				}
				if got := lipgloss.Height(m.View()); got > size[1] {
					t.Fatalf("view height %d exceeds terminal height %d", got, size[1])
				}
			})
		}
	}
}
