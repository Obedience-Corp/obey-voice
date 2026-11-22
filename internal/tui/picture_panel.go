package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// pictureSpot is one picture of one entry. The same picture attached to two
// entries is two spots.
type pictureSpot struct {
	entry, attachment int
	ref               chatmedia.Ref
}

// picturePanel shows one picture of the conversation with what can be done
// with it. While it is open it owns every key and mouse event.
type picturePanel struct {
	open   bool
	spot   pictureSpot
	cursor int
	saving bool
	// busy is a save still being written; a second Enter must not start
	// another that then reports the first one's file as already there.
	busy   bool
	input  textinput.Model
	result string
	failed bool
	// generation changes whenever the panel shows something else, so a
	// result that arrives late is not reported against another picture.
	generation int
}

const (
	pictureOpen = iota
	pictureCopy
	pictureCopyPath
	pictureSave
)

var pictureActionLabels = [...]string{"Open the original", "Copy the picture", "Copy its path", "Save a copy…"}

type pictureActionDone struct {
	generation int
	text       string
	err        error
	saved      bool
}

func (m conversationModel) pictureSpots() []pictureSpot {
	var spots []pictureSpot
	for i, item := range m.items {
		for j, r := range item.attachments {
			spots = append(spots, pictureSpot{i, j, r})
		}
	}
	return spots
}

// pictureIndex finds the shown picture among spots, by place first and then
// by content in case the transcript was rebuilt.
func (p picturePanel) pictureIndex(spots []pictureSpot) int {
	for i, s := range spots {
		if s == p.spot {
			return i
		}
	}
	for i, s := range spots {
		if s.ref.ID == p.spot.ref.ID {
			return i
		}
	}
	return -1
}

func (m *conversationModel) openPicturePanel(spot pictureSpot) tea.Cmd {
	m.initMedia()
	m.pictures = picturePanel{open: true, spot: spot, generation: m.pictures.generation + 1}
	return m.mediaCommand()
}

func (m *conversationModel) closePicturePanel() tea.Cmd {
	if !m.pictures.open {
		return nil
	}
	m.pictures = picturePanel{generation: m.pictures.generation + 1}
	return m.mediaCommand()
}

func (m *conversationModel) movePicture(step int) tea.Cmd {
	spots := m.pictureSpots()
	i := m.pictures.pictureIndex(spots) + step
	if i < 0 || i >= len(spots) {
		return nil
	}
	cursor := m.pictures.cursor
	m.pictures = picturePanel{open: true, spot: spots[i], cursor: cursor, generation: m.pictures.generation + 1}
	return m.mediaCommand()
}

func (m *conversationModel) updatePicturePanelKey(msg tea.KeyMsg) tea.Cmd {
	p := &m.pictures
	if p.saving {
		switch msg.String() {
		case "esc":
			p.saving, p.result, p.failed = false, "", false
			return nil
		case "enter":
			if p.busy {
				return nil
			}
			return m.runPictureSave()
		}
		// The input's own paste and blink commands answer to a model that no
		// longer exists by the time they return; a terminal paste still types.
		p.input.Width = m.pictureSaveInputWidth()
		p.input, _ = p.input.Update(msg)
		return nil
	}
	switch msg.String() {
	case "esc":
		return m.closePicturePanel()
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = min(len(pictureActionLabels)-1, p.cursor+1)
	case "left", "h":
		return m.movePicture(-1)
	case "right", "l":
		return m.movePicture(1)
	case "enter":
		return m.runPictureAction(p.cursor)
	}
	return nil
}

// A click outside the box closes the panel and a click on an action runs it.
// Nothing reaches the transcript underneath.
func (m *conversationModel) updatePicturePanelMouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return nil
	}
	g, _ := m.picturePanelLayout()
	row := msg.Y - g.top
	if row < 0 || row >= g.boxRows || msg.X < 2 || msg.X >= 2+g.boxWidth {
		return m.closePicturePanel()
	}
	if m.pictures.saving {
		return nil
	}
	if msg.X < g.actionX || msg.X >= g.actionX+g.actionW {
		return nil
	}
	for i, actionRow := range g.actionRows {
		if row == actionRow {
			m.pictures.cursor = i
			return m.runPictureAction(i)
		}
	}
	return nil
}

