package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// appendTranscript adds rendered lines to the transcript, following the tail
// when followChat is set. Sticky flags (not live AtBottom) own follow intent
// so a prior reflow cannot freeze the chat mid-session.
func (m *conversationModel) appendTranscript(lines ...string) {
	for _, line := range lines {
		m.items = append(m.items, chatItem{kind: chatItemSys, sys: line})
	}
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) clearTranscript() {
	m.transcript = nil
	m.items = nil
	m.jobItems = nil
	m.completedJobItems = nil
	m.workerRepliesSeen = nil
	m.workResultItems = nil
	m.workResultJobs = nil
	m.workScope = work.WorkScope{}
	m.workRevision = 0
	m.pendingUserEcho = ""
	m.lastAssistantText = ""
	m.plainTurns = nil
	m.streamingThink = ""
	m.streamingAgent = ""
	m.liveThink = textclean.ThinkFilter{}
	m.refreshContent()
	if m.followChat {
		m.viewport.GotoBottom()
	}
}

// rememberAssistant keeps the latest plain agent reply for clipboard yank.
func (m *conversationModel) rememberAssistant(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.lastAssistantText = text
	m.plainTurns = append(m.plainTurns, plainChatTurn{role: "assistant", text: text})
}

// rememberUser keeps a plain user turn for /copy all. speaker is an optional
// display label (renamed speaker-N); empty falls back to "You:" when formatting.
func (m *conversationModel) rememberUser(text string, speaker ...string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	label := ""
	if len(speaker) > 0 {
		label = strings.TrimSpace(speaker[0])
	}
	m.plainTurns = append(m.plainTurns, plainChatTurn{role: "user", text: text, speaker: label})
}

// copyLastAssistant writes the latest agent reply to the system clipboard.
// When fromIdleCtrlC is true, arm a second Ctrl+C within idleCopyQuitWindow as quit.
func (m *conversationModel) copyLastAssistant(fromIdleCtrlC ...bool) {
	text := m.latestReadableWorkResult()
	if text == "" {
		text = strings.TrimSpace(m.lastAssistantText)
	}
	if text == "" {
		text = strings.TrimSpace(m.streamingAgent)
	}
	if text == "" {
		m.commandError("nothing to copy yet")
		return
	}
	if err := m.clipboard().WriteAll(text); err != nil {
		m.commandError("copy failed: " + err.Error())
		return
	}
	if len(fromIdleCtrlC) > 0 && fromIdleCtrlC[0] {
		m.lastIdleCopyAt = time.Now()
		m.commandNotice(fmt.Sprintf("Copied last reply (%d characters) · Ctrl+C again to quit · /quit also exits", runeLen(text)))
		return
	}
	m.lastIdleCopyAt = time.Time{}
	m.commandNotice(fmt.Sprintf("Copied last reply (%d characters)", runeLen(text)))
}

// idleCopyQuitWindow is how long a second idle Ctrl+C counts as quit after yank.
const idleCopyQuitWindow = 1500 * time.Millisecond

