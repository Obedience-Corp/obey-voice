package tui

import (
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/internal/tui/anim"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

func (m *conversationModel) setStatus(text string, isErr bool) {
	previousRows := m.voicePanelHeight()
	m.status = text
	m.statusErr = isErr
	if m.ready && previousRows != m.voicePanelHeight() {
		m.reflow()
	}
}

// setVoiceMode updates the animated meter state and reflows when the panel
// changes height so the viewport stays within the terminal.
func (m *conversationModel) setVoiceMode(mode anim.Mode) {
	prev := m.voiceMode
	m.voiceMode = mode
	if mode == anim.ModeIdle {
		m.inputLevel = 0
	}
	if prev != mode && m.ready {
		m.reflow()
	}
}

// handleEvent maps one bus event onto viewport/status state. The mapping
// mirrors internal/ui's stdout renderer, minus the per-stage timing noise.
func (m *conversationModel) handleEvent(e events.Event) {
	// A final transcript means the brain now owns this turn: a text submit
	// past this point would kill a response in flight, so Enter waits.
	if _, ok := e.(events.UserInput); ok && m.turnState == turnVoiceListening {
		m.turnState = turnVoiceResponding
	}

	switch e := e.(type) {
	case events.RouteDecided:
		m.routeDecided(e)
	case events.PersonaSelection:
		m.applySelectionSnapshot(e)
	case events.AudioLevel:
		// Peak-hold + light smoothing so quiet frames don't flatten the meter.
		level := e.Level
		if level < 0 {
			level = 0
		}
		if level > 1 {
			level = 1
		}
		switch e.Source {
		case "output":
			if level > m.outputLevel {
				m.outputLevel = level
			} else {
				m.outputLevel = m.outputLevel*0.6 + level*0.4
			}
		default:
			if level > m.inputLevel {
				m.inputLevel = level
			} else {
				m.inputLevel = m.inputLevel*0.55 + level*0.45
			}
			// Meter only. Hearing you is Silero (STTPhase), not RMS — a fan
			// must not look like a turn that will never end.
		}

	case events.STTPhase:
		// Skip consecutive "listening" ticks from the no-speech restart loop.
		if e.Phase == "listening" && len(m.activity) > 0 {
			last := m.activity[len(m.activity)-1]
			if last.stage == "input" && last.detail == "listening" {
				m.setVoiceMode(anim.ModeListening)
				m.setStatus("Listening", false)
				break
			}
		}
		m.appendActivity("input", e.Phase, e.Elapsed)
		switch e.Phase {
		case "listening":
			m.setVoiceMode(anim.ModeListening)
			m.setStatus("Listening", false)
		case "hearing":
			m.setVoiceMode(anim.ModeHearing)
			m.setStatus("Hearing you", false)
		case "transcribing":
			m.setVoiceMode(anim.ModeTranscribing)
			m.setStatus("Transcribing", false)
		}

	case events.TranscriptPartial:
		if m.voiceMode != anim.ModeHearing && m.voiceMode != anim.ModeTranscribing {
			m.setVoiceMode(anim.ModeHearing)
		}
		m.setStatus(e.Text, false)

	case events.UserInput:
		m.appendActivity("input", "final", 0)
		// Typed submits already echoed the bubble; voice transcripts still need
		// one. Dedupe on exact text so a delayed cancel path cannot double-post.
		if m.pendingUserEcho != "" && m.pendingUserEcho == e.Text {
			m.pendingUserEcho = ""
			break
		}
		m.pendingUserEcho = ""
		m.activityFocused = false
		m.followChat = true
		label := e.Speaker
		if label == "" {
			label = m.currentLiveSpeakerLabel()
		}
		m.rememberUser(e.Text, m.displaySpeakerLabel(label))
		m.appendTranscript(renderSpeakerUserTurn(m.displaySpeakerLabel(label), e.Text))

	case events.ThinkingStarted:
		// Start a fresh streaming buffer; a prior turn's leftover (e.g. after an
		// interrupt that skipped ResponseReady) must not bleed into this reply.
		// Re-render so any stale live partial leaves the viewport immediately.
		m.liveThink = textclean.ThinkFilter{}
		if m.streamingAgent != "" || m.streamingThink != "" {
			m.streamingAgent = ""
			m.streamingThink = ""
			m.refreshContent()
			m.applyFollow()
		}
		m.appendActivity("model", "started", 0)
		m.setVoiceMode(anim.ModeThinking)
		m.setStatus(m.agentName+" thinking", false)

	case events.ResponseStreamingStarted:
		m.appendActivity("model", "first token", e.Elapsed)

	case events.ResponseSpeakableStarted:
		m.appendActivity("model", "first speakable", e.Elapsed)

	case events.ThinkingDelta:
		if e.Text == "" {
			break
		}
		m.appendStreamingThink(e.Text)

	case events.ResponseDelta:
		m.ingestSpeakableDelta(e.Text)

	case events.ThinkingComplete:
		m.appendActivity("model", "complete", e.Elapsed)

	case events.TokenUsage:
		m.appendActivity("model", fmt.Sprintf("prefill %d tok · gen %d tok", e.Prefill, e.Gen), 0)

	case events.SessionWarning:
		m.appendActivity("model", fmt.Sprintf("session prompt %d tok ≥ warn %d — consider clearing the conversation", e.PromptTokens, e.Threshold), 0)

	case events.SpeechSegmentReady:
		if e.Elapsed > 0 {
			m.appendActivity("voice", "first admit", e.Elapsed)
		} else {
			m.appendActivity("voice", "segment ready", 0)
		}

	case events.GeneratingVoice:
		m.appendActivity("voice", "synthesizing", 0)
		m.setVoiceMode(anim.ModeSynthesizing)
		// An error status outranks any remaining voice-progress events.
		if !m.statusErr {
			m.setStatus("Synthesizing voice", false)
		}

	case events.VoiceGenerated:
		m.appendActivity("voice", "synth first frame", e.Elapsed)

	case events.SpeakingStarted:
		label := "playing"
		if e.AudibleSink == "none" {
			label = "synth"
		} else if e.Elapsed > 0 && e.AudibleSink == "stream_client" {
			label = "audible_tx"
		} else if e.Elapsed > 0 {
			label = "audible"
		}
		m.appendActivity("output", label, e.Elapsed)
		m.setVoiceMode(anim.ModeSpeaking)
		if !m.statusErr {
			m.setStatus("Speaking", false)
		}

	case events.SpeakingComplete:
		label := "complete"
		if e.Interrupted {
			label = "interrupted"
		} else if e.AudibleSink == "none" {
			label = "synth drain (no sink)"
		} else if e.AudibleSink == "stream_client" {
			label = "audible_tx done"
		}
		m.appendActivity("output", label, e.Elapsed)
		// A partial reply may finish playing after an error; the error status
		// must survive that playback finishing. Still leave
		// ModeSpeaking so the EQ/label does not stay "Speaking" forever.
		if m.statusErr {
			m.setVoiceMode(anim.ModeError)
		} else {
			m.setVoiceMode(anim.ModeIdle)
			m.setStatus("", false)
		}

	case events.SpeakingInterrupted:
		m.appendActivity("output", "interrupted: "+e.Reason, 0)
		m.setVoiceMode(anim.ModeIdle)
		m.setStatus("speech interrupted ("+e.Reason+")", false)

	case events.TurnInterrupted:
		m.appendActivity("turn", "interrupted: "+e.Reason, 0)
		m.setVoiceMode(anim.ModeIdle)
		m.setStatus("turn interrupted ("+e.Reason+")", false)

	case events.ResponseReady:
		m.appendActivity("turn", "response ready", 0)
		// Text-only / no-TTS turns never emit SpeakingComplete; clear the
		// thinking status here so it does not stick after a successful reply.
		// An error status is exempt: recoverTurn emits Error then ResponseReady
		// back-to-back, and clearing here erased the error one frame after it
		// rendered. Errors persist until the next turn's activity writes over
		// them.
		if m.voiceMode != anim.ModeSpeaking && m.voiceMode != anim.ModeSynthesizing && !m.statusErr {
			m.setVoiceMode(anim.ModeIdle)
			m.setStatus("", false)
		}
		// Finalize the streamed turn: clear the live buffers first so they are
		// not rendered twice. Thinking is stored on the agent item but hidden
		// until showThink; the spoken bubble comes from e.Response when present.
		thought, liveSpoken := m.takeStreamingTurn()
		spoken := strings.TrimSpace(e.Response)
		if spoken == "" {
			spoken = liveSpoken
		} else if t, b := textclean.SplitThink(spoken); t != "" {
			if thought == "" {
				thought = t
			}
			spoken = b
		}
		name := m.agentName
		if e.Envelope != nil && e.Envelope.DisplayName != "" {
			name = e.Envelope.DisplayName
		}
		if spoken != "" {
			m.rememberAssistant(spoken)
			m.appendAgentTurn(chatItem{name: name, think: thought, spoken: spoken})
		} else if e.Interrupted || e.Degraded {
			m.refreshContent()
			m.applyFollow()
		} else if thought != "" {
			// Thinking-only empty: keep the thought for Ctrl+K; stay off the
			// thread while showThink is false so we do not paint a blank bubble.
			m.appendActivity("model", fmt.Sprintf("thought %d chars", len(thought)), 0)
			m.appendAgentTurn(chatItem{name: name, think: thought})
		} else {
			// Tool-only or empty model finishes must still leave a visible
			// trail so "looking into it" never ends in total silence.
			m.appendTranscript(dimStyle.Render("  (no reply — model finished without text)"))
		}

	case events.ConversationCleared:
		m.stopCapture()
		m.retireAdmissionRetryNotices()
		m.workActivity = false
		m.workFilter = ""
		m.clearTranscript()
		m.followChat = true
		m.appendTranscript(dimStyle.Render("  Conversation cleared."))

	case events.WorkStateChanged:
		m.workStateChanged(e)

	case events.ConversationCompacted:
		// Annotate, don't clear: the scrollback stays for the user, but the
		// model now continues from the summary instead of the full history.
		m.appendActivity("model", fmt.Sprintf("compacted %d turns into a summary", e.TurnsBefore), 0)
		m.appendTranscript(dimStyle.Render(fmt.Sprintf("  Conversation compacted (%d turns summarized — the model continues from the summary).", e.TurnsBefore)))

	case events.TurnMetrics:
		m.lastMetrics = e
		// Terminal event with the live stream buffer still set means the turn
		// ended on a path that skipped ResponseReady (cancel/failure). Fold the
		// partial into the transcript instead of leaving it pinned beneath it,
		// where it reads as a duplicated fragment of the reply (WI-dc9e33 B1).
		// Clear the live buffer *before* appendTranscript: that helper refreshes
		// the viewport immediately, and leaving streamingAgent set would render
		// the same text twice (transcript bubble + live buffer) with no later
		// refresh on a bare interrupted metrics event.
		thought, partial := m.takeStreamingTurn()
		if partial != "" {
			m.rememberAssistant(partial)
			m.appendAgentTurn(chatItem{name: m.agentName, think: thought, spoken: partial})
		} else if thought != "" {
			m.appendAgentTurn(chatItem{name: m.agentName, think: thought})
		}
		// Idle no-speech timeouts restart listening every ~listen_timeout
		// seconds. Logging each one floods Activity without helping the user.
		if e.Outcome == "timed_out" {
			break
		}
		outcome := e.Outcome
		if e.Degraded {
			outcome += " (degraded)"
		}
		m.appendActivity("turn", outcome, e.PlaybackCompleteElapsed)
		if line := formatTurnMetrics(e); line != "" {
			m.appendTranscript(dimStyle.Render("    " + line))
		}

	case events.Error:
		// The Activity pane is the durable surface — it must carry the real
		// message, not just the stage name. The status line clears on the next
		// event; an opaque "error brain" entry is how degraded qwen turns went
		// undiagnosable for weeks (WI-dc9e33 B1).
		detail := strings.TrimSpace(strings.TrimPrefix(e.Stage+": "+e.Message, ": "))
		m.appendActivity("error", detail, 0)
		msg := e.Message
		if e.Stage != "" {
			msg = "[" + e.Stage + "] " + e.Message
		}
		notice := "Error: " + msg
		m.appendTranscript(errorStyle.Render("  " + notice))
		m.items[len(m.items)-1].notice = notice
		m.setVoiceMode(anim.ModeError)
		m.setStatus(notice, true)

	case events.Info:
		m.appendActivity("info", e.Message, 0)
		m.appendTranscript(dimStyle.Render("  " + e.Message))

	case events.SpeechDiagnostic:
		// Diagnostics are routine Activity records. Only an applicable action
		// is rendered into the transcript, and it is emitted once per actual
		// provider configuration by the pipeline's bounded advice window.
		m.appendActivity("voice", e.Message, 0)
		if e.ActionCode != "" && e.Message != "" {
			m.appendTranscript(dimStyle.Render("  " + e.Message))
		}

	case events.VoiceGateStripped:
		// Voice-only filter: the raw text is already in the transcript above;
		// this entry records that it was not spoken.
		m.appendActivity("voice", fmt.Sprintf("gate stripped %d tool-syntax line(s) from speech", e.Lines), 0)

	case events.ToolCallStarted:
		if e.JobID != "" {
			m.jobToolStarted(e)
			break
		}
		msg := "tool " + e.Name
		if e.Summary != "" {
			msg += " (" + e.Summary + ")"
		}
		m.appendActivity("tool", msg, 0)
		m.setStatus("🔧 "+msg+"...", false)
		m.appendTranscript(dimStyle.Render("  🔧 " + msg))

	case events.ToolCallFinished:
		if e.JobID != "" {
			m.jobToolFinished(e)
			break
		}
		msg := "tool " + toolOutcomeLabel(e.OutcomeStatus())
		if e.Name != "" {
			msg = "tool " + e.Name + " " + toolOutcomeLabel(e.OutcomeStatus())
		}
		if preview := e.ResultPreview(); preview != "" {
			msg += " → " + preview
		}
		if reason := e.ResultReason(); reason != "" {
			msg += ": " + reason
		}
		if e.Outcome != nil {
			if e.Outcome.ExitCode != nil {
				msg += fmt.Sprintf(" (exit code %d)", *e.Outcome.ExitCode)
			}
			if e.Outcome.Signal != "" {
				msg += " (signal " + e.Outcome.Signal + ")"
			}
		}
		if elapsed := e.ResultElapsed(); elapsed > 0 {
			msg += " in " + elapsed.String()
		}
		if status := e.OutcomeStatus(); status == events.ToolOutcomeFailed || status == events.ToolOutcomeTimedOut || status == events.ToolOutcomeCanceled {
			m.setVoiceMode(anim.ModeError)
			m.setStatus("✗ "+msg, true)
		} else {
			m.setVoiceMode(anim.ModeThinking)
			m.setStatus(m.agentName+" thinking", false)
		}
		prefix := "✓ "
		if e.OutcomeStatus() == events.ToolOutcomeFailed || e.OutcomeStatus() == events.ToolOutcomeTimedOut || e.OutcomeStatus() == events.ToolOutcomeCanceled {
			prefix = "✗ "
		} else if e.OutcomeStatus() == events.ToolOutcomeUnknown {
			prefix = "? "
		}
		m.appendTranscript(dimStyle.Render("  " + prefix + msg))
		m.appendActivity("tool", msg, 0)

	case events.JobQueued:
		m.jobQueued(e)

	case events.JobStarted:
		m.jobStarted(e)

	case events.JobFinished:
		m.jobFinished(e)

	case events.JobFailed:
		m.jobFailed(e)

	case events.JobAborted:
		m.jobAborted(e)
	}
}