func (m *conversationModel) runPictureAction(action int) tea.Cmd {
	p := &m.pictures
	r, generation := p.spot.ref, p.generation
	p.failed = false
	switch action {
	case pictureOpen:
		p.result = "Opening the original…"
		run := m.openPictureRun(chatmedia.DefaultStore().Path(r))
		return func() tea.Msg {
			return pictureActionDone{generation: generation, text: "Opened the original", err: run()}
		}
	case pictureCopy:
		p.result = "Copying the picture…"
		run := m.copyPictureRun(r)
		return func() tea.Msg {
			return pictureActionDone{generation: generation, text: "Copied the picture", err: run()}
		}
	case pictureCopyPath:
		if err := m.clipboard().WriteAll(chatmedia.DefaultStore().Path(r)); err != nil {
			p.result, p.failed = "copy failed: "+err.Error(), true
			return nil
		}
		p.result = "Copied the path"
	case pictureSave:
		input := textinput.New()
		input.Prompt = ""
		input.CharLimit = 4096
		input.KeyMap.Paste.SetEnabled(false)
		input.Width = m.pictureSaveInputWidth()
		input.SetValue(defaultSaveTarget(r))
		input.CursorEnd()
		input.Focus()
		p.input, p.saving, p.result = input, true, ""
	}
	return nil
}

func (m *conversationModel) runPictureSave() tea.Cmd {
	p := &m.pictures
	r, generation, target := p.spot.ref, p.generation, p.input.Value()
	p.result, p.failed, p.busy = "Saving…", false, true
	store := chatmedia.DefaultStore()
	return func() tea.Msg {
		path, err := savePictureCopy(store, r, target)
		return pictureActionDone{generation: generation, text: savedPrefix + path, err: err, saved: true}
	}
}

func (m *conversationModel) finishPictureAction(msg pictureActionDone) {
	p := &m.pictures
	if !p.open || msg.generation != p.generation {
		if msg.err != nil {
			m.commandError("picture: " + msg.err.Error())
		} else {
			m.commandNotice(msg.text)
		}
		return
	}
	p.result, p.failed = msg.text, msg.err != nil
	if msg.saved {
		p.busy = false
	}
	if msg.err != nil {
		// The result is one row of the box.
		p.result = strings.Join(strings.Fields(msg.err.Error()), " ")
		return
	}
	if msg.saved {
		p.saving = false
	}
}

const savedPrefix = "Saved to "

// keepEnd shortens a path from the left, where it is least telling.
func keepEnd(path string, width int) string {
	if w := ansi.StringWidth(path); w > width {
		return ansi.TruncateLeft(path, w-width+1, "…")
	}
	return path
}

type picturePanelGeometry struct {
	top, rows          int
	boxWidth, boxRows  int
	inner              int
	side               bool
	previewW, previewH int
	// actionX and actionRows place the action list on the screen.
	actionX, actionW int
	actionRows       []int
}

const (
	pictureInfoWidth = 30
	pictureInfoRows  = 3 + len(pictureActionLabels)
	pictureActionW   = 20
	// path, result, save input, key hints
	pictureFooterRows = 4
)

// The panel takes the rows between the header and the footer, so the
// preview can be larger than the picture in the transcript. The actions sit
// beside the preview or under it, whichever leaves this picture more cells.
func (m conversationModel) picturePanelGeometry() picturePanelGeometry {
	g := picturePanelGeometry{top: 2 + m.voicePanelHeight()}
	g.rows = max(1, m.height-g.top-1)
	g.boxWidth = max(16, m.width-4)
	g.inner = g.boxWidth - 4
	r := m.pictures.spot.ref
	sideW, sideH := g.inner-pictureInfoWidth-2, g.rows-2-pictureFooterRows
	underW, underH := g.inner, max(0, g.rows-2-pictureFooterRows-len(pictureActionLabels))
	g.side = sideW >= 20 && sideH >= pictureInfoRows && m.previewCells(r, sideW, sideH) > m.previewCells(r, underW, underH)
	// Screen column of the box's first inner cell: indent, border, space.
	const innerX = 4
	if g.side {
		g.previewW, g.previewH = sideW, sideH
		g.actionX, g.actionW = innerX+sideW+2, pictureInfoWidth
	} else {
		g.previewW, g.previewH = underW, underH
		g.actionW = min(pictureActionW, g.inner)
		g.actionX = innerX + g.inner - g.actionW
	}
	return g
}

