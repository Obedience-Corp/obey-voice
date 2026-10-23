package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

func (m conversationModel) animStyles() anim.Styles {
	return voiceAnimStyles()
}

func (m conversationModel) voiceLevel() float64 {
	switch m.voiceMode {
	case anim.ModeHearing, anim.ModeListening, anim.ModeTranscribing:
		return m.inputLevel
	case anim.ModeSpeaking, anim.ModeSynthesizing:
		if m.outputLevel > 0 {
			return m.outputLevel
		}
		return 0
	default:
		return 0
	}
}

func (m conversationModel) inputBorderColor() lipgloss.Color {
	switch m.voiceMode {
	case anim.ModeHearing:
		return colorHearing
	case anim.ModeListening:
		return colorAccent
	case anim.ModeSpeaking, anim.ModeSynthesizing:
		return colorSpeak
	case anim.ModeError:
		return colorError
	case anim.ModeThinking, anim.ModeTranscribing:
		return colorStatus
	default:
		return colorAccent
	}
}

// formatTurnMetrics compacts a turn's latency milestones into one dim
// trailing line under the turn (the status bar is overwritten by the next
// listening state almost immediately, so timings live in the transcript).
func formatTurnMetrics(e events.TurnMetrics) string {
	var parts []string
	origin := "since turn start"
	model, ready, complete := e.ModelCompleteElapsed, e.FirstAudioReadyElapsed, e.PlaybackCompleteElapsed
	if e.STTFinalElapsed > 0 {
		origin = "since input"
		model = max(0, model-e.STTFinalElapsed)
		ready = max(0, ready-e.STTFinalElapsed)
	}
	if e.Timing != nil {
		origin = "since turn start"
		if e.Timing.Origin == "finalized_input" {
			origin = "since input"
		}
		duration := func(ms *float64) time.Duration {
			if ms == nil {
				return 0
			}
			return time.Duration(*ms * float64(time.Millisecond))
		}
		model = duration(e.Timing.Normalized.ModelCompleteMS)
		ready = duration(e.Timing.Normalized.FirstAudioReadyMS)
		complete = duration(e.Timing.Normalized.PlaybackCompleteMS)
	}
	if model > 0 {
		parts = append(parts, "model "+formatSeconds(model))
	}
	if ready > 0 {
		parts = append(parts, "first audio "+formatSeconds(ready))
	}
	if complete > 0 && e.AudibleSink != "none" && e.AudibleSink != "stream_client" {
		label := "playback done "
		if e.Interrupted || e.Outcome == "interrupted" || e.Outcome == "failed" || e.Outcome == "timed_out" {
			label = "playback stopped "
		}
		parts = append(parts, label+formatSeconds(complete))
	}
	if len(parts) > 0 {
		parts = append([]string{origin}, parts...)
	}
	if len(parts) > 0 || e.Interrupted {
		switch {
		case e.Interrupted || e.Outcome == "interrupted":
			parts = append(parts, "interrupted")
		case e.Outcome == "failed":
			parts = append(parts, "failed")
		case e.Outcome == "timed_out":
			parts = append(parts, "timed out")
		}
	}
	if e.Degraded {
		parts = append(parts, "degraded")
	}
	return strings.Join(parts, " · ")
}

func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
}

// formatActivityAt renders a session-relative timestamp for the Activity feed
// (ctrl+t). Prefer m:ss.s / h:mm:ss.s over raw "1250.5s" so long sessions stay
// scannable. Fixed-ish width keeps the stage column aligned.
func formatActivityAt(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	// Round to 0.1s so float formatting does not jitter at .999 boundaries.
	totalMs := d.Round(100 * time.Millisecond).Milliseconds()
	if totalMs < 0 {
		totalMs = 0
	}
	tenths := (totalMs / 100) % 10
	totalSec := totalMs / 1000
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	s := totalSec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%d", h, m, s, tenths)
	}
	return fmt.Sprintf("%2d:%02d.%d", m, s, tenths)
}

// renderUserTurn and renderAgentTurn paint finalized turns (ResponseReady and
// session replay). Live streaming uses renderAgentBubble so think and speakable
// stay on separate accumulators.
func renderUserTurn(text string) string {
	body := userStyle.Render("› You") + "\n" + normalStyle.Render(text)
	return userBubbleStyle.Render(body)
}