// copyPlainChat writes the full plain-text conversation to the clipboard.
func (m *conversationModel) copyPlainChat() {
	if len(m.plainTurns) == 0 && len(m.workResultItems) == 0 {
		// Fall back to last reply alone when history was not tracked (edge).
		m.copyLastAssistant()
		return
	}
	var b strings.Builder
	for i, turn := range m.plainTurns {
		if i > 0 {
			b.WriteString("\n\n")
		}
		switch turn.role {
		case "assistant":
			name := m.agentName
			if name == "" {
				name = "Assistant"
			}
			b.WriteString(name)
			b.WriteString(": ")
		default:
			if turn.speaker != "" {
				b.WriteString(turn.speaker)
				b.WriteString(": ")
			} else {
				b.WriteString("You: ")
			}
		}
		b.WriteString(turn.text)
	}
	for _, text := range m.readableWorkResults() {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	text := b.String()
	if err := m.clipboard().WriteAll(text); err != nil {
		m.commandError("copy failed: " + err.Error())
		return
	}
	m.commandNotice(fmt.Sprintf("Copied conversation (%d characters)", runeLen(text)))
}

// composerIdle reports an empty draft with no active selection — used by
// idle yank bindings so they never steal text the user is still editing.
func (m conversationModel) composerIdle() bool {
	return strings.TrimSpace(m.input.Value()) == "" && !m.editor.selectionActive()
}

// echoUserTurn renders the user's message into Chat immediately. Typed
// submits call this so clearing the composer never looks like the message
// vanished while a voice turn cancels or the brain is reached. Matching
// UserInput events skip a second bubble via pendingUserEcho.
func (m *conversationModel) echoUserTurn(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	m.activityFocused = false
	m.followChat = true
	m.pendingUserEcho = text
	m.rememberUser(text)
	m.appendTranscript(renderUserTurn(text))
}

func (m *conversationModel) refreshContent() {
	if !m.ready {
		return
	}
	parts := make([]string, 0, len(m.items))
	for _, it := range m.items {
		if s := it.render(m.showThink); s != "" {
			parts = append(parts, s)
		}
	}
	m.transcript = parts
	content := strings.Join(parts, "\n")
	// Render the in-progress turn live beneath the finalized transcript so
	// thinking and speakable stream in before ResponseReady lands.
	if m.streamingThink != "" || m.streamingAgent != "" {
		live := renderAgentBubble(m.agentName, m.streamingThink, m.streamingAgent, m.streamingThink != "")
		if content != "" {
			content += "\n" + live
		} else {
			content = live
		}
	}
	// lipgloss wraps to width so long turns don't overflow the viewport.
	m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(content))
}

// appendStreamingDelta grows the in-progress agent turn and re-renders,
// following the tail when followChat is set.
func (m *conversationModel) appendStreamingDelta(text string) {
	m.streamingAgent += text
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) appendStreamingThink(text string) {
	first := m.streamingThink == ""
	m.streamingThink += text
	if first {
		m.appendActivity("model", "thinking", 0)
	}
	m.refreshContent()
	m.applyFollow()
}

func (m *conversationModel) appendActivity(stage, detail string, elapsed time.Duration) {
	m.activity = append(m.activity, activityEntry{
		at: time.Since(m.startedAt), stage: stage, detail: detail, elapsed: elapsed,
	})
	if len(m.activity) > 500 {
		m.activity = append([]activityEntry(nil), m.activity[len(m.activity)-500:]...)
	}
	m.refreshActivity()
	m.applyFollow()
}

func (m *conversationModel) refreshActivity() {
	if !m.ready {
		return
	}
	if m.workActivity {
		m.activityViewport.SetContent(lipgloss.NewStyle().Width(max(m.activityViewport.Width, 1)).Render(m.renderWorkActivity()))
		return
	}
	var lines []string
	for _, entry := range m.activity {
		// Session offset as a clock (20:50.5), not raw seconds (1250.5s) —
		// long conversations make absolute seconds unreadable at a glance.
		when := formatActivityAt(entry.at)
		line := when + "  " + entry.stage
		if entry.detail != "" {
			line += "  " + entry.detail
		}
		if entry.elapsed > 0 {
			line += "  " + formatSeconds(entry.elapsed)
		}
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n")
	m.activityViewport.SetContent(lipgloss.NewStyle().Width(max(m.activityViewport.Width, 1)).Render(content))
}

// seedTranscript pre-populates the viewport from persisted session turns.
// Roles are the normalized on-disk scheme ("user"/"assistant"); anything
// else (e.g. "tool") is dropped, matching brain.LoadHistory. Rendering goes
// through the same functions live events use so the two paths cannot drift.
func (m *conversationModel) seedTranscript(turns []brain.Turn) {
	for _, t := range turns {
		switch t.Role {
		case "user":
			m.rememberUser(t.Content, m.displaySpeakerLabel(t.Speaker))
			m.appendTranscript(renderSpeakerUserTurn(m.displaySpeakerLabel(t.Speaker), t.Content))
		case "assistant":
			item := agentItemFromStored(m.agentName, t.Content)
			if item.spoken != "" {
				m.rememberAssistant(item.spoken)
			}
			m.appendAgentTurn(item)
		}
	}
}