func (m conversationModel) pictureSaveInputWidth() int {
	return max(1, m.picturePanelGeometry().inner-len("Save as ")-1)
}

// panelLine is one row inside the box. On a screen too short for all of them
// the rows with the lowest keep go first, so the save field, a result and the
// selected action outlast the path, the hints and blank rows.
type panelLine struct {
	text   string
	keep   int
	action int
}

const (
	keepBlank = iota
	keepIdleAction
	keepPath
	keepHint
	keepAction
	keepSelected
	keepAlways
)

// previewCells is how many cells the picture covers when fitted into
// cols×rows, by the renderer that will draw it.
func (m conversationModel) previewCells(r chatmedia.Ref, cols, rows int) int {
	if cols <= 0 || rows <= 0 {
		return 0
	}
	if box := m.graphics.box(r, cols, rows); box.id != "" {
		return box.cols * box.rows
	}
	// Half-blocks draw the preview, which is at most 160×100, two pixels per cell.
	w, h := fitInside(r.Width, r.Height, 160, 100)
	w, h = fitInside(w, h, cols, rows*2)
	return w * ((h + 1) / 2)
}

// fitInside mirrors chatmedia.Thumbnail: fit without enlarging, keep the shape.
func fitInside(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 {
		return 1, 1
	}
	fw, fh := max(1, min(maxW, w)), max(1, min(maxH, h))
	if fw*h <= fh*w {
		return fw, max(1, fw*h/w)
	}
	return max(1, fh*w/h), fh
}

func (m conversationModel) picturePanelView() string {
	g, lines := m.picturePanelLayout()
	body := make([]string, len(lines))
	for i, line := range lines {
		body[i] = line.text
	}
	spots := m.pictureSpots()
	title := "Picture"
	if i := m.pictures.pictureIndex(spots); i >= 0 {
		title = fmt.Sprintf("Picture %d of %d", i+1, len(spots))
	}
	box := formBoxStyled(headerStyle.Render(title), body, g.boxWidth, lipgloss.NewStyle().Foreground(colorAccent))
	for len(box) < g.rows {
		box = append(box, "")
	}
	return strings.Join(box[:g.rows], "\n")
}