func renderAgentTurn(name, text string) string {
	think, spoken := textclean.SplitThink(text)
	showThink := think != ""
	// Tag-only leftover on ResponseReady / session replay: both halves empty
	// means the stored text is think markup with no inner body. Paint the dim
	// state rather than the raw tags. Live frames no longer call this function.
	if !showThink && spoken == "" && strings.TrimSpace(text) != "" {
		showThink = true
	}
	return renderAgentBubble(name, think, spoken, showThink)
}

func renderAgentBubble(name, think, spoken string, showThink bool) string {
	var b strings.Builder
	b.WriteString(agentStyle.Render("● " + name))
	if showThink {
		b.WriteByte('\n')
		b.WriteString(thinkLabelStyle.Render("thinking"))
		if think != "" {
			b.WriteByte('\n')
			b.WriteString(thinkBodyStyle.Render(think))
		}
	}
	if spoken != "" {
		b.WriteByte('\n')
		b.WriteString(normalStyle.Render(spoken))
	}
	return agentBubbleStyle.Render(b.String())
}

func (m conversationModel) View() string {
	if m.startup.active {
		return m.startupView()
	}
	if !m.ready {
		return "\n  " + headerStyle.Render("Preparing conversation…") + "\n"
	}

	if m.width < 20 || m.height < 6 {
		return "Resize to 20×6 or larger"
	}
	now := m.workStripRenderAt
	if now.IsZero() {
		now = time.Now()
	}
	styles := m.animStyles()
	w := max(m.width, 1)

	// Clean header: name, tabs, compact EQ chip.
	left := headerStyle.Render(m.agentName)
	if badge := ttsBadgeLabel(m.cfg); badge != "" {
		left += "  " + chipMutedStyle.Render(badge)
	}
	left += "  " + m.renderTabs()
	if m.sessionID != "" {
		left += "  " + dimStyle.Render(shortSessionID(m.sessionID))
	}
	right := ""
	if m.voiceMode != anim.ModeIdle {
		right = anim.CompactMeter(m.voiceMode, m.voiceFrame, m.voiceLevel(), m.status, styles, m.reducedMotion)
	} else if m.status != "" {
		style := statusStyle
		if m.statusErr {
			style = errorStyle
		}
		right = style.Render(m.status)
	}
	headerInner := left
	if right != "" {
		pad := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
		if pad < 1 {
			pad = 1
		}
		headerInner = left + strings.Repeat(" ", pad) + right
	}
	header := ansi.Truncate(headerInner, w, "…")

	rule := lipgloss.NewStyle().Foreground(m.inputBorderColor()).Render(strings.Repeat("─", w))

	voiceStrip := ""
	if m.voiceMode != anim.ModeIdle && m.height >= 14 {
		voiceStrip = anim.Stage(m.voiceMode, m.voiceFrame, m.voiceLevel(), w, m.status, styles, m.reducedMotion)
		if voiceStrip != "" {
			voiceStrip += "\n"
		}
	}

	// Capture stays armed while voice input is paused; wording must not claim
	// the OS microphone was released.
	micChip := chipMutedStyle.Render("mic off")
	if m.voiceOn() {
		micChip = chipStyle.Render("mic on")
		if m.voiceMode == anim.ModeHearing {
			micChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorHearing).Bold(true).Padding(0, 1).Render("recording")
		}
	}
	outChip := chipMutedStyle.Render("audio n/a")
	if m.outputAvailable {
		if m.outputMuted {
			outChip = chipMutedStyle.Render("audio off")
		} else if m.voiceMode == anim.ModeSpeaking {
			outChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorSpeak).Bold(true).Padding(0, 1).Render("speaking")
		} else {
			outChip = lipgloss.NewStyle().Foreground(colorBg).Background(colorAgent).Bold(true).Padding(0, 1).Render("audio on")
		}
	}
	footerLeft := "  " + micChip + " " + outChip
	if m.journalIncomplete {
		footerLeft += " " + chipMutedStyle.Render("Diagnostic history incomplete")
	}
	if hint := m.talkHint(); hint != "" && !m.vim.enabled {
		footerLeft += " " + dimStyle.Render(hint)
	}
	workers := m.jobStatus
	if m.deps.workersSummary != nil {
		workers = m.deps.workersSummary()
	}
	if workers == "" {
		workers = workersCountLabel(0, 0, m.cfg == nil || !m.cfg.VoiceWorkersDisabled)
	}
	footerLeft += " " + dimStyle.Render(workerFooterLabel(workers, max(0, w-lipgloss.Width(footerLeft)-1)))
	if m.showThink {
		footerLeft += " " + chipStyle.Render("think")
	}
	switch m.recordMode() {
	case "talk":
		footerLeft += " " + chipStyle.Render("rec")
	case "lab":
		footerLeft += " " + chipStyle.Render("lab")
	}
	if m.liveSpeakerStatsKnown {
		footerLeft += " " + renderLiveSpeakerFooterNamed(m.liveSpeakerStats, m.displaySpeakerLabel(m.liveSpeakerStats.LastLabel))
	}
	activeViewport := m.activeViewport()
	if activeViewport.TotalLineCount() > activeViewport.VisibleLineCount() {
		footerLeft += " " + chipMutedStyle.Render(fmt.Sprintf("%d%%", int(activeViewport.ScrollPercent()*100)))
	}
	footerHelp := m.vimFooterHelp()
	if !m.vim.enabled {
		footerHelp = strings.ReplaceAll(footerHelp, "/menu back  ", "")
	}
	footerText := footerLeft
	switch {
	case m.vim.enabled:
		// Modal controls are the primary interaction contract. Keep them visible
		// at medium widths, then include device state when the terminal has room.
		compactHelp := "  " + m.vimCompactFooterHelp()
		footerText = compactHelp
		if m.width >= lipgloss.Width(compactHelp)+lipgloss.Width(footerLeft)+4 {
			footerText += strings.Repeat(" ", m.width-lipgloss.Width(compactHelp)-lipgloss.Width(footerLeft)) + footerLeft
		}
	case m.width >= lipgloss.Width(footerLeft)+lipgloss.Width(footerHelp)+4:
		footerText += strings.Repeat(" ", m.width-lipgloss.Width(footerLeft)-lipgloss.Width(footerHelp)) + dimStyle.Render(footerHelp)
	case m.width >= 60:
		compact := "  ·  ^G mic  ^O audio  ^T switch  ^K think  ^R rec"
		footerText += dimStyle.Render(compact)
	default:
		footerText += dimStyle.Render("  ^G  ^O  ^T  ^K")
	}
	footer := ansi.Truncate(footerText, w, "…")

	content := m.viewport.View()
	if m.activityFocused {
		content = m.activityViewport.View()
	}

	inputLabel := "Your message"
	switch {
	case m.voiceMode == anim.ModeHearing:
		inputLabel = hearingStyle.Render("● Hearing you — type to interrupt")
	case m.voiceMode == anim.ModeListening:
		inputLabel = headerStyle.Render("◎ Listening — type anytime")
	case m.voiceMode == anim.ModeSpeaking || m.voiceMode == anim.ModeSynthesizing:
		inputLabel = speakStyle.Render("◉ Speaking — type to barge in")
	case m.turnState == turnVoiceListening:
		inputLabel = headerStyle.Render("🎙 Listening — type to interrupt")
	case m.turnState == turnVoiceResponding || m.turnState == turnTextRunning:
		inputLabel = thinkStyle.Render("✦ Responding — type to barge in")
	case m.turnState == turnVoiceCanceling:
		inputLabel = thinkStyle.Render("✦ Interrupting…")
	default:
		inputLabel = dimStyle.Render(inputLabel)
	}
	inputLabel = m.vimInputLabel(inputLabel)
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.inputBorderColor()).
		Padding(0, 1).
		Render(m.input.View())

	palette := m.renderCommandPalette()
	if palette != "" {
		content += "\n" + palette
	}

	return header + "\n" + rule + "\n" +
		voiceStrip +
		content + "\n" +
		m.renderWorkStrip(now) +
		ansi.Truncate(inputLabel, w, "…") + "\n" +
		inputBox + "\n" +
		footer
}

func (m conversationModel) renderTabs() string {
	inactive := lipgloss.NewStyle().Foreground(colorDim).Padding(0, 1)
	active := lipgloss.NewStyle().Foreground(colorBg).Background(colorSelect).Bold(true).Padding(0, 1)
	chat := inactive.Render("Chat")
	activity := inactive.Render("Activity")
	if m.activityFocused {
		activity = active.Render("Activity")
	} else {
		chat = active.Render("Chat")
	}
	return chat + " " + activity
}

func shortSessionID(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:18]
}

func truncateJobLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 48 {
		return s
	}
	return s[:45] + "..."
}
