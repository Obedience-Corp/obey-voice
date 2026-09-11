package tui

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// chatItemKind distinguishes opaque transcript lines from agent turns whose
// thinking can be shown or hidden after the fact.
type chatItemKind uint8

const (
	chatItemSys chatItemKind = iota
	chatItemAgent
)

// chatItem is the source of truth for the conversation viewport. System and
// user lines are stored already rendered; agent turns keep think and spoken
// text apart so Ctrl+K / /think can rebuild the bubble without touching
// clipboard yank or model history.
type chatItem struct {
	kind  chatItemKind
	name  string
	think string
	// thinkLabel paints the dim "thinking" marker when the stored text is
	// tag-only markup with no inner body (session replay leftovers).
	thinkLabel bool
	spoken     string
	sys        string
	// workResultID marks a canonical background result projection. These
	// entries can be cleared when the work scope changes or the result is
	// dismissed without touching ordinary conversation history.
	workResultID string
}

func (it chatItem) render(showThink bool) string {
	if it.kind != chatItemAgent {
		return it.sys
	}
	show := showThink && (it.think != "" || it.thinkLabel)
	if it.spoken == "" && !show {
		return ""
	}
	return renderAgentBubble(it.name, it.think, it.spoken, show)
}

func (m *conversationModel) appendAgentTurn(item chatItem) {
	item.kind = chatItemAgent
	m.items = append(m.items, item)
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) setShowThink(on bool) {
	if m.showThink == on {
		return
	}
	m.showThink = on
	m.activityFocused = false
	m.refreshContent()
	m.applyFollow()
	if on {
		m.appendActivity("view", "thinking on", 0)
		return
	}
	m.appendActivity("view", "thinking off", 0)
}

func (m *conversationModel) toggleShowThink() {
	m.setShowThink(!m.showThink)
}

func (m *conversationModel) configureThink(args []string) {
	if len(args) > 1 {
		m.commandError("usage: /think [on|off]")
		return
	}
	action := "toggle"
	if len(args) == 1 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "toggle":
		m.toggleShowThink()
	case "on", "show":
		m.setShowThink(true)
	case "off", "hide":
		m.setShowThink(false)
	default:
		m.commandError("usage: /think [on|off]")
	}
}

func agentItemFromStored(name, text string) chatItem {
	think, spoken := textclean.SplitThink(text)
	item := chatItem{name: name, think: think, spoken: spoken}
	if think == "" && spoken == "" && strings.TrimSpace(text) != "" {
		item.thinkLabel = true
	}
	return item
}

// ingestSpeakableDelta pulls <think> regions off a ResponseDelta so leaked
// markup becomes the thinking block Ctrl+K reveals, not spoken transcript.
func (m *conversationModel) ingestSpeakableDelta(text string) {
	speakable, think := m.liveThink.FeedSplit(text)
	if think != "" {
		m.appendStreamingThink(think)
	}
	if speakable != "" {
		m.appendStreamingDelta(speakable)
	}
}

// takeStreamingTurn drains live think/speakable buffers and recovers think
// text that arrived as tagged content instead of ThinkingDelta.
func (m *conversationModel) takeStreamingTurn() (thought, spoken string) {
	heldSpeak, heldThink := m.liveThink.FlushSplit()
	m.liveThink = textclean.ThinkFilter{}
	if heldThink != "" {
		m.streamingThink += heldThink
	}
	if heldSpeak != "" {
		m.streamingAgent += heldSpeak
	}
	thought = strings.TrimSpace(m.streamingThink)
	spoken = strings.TrimSpace(m.streamingAgent)
	m.streamingThink = ""
	m.streamingAgent = ""
	if thought == "" {
		if t, b := textclean.SplitThink(spoken); t != "" {
			return t, b
		}
	} else if t, b := textclean.SplitThink(spoken); t != "" {
		spoken = b
		if !strings.Contains(thought, t) {
			thought = strings.TrimSpace(thought + "\n" + t)
		}
	}
	return thought, spoken
}
