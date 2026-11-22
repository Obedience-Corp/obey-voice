package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	tea "github.com/charmbracelet/bubbletea"
)

// imageOnlyText is the line a draft with pictures and no text is sent as.
const imageOnlyText = "Attached image."

type mediaState struct {
	shownTurn                                        string
	err                                              string
	draft, pending                                   []chatmedia.Ref
	cache                                            map[string]mediaPreview
	token                                            *int
	importing, validating, approved, ticking, paused bool
	started                                          time.Time
	placed                                           map[mediaBox]int
	wantSignature                                    string

	// refused holds submissions whose turns were refused over their pictures
	// after they left the input, oldest first, until the input is free.
	refused []refusedDraft
	// returned counts the ones given back. It outlives a cleared conversation,
	// so that a paste asked for before the clear is not taken for a late one.
	returned int
	// inputElsewhere marks a front end that keeps its own text field, which
	// this model cannot see. Nothing is held or returned there.
	inputElsewhere bool
}

// refusedDraft is the text and pictures of a refused submission, and why.
type refusedDraft struct {
	text, reason string
	refs         []chatmedia.Ref
}
type mediaPreview struct {
	preview chatmedia.Preview
	err     error
}
type mediaImported struct {
	clearText string
	token     *int
	refs      []chatmedia.Ref
	previews  map[string]mediaPreview
	err       error
}
type mediaLoaded struct {
	token    *int
	previews map[string]mediaPreview
}
type mediaTick struct{ token *int }
type mediaValidated struct {
	model string
	token *int
	text  string
	refs  []chatmedia.Ref
	err   error
}

