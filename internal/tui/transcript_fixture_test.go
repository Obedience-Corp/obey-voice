package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

func truecolorTB(tb testing.TB) {
	tb.Helper()
	previous := lipgloss.ColorProfile()
	tb.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// fixtureItem is transcript item i of a synthetic long session: user turns,
// agent replies with and without thinking, wrapped notices, styled tool
// lines, wide characters and the odd picture.
func fixtureItem(i int) chatItem {
	switch i % 8 {
	case 0:
		return chatItem{kind: chatItemSys, sys: renderSpeakerUserTurn("Speaker 1", fmt.Sprintf("Question %d: can you compare the two plans and tell me which one is cheaper over a year? 天気 も 確認して 🙂", i))}
	case 1:
		return chatItem{kind: chatItemAgent, name: "Agent A", think: fmt.Sprintf("Turn %d. The user wants a comparison.\nPlan one costs more up front.\n\tPlan two has a monthly fee.", i), spoken: fmt.Sprintf("Plan two is cheaper over a year by about %d units. @Agent B can double-check the monthly fee. 👍🏽 Ünïcödé text keeps going so the line has to wrap at the viewport edge more than once.", i)}
	case 2:
		return chatItem{kind: chatItemAgent, name: "Agent B", hosted: i%16 == 2, spoken: fmt.Sprintf("Reply %d.\n\nFirst paragraph with a long sentence that runs past the width of the viewport and must wrap onto a second line.\n- item one\n- item two 中文字符混合 English", i)}
	case 3:
		text := noticeText(fmt.Sprintf("Info %d: worker finished\tstep one\tstep two with a long tail of words that wraps and keeps its hanging indent across lines", i))
		return chatItem{kind: chatItemSys, sys: dimStyle.Render("  " + text), hang: text, hangStyle: dimStyle}
	case 4:
		return chatItem{kind: chatItemSys, sys: dimStyle.Render("  ") + errorStyle.Render("✗") + dimStyle.Render(fmt.Sprintf(" tool search failed → %s: exit status 1 (exit code 1) in 1.2s\n    stderr: %s", strings.Repeat("path/to/some/file.go:12 ", 6), strings.Repeat("ε", 30)))}
	case 5:
		return chatItem{kind: chatItemAgent, name: "Agent A", thinkLabel: true}
	case 6:
		return chatItem{kind: chatItemSys, sys: dimStyle.Render(fmt.Sprintf("    turn %d · ttft 0.4s · total 2.1s · %s", i, strings.Repeat("metric ", 30)))}
	default:
		item := chatItem{kind: chatItemAgent, name: "Agent A", spoken: fmt.Sprintf("Short reply %d.", i)}
		if i%40 == 7 {
			item.attachments = []chatmedia.Ref{{ID: fmt.Sprintf("img-%d", i), Name: "chart.png", Format: "png", Width: 640, Height: 480, Frames: 1}}
		}
		return item
	}
}

// longConversation is a ready conversation holding n synthetic items, three
// of them background results, scrolled to the top so the results below are
// unseen.
func longConversation(tb testing.TB, n, width, height int) conversationModel {
	tb.Helper()
	m := newConversation("Agent A")
	m, _ = m.Update(teaWindowSize(width, height))
	if !m.ready {
		tb.Fatal("model not ready")
	}
	for i := range n / 2 {
		m.items = append(m.items, fixtureItem(i))
	}
	m.handleEvent(events.WorkStateChanged{Snapshot: fixtureWorkSnapshot(3)})
	for i := n / 2; len(m.items) < n; i++ {
		m.items = append(m.items, fixtureItem(i))
	}
	m.refreshContent()
	m.viewport.GotoTop()
	m.followChat = false
	m.workDisplay.seen = map[string]bool{}
	return m
}

func fixtureWorkSnapshot(results int) work.Snapshot {
	scope := work.WorkScope{SessionID: "s", RuntimeEpoch: "r", ConversationEpoch: "c"}
	snap := work.Snapshot{Scope: scope, Revision: 1}
	for i := range results {
		out := work.Outcome{Scope: scope, JobID: fmt.Sprintf("job-%d", i), ResultID: fmt.Sprintf("result-%d", i), Status: work.OutcomeSucceeded, Text: fmt.Sprintf("Fixture result %d with enough words to wrap across the viewport at narrow widths", i), Config: map[string]string{"persona": "Agent A", "worker_tier": "simple"}}
		snap.Pending = append(snap.Pending, work.ResultView{Outcome: out, Task: fmt.Sprintf("Task %d", i), State: work.Held, HoldReason: "muted", DeliveryKey: work.JobKey{Scope: scope, JobID: out.JobID}})
	}
	return snap
}
