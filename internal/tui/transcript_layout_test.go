package tui

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// transcriptByFullRender draws every item and wraps the joined transcript in
// one pass: the reference the cached layout must reproduce byte for byte.
func transcriptByFullRender(m conversationModel) (parts []string, content string, rows []workResultRows) {
	wrap := lipgloss.NewStyle().Width(max(m.viewport.Width, 1))
	line := 0
	for _, it := range m.items {
		s := it.render(m.showThink, m.viewport.Width)
		if s == "" {
			continue
		}
		h := lipgloss.Height(wrap.Render(s))
		if it.workResultID != "" {
			rows = append(rows, workResultRows{id: it.workResultID, start: line, end: line + h})
		}
		line += h
		if len(it.attachments) > 0 {
			s += "\n" + m.renderMedia(it.attachments, max(1, m.viewport.Width-4), m.threadPreviewRows(len(it.attachments)))
		}
		parts = append(parts, s)
	}
	content = strings.Join(parts, "\n")
	if m.streamingThink != "" || m.streamingAgent != "" {
		live := renderAgentBubble(m.agentName, m.streamingThink, m.streamingAgent, m.streamingThink != "")
		if content != "" {
			content += "\n" + live
		} else {
			content = live
		}
	}
	return parts, lipgloss.NewStyle().Width(m.viewport.Width).Render(content), rows
}

func assertTranscriptMatches(t *testing.T, m conversationModel, step string) {
	t.Helper()
	parts, want, rows := transcriptByFullRender(m)
	if m.shown.content != want {
		gotLines, wantLines := strings.Split(m.shown.content, "\n"), strings.Split(want, "\n")
		for i := range min(len(gotLines), len(wantLines)) {
			if gotLines[i] != wantLines[i] {
				t.Fatalf("%s: line %d differs\n got %q\nwant %q", step, i, gotLines[i], wantLines[i])
			}
		}
		t.Fatalf("%s: %d lines, want %d", step, len(gotLines), len(wantLines))
	}
	if m.viewport.TotalLineCount() != strings.Count(want, "\n")+1 {
		t.Fatalf("%s: viewport holds %d lines, want %d", step, m.viewport.TotalLineCount(), strings.Count(want, "\n")+1)
	}
	if !slices.Equal(m.transcript, parts) {
		t.Fatalf("%s: transcript parts differ", step)
	}
	if !slices.Equal(m.workResultRows, rows) {
		t.Fatalf("%s: result rows %v, want %v", step, m.workResultRows, rows)
	}
}

func layoutTestConversation(t *testing.T, width, height int) conversationModel {
	t.Helper()
	truecolorTB(t)
	m := newConversation("Agent A")
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func stillPreview() mediaPreview {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for x := range 8 {
		for y := range 6 {
			img.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 40), 90, 255})
		}
	}
	return mediaPreview{preview: chatmedia.Preview{Frames: []image.Image{img}}}
}