// picturePanelLayout is the rows the box shows and where its actions landed on
// the screen, so the view and the mouse agree.
func (m conversationModel) picturePanelLayout() (picturePanelGeometry, []panelLine) {
	g := m.picturePanelGeometry()
	p := m.pictures
	r := p.spot.ref

	var preview []string
	if g.previewH > 0 {
		for line := range strings.SplitSeq(m.mediaBody(r, g.previewW, g.previewH), "\n") {
			line = ansi.Truncate(line, g.previewW, "…")
			preview = append(preview, line+strings.Repeat(" ", max(0, g.previewW-ansi.StringWidth(line))))
		}
		for len(preview) < g.previewH {
			preview = append(preview, strings.Repeat(" ", g.previewW))
		}
		preview = preview[:g.previewH]
	}

	size := fmt.Sprintf("%d×%d · %s", r.Width, r.Height, strings.ToUpper(r.Format))
	if r.Frames > 1 {
		size += fmt.Sprintf(" · %d frames", r.Frames)
	}
	about := []string{headerStyle.Render(safeWorkerText(r.Name)), dimStyle.Render(size)}
	actionKeep := func(i int) int {
		switch {
		case p.saving:
			return keepIdleAction
		case i == p.cursor:
			return keepSelected
		}
		return keepAction
	}
	var actions []string
	for i, label := range pictureActionLabels {
		if i == p.cursor && !p.saving {
			actions = append(actions, selectedStyle.Render("› "+label))
		} else {
			actions = append(actions, normalStyle.Render("  "+label))
		}
	}

	fit := func(line string, width int) string {
		line = ansi.Truncate(line, width, "…")
		return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
	}
	var lines []panelLine
	if g.side {
		info := append(append(about, ""), actions...)
		for i, line := range preview {
			row := panelLine{text: line + "  ", keep: keepAlways}
			if i < len(info) {
				row.text += ansi.Truncate(info[i], g.actionW, "…")
			}
			if a := i - len(about) - 1; a >= 0 && a < len(actions) {
				row.action = a + 1
			}
			lines = append(lines, row)
		}
	} else {
		for _, line := range preview {
			lines = append(lines, panelLine{text: line, keep: keepAlways})
		}
		aboutW := max(0, g.inner-g.actionW-2)
		for i, action := range actions {
			left := ""
			if i < len(about) {
				left = about[i]
			}
			text := fit(left, aboutW) + strings.Repeat(" ", max(0, g.inner-g.actionW-aboutW)) + fit(action, g.actionW)
			lines = append(lines, panelLine{text: text, keep: actionKeep(i), action: i + 1})
		}
	}

	home, _ := os.UserHomeDir()
	lines = append(lines, panelLine{text: dimStyle.Render(keepEnd(collapseHome(chatmedia.DefaultStore().Path(r), home), g.inner)), keep: keepPath})
	switch {
	case p.result == "":
		lines = append(lines, panelLine{})
	case p.failed:
		lines = append(lines, panelLine{text: errorStyle.Render(keepEnd(p.result, g.inner)), keep: keepAlways})
	default:
		result := p.result
		if path, ok := strings.CutPrefix(result, savedPrefix); ok {
			result = savedPrefix + keepEnd(collapseHome(path, home), g.inner-len(savedPrefix))
		}
		lines = append(lines, panelLine{text: statusStyle.Render(result), keep: keepAlways})
	}
	hint := "↑↓ choose · enter run · ←→ pictures · esc close"
	if p.saving {
		p.input.Width = m.pictureSaveInputWidth()
		lines = append(lines, panelLine{text: "Save as " + p.input.View(), keep: keepAlways})
		hint = "enter save · esc back to actions"
	} else {
		lines = append(lines, panelLine{})
	}
	lines = append(lines, panelLine{text: dimStyle.Render(hint), keep: keepHint})

	for room := max(1, g.rows-2); len(lines) > room; {
		drop := 0
		for i, line := range lines {
			if line.keep <= lines[drop].keep {
				drop = i
			}
		}
		lines = append(lines[:drop], lines[drop+1:]...)
	}
	g.boxRows = len(lines) + 2
	g.actionRows = make([]int, len(pictureActionLabels))
	for i := range g.actionRows {
		g.actionRows[i] = -1
	}
	for i, line := range lines {
		if line.action > 0 {
			g.actionRows[line.action-1] = 1 + i
		}
	}
	return g, lines
}

// pictureHit maps a screen cell to the transcript picture drawn there: its
// name line or its body, never the blank cells beside them. The rows are
// measured the way refreshContent wraps them, and the cell is read from the
// rendered screen.
func (m conversationModel) pictureHit(x, y int) (pictureSpot, bool) {
	if !m.ready || m.activityFocused || m.startup.active || x < 0 || y < 0 || x >= m.width || y >= m.height {
		return pictureSpot{}, false
	}
	row := y - 2 - m.voicePanelHeight()
	if row < 0 || row >= m.viewport.Height {
		return pictureSpot{}, false
	}
	screen := strings.Split(m.View(), "\n")
	if y >= len(screen) || x >= ansi.StringWidth(strings.TrimRight(ansi.Strip(screen[y]), " ")) {
		return pictureSpot{}, false
	}
	line := m.viewport.YOffset + row
	wrap := lipgloss.NewStyle().Width(m.viewport.Width)
	width := max(1, m.viewport.Width-4)
	var hit pictureSpot
	found, total := false, 0
	for i, it := range m.items {
		s := it.render(m.showThink, m.viewport.Width)
		if s == "" {
			continue
		}
		total += lipgloss.Height(wrap.Render(s))
		for j, r := range it.attachments {
			h := lipgloss.Height(wrap.Render(m.renderMedia([]chatmedia.Ref{r}, width, m.threadPreviewRows(len(it.attachments)))))
			if !found && line >= total && line < total+h {
				hit, found = pictureSpot{i, j, r}, true
			}
			total += h
		}
	}
	if m.streamingThink != "" || m.streamingAgent != "" {
		total += lipgloss.Height(wrap.Render(renderAgentBubble(m.agentName, m.streamingThink, m.streamingAgent, m.streamingThink != "")))
	}
	// A transcript that changed since it was drawn cannot say what is where.
	if !found || total != m.viewport.TotalLineCount() {
		return pictureSpot{}, false
	}
	return hit, true
}