func (m *conversationModel) initMedia() {
	if m.media.token == nil {
		m.media.token = new(int)
		m.media.started = time.Now()
	}
	if m.media.cache == nil {
		m.media.cache = make(map[string]mediaPreview)
	}
}
func (m *conversationModel) importMedia(paths []string, data []byte) tea.Cmd {
	m.initMedia()
	if m.media.importing || m.media.validating {
		m.commandError("Wait for the current attachment operation")
		return nil
	}
	count := len(paths)
	if len(data) > 0 {
		count++
	}
	if count == 0 || count+len(m.media.draft) > chatmedia.MaxAttachments {
		m.commandError("Attach between one and four images")
		return nil
	}
	m.media.err = ""
	m.media.importing = true
	m.setStatus("Loading images…", false)
	token := m.media.token
	store := chatmedia.DefaultStore()
	return func() tea.Msg {
		result := mediaImported{token: token, previews: make(map[string]mediaPreview)}
		for _, path := range paths {
			r, err := store.ImportFile(path)
			if err != nil {
				result.err = err
				return result
			}
			result.refs = append(result.refs, r)
		}
		if len(data) > 0 {
			r, err := store.Import("", data)
			if err != nil {
				result.err = err
				return result
			}
			result.refs = append(result.refs, r)
		}
		for _, r := range result.refs {
			p, err := store.Preview(r)
			if err != nil {
				result.err = err
				return result
			}
			result.previews[r.ID] = mediaPreview{preview: p}
		}
		return result
	}
}
func (m *conversationModel) removeMedia(id string) {
	if m.media.validating || m.media.importing {
		return
	}
	for i, r := range m.media.draft {
		if r.ID == id || id == "" && i == len(m.media.draft)-1 {
			m.media.draft = append(m.media.draft[:i:i], m.media.draft[i+1:]...)
			m.media.err = ""
			break
		}
	}
	m.reflow()
}
func (m *conversationModel) prepareMediaSubmit(text string) (tea.Cmd, bool) {
	if len(m.media.draft) == 0 {
		return nil, false
	}
	if m.media.importing || m.media.validating || m.turnState == turnVoiceCanceling {
		return nil, true
	}
	if m.media.approved {
		m.media.approved = false
		m.media.pending = append([]chatmedia.Ref(nil), m.media.draft...)
		m.media.draft = nil
		m.reflow()
		return nil, false
	}
	m.initMedia()
	m.media.err = ""
	m.media.validating = true
	m.reflow()
	refs := append([]chatmedia.Ref(nil), m.media.draft...)
	token := m.media.token
	runner := m.deps.runner
	provider, model := m.activeBrain()
	modelTag := provider + "/" + model
	var validate func(context.Context, []chatmedia.Ref) error
	if owner, ok := runner.(interface {
		ImageValidator() func(context.Context, []chatmedia.Ref) error
	}); ok {
		validate = owner.ImageValidator()
	} else if v, ok := runner.(interface {
		ValidateImages(context.Context, []chatmedia.Ref) error
	}); ok {
		validate = v.ValidateImages
	}
	parent := m.deps.ctx
	if parent == nil {
		parent = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		defer cancel()

		var err error
		if validate == nil {
			err = errors.New("this model does not support image input; choose a vision-capable Ollama model")
		} else {
			err = validate(ctx, refs)
		}
		return mediaValidated{token: token, text: text, refs: refs, err: err, model: modelTag}
	}, true
}
func (m *conversationModel) mediaCommand() tea.Cmd {
	m.initMedia()
	var refs []chatmedia.Ref
	for _, item := range m.items {
		refs = append(refs, item.attachments...)
	}
	refs = append(refs, m.media.draft...)
	if len(refs) == 0 {
		return m.syncGraphics(nil)
	}
	// Keep a bounded preview working set; originals remain available through /image.
	keep := make(map[string]bool)
	frames := 0
	var recent []chatmedia.Ref
	if m.pictures.open {
		r := m.pictures.spot.ref
		keep[r.ID] = true
		frames += max(1, r.Frames)
		recent = append(recent, r)
	}
	for i := len(refs) - 1; i >= 0; i-- {
		r := refs[i]
		if keep[r.ID] {
			continue
		}
		if len(keep) >= 24 || frames+max(1, r.Frames) > 256 {
			continue
		}
		keep[r.ID] = true
		frames += max(1, r.Frames)
		recent = append(recent, r)
	}
	for id := range m.media.cache {
		if !keep[id] {
			delete(m.media.cache, id)
		}
	}
	var missing []chatmedia.Ref
	for _, r := range recent {
		if _, ok := m.media.cache[r.ID]; !ok {
			m.media.cache[r.ID] = mediaPreview{}
			missing = append(missing, r)
		}
	}
	token := m.media.token
	store := chatmedia.DefaultStore()
	cmds := []tea.Cmd{m.syncGraphics(keep)}
	if len(missing) > 0 {
		cmds = append(cmds, func() tea.Msg {
			out := make(map[string]mediaPreview)
			for _, r := range missing {
				p, err := store.Preview(r)
				out[r.ID] = mediaPreview{p, err}
			}
			return mediaLoaded{token, out}
		})
	}
	animated := false
	for _, r := range recent {
		if r.Frames > 1 {
			animated = true
		}
	}
	if animated && !m.media.paused && !m.reducedMotion && !m.media.ticking {
		m.media.ticking = true
		cmds = append(cmds, tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return mediaTick{token} }))
	}
	return tea.Batch(cmds...)
}
func (m *conversationModel) updateMedia(msg tea.Msg) (tea.Cmd, bool) {
	switch v := msg.(type) {
	case mediaImported:
		if v.token != m.media.token {
			return nil, true
		}
		m.media.importing = false
		if v.err != nil {
			m.media.err = v.err.Error()
			m.commandError("Image import: " + v.err.Error())
			return nil, true
		}
		if v.clearText != "" && m.input.Value() == v.clearText {
			previous := m.input.Value()
			m.input.Reset()
			m.editor.sync("", 0)
			m.syncComposer(previous)
		}
		m.media.draft = append(m.media.draft, v.refs...)
		for id, p := range v.previews {
			m.media.cache[id] = p
		}
		m.setStatus("Image attached", false)
		m.reflow()
		return m.mediaCommand(), true
	case mediaLoaded:
		if v.token != m.media.token {
			return nil, true
		}
		for id, p := range v.previews {
			if _, wanted := m.media.cache[id]; wanted {
				m.media.cache[id] = p
			}
		}
		m.refreshContent()
		return nil, true
	case mediaPlaced:
		if v.token != m.media.token {
			return nil, true
		}
		m.media.placed = v.placed
		m.reflow()
		m.refreshContent()
		m.applyFollow()
		return nil, true
	case mediaTick:
		if v.token != m.media.token {
			return nil, true
		}
		m.media.ticking = false
		if !m.media.paused && !m.reducedMotion {
			m.refreshContent()
			m.applyFollow()
		}
		return m.mediaCommand(), true
	case mediaValidated:
		if v.token != m.media.token {
			return nil, true
		}
		m.media.validating = false
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			text = imageOnlyText
		}
		provider, model := m.activeBrain()
		if text != v.text || provider+"/"+model != v.model || !reflect.DeepEqual(v.refs, m.media.draft) {
			m.commandError("Draft changed; send again when ready")
			return nil, true
		}
		if v.err != nil {
			// This is an unsent draft error, not a conversation event. Keep a
			// single message beside the attachment even after repeated Enter.
			m.media.err = v.err.Error()
			m.reflow()
			return nil, true
		}
		m.media.err = ""
		m.media.approved = true
		return m.handleSubmit(), true
	}
	return nil, false
}
func (m *conversationModel) attachEchoMedia() {
	if len(m.media.pending) == 0 {
		return
	}
	refs := append([]chatmedia.Ref(nil), m.media.pending...)
	if len(m.plainTurns) > 0 {
		m.plainTurns[len(m.plainTurns)-1].attachments = refs
	}
	if len(m.items) > 0 {
		m.items[len(m.items)-1].attachments = refs
	}
	m.refreshContent()
}

