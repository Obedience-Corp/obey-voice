package tui

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
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
	inputTyped  string
	input       *utterance.Finalized
	attachments []chatmedia.Ref
	kind        chatItemKind
	name        string
	// hosted marks a reply from a persona whose backend sent the conversation
	// off this machine.
	hosted bool
	think  string
	// thinkLabel paints the dim "thinking" marker when the stored text is
	// tag-only markup with no inner body (session replay leftovers).
	thinkLabel bool
	spoken     string
	sys        string
	// hang is a notice's plain text, wrapped at render time so every line
	// keeps the two-column indent; sys holds the unwrapped fallback. render
	// prefers hang, so an edit to sys alone never shows on a notice item.
	hang      string
	hangStyle lipgloss.Style
	// notice preserves unstyled local command output for native clients.
	notice string
	// workResultID marks a canonical background result projection. These
	// entries can be cleared when the work scope changes or the result is
	// dismissed without touching ordinary conversation history.
	workResultID string
}

// render paints the item for a viewport width columns wide.
func (it chatItem) render(showThink bool, width int) string {
	if it.kind != chatItemAgent {
		if it.hang != "" && width >= noticeIndent+2 {
			return renderHanging(it.hangStyle, it.hang, width)
		}
		return it.sys
	}
	show := showThink && (it.think != "" || it.thinkLabel)
	if it.spoken == "" && !show {
		return ""
	}
	name := it.name
	if it.hosted {
		name += " [hosted]"
	}
	return renderAgentBubble(name, it.think, it.spoken, show)
}

// noticeIndent is the left margin of transcript notices; a hanging notice
// also needs two columns so a double-width character fits.
const noticeIndent = 2

// noticeTabWidth is the tab stop notices expand to before wrapping.
const noticeTabWidth = 8

// renderHanging wraps text to fit width with every line, not just the first,
// indented by noticeIndent.
func renderHanging(style lipgloss.Style, text string, width int) string {
	pad := strings.Repeat(" ", noticeIndent)
	lines := strings.Split(ansi.Wrap(text, width-noticeIndent, ""), "\n")
	for i, line := range lines {
		lines[i] = style.Render(pad + line)
	}
	return strings.Join(lines, "\n")
}

// noticeText is a notice as plain text with its tabs expanded. Event
// messages are plain text; terminal sequences inside one (from a CLI's
// stderr, say) would not survive the wrap, so they are dropped.
func noticeText(text string) string {
	text = ansi.Strip(text)
	if !strings.Contains(text, "\t") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		var b strings.Builder
		for _, r := range line {
			if r == '\t' {
				b.WriteString(strings.Repeat(" ", noticeTabWidth-ansi.StringWidth(b.String())%noticeTabWidth))
				continue
			}
			b.WriteRune(r)
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// appendNotice adds a notice line that keeps its indent when it wraps.
func (m *conversationModel) appendNotice(style lipgloss.Style, text string) {
	text = noticeText(text)
	m.items = append(m.items, chatItem{kind: chatItemSys, sys: style.Render(strings.Repeat(" ", noticeIndent) + text), hang: text, hangStyle: style})
	m.refreshContent()
	m.applyFollow()
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
		if reason := m.noThinkingReason(); reason != "" {
			m.commandNotice(reason)
		}
		return
	}
	m.appendActivity("view", "thinking off", 0)
}

// noThinkingReason explains a thinking toggle that reveals nothing, so the key
// does not look dead. Empty when the thread already holds thinking to show.
func (m *conversationModel) noThinkingReason() string {
	if strings.TrimSpace(m.streamingThink) != "" {
		return ""
	}
	for _, item := range m.items {
		if item.kind == chatItemAgent && (item.think != "" || item.thinkLabel) {
			return ""
		}
	}
	provider := strings.TrimSpace(m.selection.BrainProvider)
	if provider == "" && m.cfg != nil {
		provider = strings.TrimSpace(m.cfg.BrainProvider)
	}
	if m.cfg != nil && (provider == "ollama" || provider == "") {
		switch level := strings.ToLower(strings.TrimSpace(m.cfg.OllamaThink)); level {
		case "", "auto", "false", "off", "0", "no":
			if level == "" {
				level = "auto"
			}
			return "No thinking to show: the model is not asked to think (Thinking is " + level + " in Settings → Brain)."
		}
	}
	return "No thinking to show: no reply in this chat has included any yet."
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