func TestTranscriptMatchesFullRenderThroughEveryChange(t *testing.T) {
	m := layoutTestConversation(t, 100, 30)
	step := func(name string) {
		t.Helper()
		assertTranscriptMatches(t, m, name)
	}
	// handleEvent, not Update: Update also asks for picture previews after the
	// refresh, and the reference must see the state the refresh saw.
	send := func(name string, e events.Event) {
		t.Helper()
		m.handleEvent(e)
		step(name)
	}
	for i := range 48 {
		m.items = append(m.items, fixtureItem(i))
	}
	m.refreshContent()
	step("fixture")
	if !m.shown.joined {
		t.Fatal("fixture transcript did not use the per-item layout")
	}

	send("user input", events.UserInput{Text: "Hello there 🙂 天気"})
	send("thinking started", events.ThinkingStarted{})
	for i, d := range []string{"Weighing ", "the two\n", "plans\tcarefully."} {
		send(fmt.Sprintf("thinking delta %d", i), events.ThinkingDelta{Text: d})
	}
	for i, d := range []string{"Plan two ", "is cheaper, ", "@Agent B agrees. ", strings.Repeat("long words ", 30)} {
		send(fmt.Sprintf("response delta %d", i), events.ResponseDelta{Text: d})
	}
	send("response ready", events.ResponseReady{Response: "Plan two is cheaper, @Agent B agrees."})
	send("tool started", events.ToolCallStarted{Name: "search", Summary: "plans"})
	send("tool failed", events.ToolCallFinished{Name: "search", Outcome: &events.ToolOutcome{Status: events.ToolOutcomeFailed, Reason: "exit status 1"}})
	send("tool ok", events.ToolCallFinished{Name: "search"})
	send("info notice", events.Info{Message: "Notice\twith tabs and a long tail of words that wraps across the viewport and keeps its indent"})
	send("error notice", events.Error{Stage: "brain", Message: "provider said no " + strings.Repeat("ε", 40)})
	send("think-only reply", events.ResponseReady{Response: "<think>only thinking</think>"})

	m.setShowThink(true)
	step("thinking shown")
	for _, w := range []int{47, 20, 4, 3, 2, 1, 133} {
		m.setSize(w, 30)
		step(fmt.Sprintf("width %d", w))
	}
	m.setShowThink(false)
	step("thinking hidden")

	send("job queued", events.JobQueued{ID: "job-1", Task: "inspect files"})
	send("job started", events.JobStarted{ID: "job-1", Task: "inspect files"})
	send("job tool started", events.ToolCallStarted{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read", Summary: "notes.txt"})
	send("job tool finished", events.ToolCallFinished{ID: "c1", JobID: "job-1", Name: "read_file", Kind: "read"})
	send("job report", events.JobReport{ID: "job-1", Checkpoint: "half", Answer: true})
	send("job aborted stopping", events.JobAborted{ID: "job-1", Task: "inspect files", Elapsed: 5 * time.Second, Stopping: true})
	send("job exited", events.JobExited{ID: "job-1"})
	send("ended job correction", events.JobCorrection{ID: "job-1", Revision: 1, State: "not_applied", Reason: "the job was stopped"})
	send("job 2 started", events.JobStarted{ID: "job-2", Task: "summarize"})
	send("job 2 finished", events.JobFinished{ID: "job-2", Task: "summarize", Elapsed: 9 * time.Second, Summary: "Done with the summary."})
	send("continued reply", events.ResponseReady{Response: "Starting on it.", ContinuedAs: "job-3"})
	send("continued not taken over", events.JobAborted{ID: "job-3", Reason: events.AbortNotTakenOver})

	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	snap := fixtureWorkSnapshot(2)
	snap.Pending[1].Outcome.Attachments = []chatmedia.Ref{{ID: "result-img", Name: "plot.png", Format: "png", Width: 320, Height: 200, Frames: 1}}
	send("work results", events.WorkStateChanged{Snapshot: snap})
	snap.Revision++
	snap.Pending[0].State = work.Communicated
	snap.Pending[0].Outcome.Text = "Rewritten result text"
	send("work result rewritten", events.WorkStateChanged{Snapshot: snap})
	send("result correction", events.JobCorrection{ID: snap.Pending[0].Outcome.JobID, Revision: 1, State: "included", Scope: work.WorkScope{}})
	snap.Revision++
	snap.Pending = snap.Pending[1:]
	send("work result removed", events.WorkStateChanged{Snapshot: snap})
	other := fixtureWorkSnapshot(1)
	other.Scope = work.WorkScope{SessionID: "s2", RuntimeEpoch: "r2", ConversationEpoch: "c2"}
	for i := range other.Pending {
		other.Pending[i].Outcome.Scope = other.Scope
		other.Pending[i].DeliveryKey.Scope = other.Scope
	}
	send("job 4 running", events.JobStarted{ID: "job-4", Task: "still running"})
	send("work scope changed", events.WorkStateChanged{Snapshot: other})
	_ = scope

	input := &utterance.Finalized{ID: "voice-1", CaptureEpoch: 3, Transcript: utterance.Transcript{State: utterance.Pending}}
	send("voice input", events.UserInput{Utterance: input})
	send("caption", events.InputTranscriptUpdated{UtteranceID: input.ID, CaptureEpoch: 3, Transcript: utterance.Transcript{State: utterance.Ready, Text: "the caption arrived"}})

	refs := []chatmedia.Ref{{ID: "echo-img", Name: "photo.png", Format: "png", Width: 64, Height: 48, Frames: 1}}
	m.media.pending = refs
	m.echoUserTurn("look at this")
	step("echo with picture")
	_ = m.mediaCommand()
	m.refreshContent()
	step("previews requested")
	m, _ = m.Update(mediaLoaded{token: m.media.token, previews: map[string]mediaPreview{"echo-img": stillPreview(), "result-img": stillPreview()}})
	step("previews loaded")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	step("pictures paused")
	m, _ = m.Update(mediaPlaced{token: m.media.token, placed: map[mediaBox]int{}})
	step("pictures placed")
	send("reply with pictures", events.ReplyImagesReady{Response: "Here are the charts.", Attachments: refs})
	m.setSize(61, 44)
	step("taller viewport")

	edits := []struct {
		name string
		edit func(it *chatItem)
	}{
		{"sys", func(it *chatItem) { it.sys += " edited" }},
		{"hang", func(it *chatItem) { it.hang = "a new hanging notice that wraps " + strings.Repeat("again ", 20) }},
		{"hang style", func(it *chatItem) { it.hangStyle = &errorStyle }},
		{"name", func(it *chatItem) { it.name = "Agent C" }},
		{"hosted", func(it *chatItem) { it.hosted = !it.hosted }},
		{"think", func(it *chatItem) { it.think = "new thought" }},
		{"think label", func(it *chatItem) { it.thinkLabel = !it.thinkLabel }},
		{"spoken", func(it *chatItem) { it.spoken = "new words 🙂" }},
		{"kind", func(it *chatItem) { it.kind = chatItemAgent - it.kind }},
		{"attachments", func(it *chatItem) { it.attachments = refs }},
		{"work result", func(it *chatItem) { it.workResultID = "result-1" }},
		{"cleared", func(it *chatItem) { *it = chatItem{kind: chatItemSys, sys: dimStyle.Render("  replaced")} }},
	}
	for _, show := range []bool{false, true} {
		m.setShowThink(show)
		for _, e := range edits {
			for _, i := range []int{1, 3, 5, len(m.items) - 1} {
				e.edit(&m.items[i])
				m.refreshContent()
				step(fmt.Sprintf("edit %s of item %d (thinking %t)", e.name, i, show))
			}
		}
	}

	send("cleared", events.ConversationCleared{})
	m.seedTranscript([]brain.Turn{
		{Role: "user", Content: "saved question", Speaker: "Speaker 1"},
		{Role: "assistant", Content: "<think>saved thought</think>saved answer", PersonaDisplayName: "Agent B"},
		{Role: "user", Content: "with a picture", Attachments: refs},
		{Role: "assistant", Content: "<think></think>"},
		{Role: "tool", Content: "dropped"},
	})
	step("saved session")
	m.setShowThink(true)
	step("saved session thinking")
}

func TestTranscriptMatchesFullRenderWhenPiecesDoNotJoin(t *testing.T) {
	odd := []chatItem{
		{kind: chatItemSys, sys: "\x1b[44mopen background with spaces that wraps " + strings.Repeat("word ", 30)},
		{kind: chatItemSys, sys: "\x1b]8;;https://example.com\x07open link " + strings.Repeat("text ", 30)},
		{kind: chatItemSys, sys: "ends with a carriage return\r"},
		{kind: chatItemSys, sys: "wide 天気天気天気天気"},
		{kind: chatItemAgent, name: "Agent A", spoken: "\x1b[31munterminated red " + strings.Repeat("word ", 30)},
		{kind: chatItemSys, sys: "trailing newline\n"},
		{kind: chatItemSys, sys: "\x1b[1mbold left open"},
	}
	for _, w := range []int{100, 9, 3, 2, 1} {
		for k, item := range odd {
			t.Run(fmt.Sprintf("width %d item %d", w, k), func(t *testing.T) {
				m := layoutTestConversation(t, max(w, 1), 30)
				for i := range 12 {
					m.items = append(m.items, fixtureItem(i))
				}
				m.items = append(m.items, item, fixtureItem(12), item)
				m.refreshContent()
				assertTranscriptMatches(t, m, "odd item")
				m.streamingAgent = "live words " + strings.Repeat("天", 5)
				m.refreshContent()
				assertTranscriptMatches(t, m, "odd item with live bubble")
				m.items = m.items[:12]
				m.refreshContent()
				assertTranscriptMatches(t, m, "odd item removed")
			})
		}
	}
}

func TestOneEventRendersAFixedNumberOfItems(t *testing.T) {
	truecolorTB(t)
	m := longConversation(t, 2000, 120, 40)
	cases := []struct {
		name  string
		msg   tea.Msg
		limit int64
	}{
		{"wheel", tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}, 0},
		{"page up", tea.KeyMsg{Type: tea.KeyPgUp}, 0},
		{"thinking delta", busEventMsg{event: events.ThinkingDelta{Text: "idea "}}, 0},
		{"response delta", busEventMsg{event: events.ResponseDelta{Text: "word "}}, 0},
		{"typed key", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, 0},
		{"finished reply", busEventMsg{event: events.ResponseReady{Response: "A finished reply."}}, 1},
		{"notice", busEventMsg{event: events.Info{Message: "A notice."}}, 1},
		{"left click", tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}, 0},
	}
	for _, c := range cases {
		before := chatItemRenders.Load()
		m, _ = m.Update(c.msg)
		_ = m.View()
		if n := chatItemRenders.Load() - before; n > c.limit {
			t.Fatalf("%s rendered %d of %d items, want at most %d", c.name, n, len(m.items), c.limit)
		}
	}
	assertTranscriptMatches(t, m, "after events")
}