type desktopMedia struct {
	chatmedia.Ref
	Path string `json:"path"`
}

func desktopMediaRefs(refs []chatmedia.Ref) []desktopMedia {
	out := make([]desktopMedia, 0, len(refs))
	for _, r := range refs {
		out = append(out, desktopMedia{r, chatmedia.DefaultStore().Path(r)})
	}
	return out
}
func mediaSummary(r chatmedia.Ref) string {
	label := fmt.Sprintf("%s · %d×%d", safeWorkerText(r.Name), r.Width, r.Height)
	if r.Frames > 1 {
		label += " · GIF (model input: first frame)"
	}
	return label
}

// holdRefusedDraft keeps a submission whose turn was refused over its pictures
// after it left the input, for instance when the message named a persona that
// cannot take one. That turn sent and recorded nothing. The refusal is
// reported here, and returnRefusedDraft gives the submission back.
func (m *conversationModel) holdRefusedDraft(msg textTurnDoneMsg) bool {
	if m.media.inputElsewhere || !pipeline.ImagesRefused(msg.err) || len(msg.refs) == 0 || msg.media != m.media.token {
		return false
	}
	held := refusedDraft{text: msg.input, reason: msg.err.Error(), refs: msg.refs}
	if held.text == imageOnlyText {
		held.text = ""
	}
	m.emit(events.Error{Message: held.reason})
	// The error stands under the message it is about unless that message
	// waits or another was sent after it. Then a line says which one it was.
	waits := !m.inputFree()
	if waits || m.turnState == turnVoiceCanceling {
		m.emit(events.Info{Message: held.unsentNotice(waits)})
	}
	m.media.refused = append(m.media.refused, held)
	return true
}

func (d refusedDraft) unsentNotice(waits bool) string {
	notice := "Your picture message was not sent."
	if words := strings.Fields(d.text); len(words) > 0 {
		quoted := []rune(strings.Join(words, " "))
		if len(quoted) > 60 {
			quoted = append(quoted[:60], '…')
		}
		notice = fmt.Sprintf("Your message %q was not sent.", string(quoted))
	}
	if !waits {
		return notice
	}
	pictures := "picture"
	if len(d.refs) > 1 {
		pictures = "pictures"
	}
	return notice + " It returns to the input with its " + pictures + " once the input is empty."
}

// inputFree reports an input with no text and no pictures, and none being
// imported or waiting to be sent. A draft being checked still holds its
// pictures.
func (m *conversationModel) inputFree() bool {
	return !m.media.importing && len(m.media.draft) == 0 && len(m.media.pending) == 0 && strings.TrimSpace(m.input.Value()) == ""
}

// returnRefusedDraft gives the oldest held submission back once the input is
// free, with the reason beside its pictures as for a draft refused before it
// was sent. It goes only into an empty input, never into text or pictures
// that are already there.
func (m *conversationModel) returnRefusedDraft() tea.Cmd {
	if len(m.media.refused) == 0 || !m.inputFree() {
		return nil
	}
	held := m.media.refused[0]
	m.media.refused = m.media.refused[1:]
	m.media.returned++
	m.media.draft = append([]chatmedia.Ref(nil), held.refs...)
	m.media.approved = false
	m.media.err = held.reason
	previous := m.input.Value()
	m.input.SetValue(held.text)
	m.syncEditorFromTextarea()
	m.editor.resetUndo()
	m.syncComposer(previous)
	m.reflow()
	return m.mediaCommand()
}

// A canceled barge-in must not lend its images to the next unrelated message.
func (m *conversationModel) restorePendingMedia() {
	if len(m.media.pending) == 0 {
		return
	}
	m.media.draft = append(m.media.pending, m.media.draft...)
	m.media.pending = nil
	m.media.approved = false
	m.reflow()
}

func (m *conversationModel) attachAssistantMedia(refs []chatmedia.Ref) {
	if len(m.plainTurns) > 0 {
		m.plainTurns[len(m.plainTurns)-1].attachments = append([]chatmedia.Ref(nil), refs...)
	}
}

func (m conversationModel) workResultImages() [][]desktopMedia {
	var out [][]desktopMedia
	seen := map[string]bool{}
	for _, item := range m.items {
		id := item.workResultID
		if id != "" && !seen[id] {
			seen[id] = true
			if m.workResultText(id) != "" {
				out = append(out, desktopMediaRefs(item.attachments))
			}
		}
	}
	return out
}
