package ui

import (
	"fmt"
	"io"
	"os"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// UI handles terminal output for the voice assistant.
type UI struct {
	bus  *events.Bus
	name string
	out  io.Writer
}

// New creates a UI writing to stdout and subscribes it to the event bus.
func New(bus *events.Bus, agentName string) *UI {
	return NewWithOutput(bus, agentName, os.Stdout)
}

// NewWithOutput is New with an explicit sink. serve passes its human-output
// writer: stdout there belongs to the --banner-json protocol, and --log-file
// expects the conversation mirror in the file rather than on a pipe the
// spawning app may already have closed.
func NewWithOutput(bus *events.Bus, agentName string, out io.Writer) *UI {
	if out == nil {
		out = os.Stdout
	}
	u := &UI{bus: bus, name: agentName, out: out}

	events.Subscribe(bus, u.onSTTPhase)
	events.Subscribe(bus, u.onTranscriptPartial)
	events.Subscribe(bus, u.onUserInput)
	events.Subscribe(bus, u.onThinkingStarted)
	events.Subscribe(bus, u.onThinkingComplete)
	events.Subscribe(bus, u.onResponseStreamingStarted)
	events.Subscribe(bus, u.onSpeechSegmentReady)
	events.Subscribe(bus, u.onGeneratingVoice)
	events.Subscribe(bus, u.onVoiceGenerated)
	events.Subscribe(bus, u.onSpeakingStarted)
	events.Subscribe(bus, u.onSpeakingComplete)
	events.Subscribe(bus, u.onSpeakingInterrupted)
	events.Subscribe(bus, u.onResponseReady)
	events.Subscribe(bus, u.onCleared)
	events.Subscribe(bus, u.onError)
	events.Subscribe(bus, u.onInfo)
	events.Subscribe(bus, u.onToolCallStarted)
	events.Subscribe(bus, u.onToolCallFinished)
	events.Subscribe(bus, u.onVoiceGateStripped)

	return u
}

// status prints a dim metric/phase line, clearing any transient text first.
func (u *UI) status(text string) {
	fmt.Fprintf(u.out, "\033[2K%s\n", dimStyle.Render(text))
}

// ShowWelcome displays the startup banner.
func (u *UI) ShowWelcome() {
	banner := bannerStyle.Render(
		titleStyle.Render(fmt.Sprintf("%s (Go)", u.name)) + "\n" +
			dimStyle.Render("Ultra-low-latency voice assistant"),
	)
	fmt.Fprintln(u.out)
	fmt.Fprintln(u.out, banner)
	fmt.Fprintln(u.out)
	fmt.Fprintln(u.out, dimStyle.Render("  Say something, and I'll respond."))
	fmt.Fprintln(u.out, dimStyle.Render("  Press Ctrl+C to exit."))
	fmt.Fprintln(u.out)
}

// ShowGoodbye displays the exit message.
func (u *UI) ShowGoodbye() {
	fmt.Fprintln(u.out)
	fmt.Fprintln(u.out, dimStyle.Render("  See you later."))
	fmt.Fprintln(u.out)
}

// ShowProviders shows active providers.
func (u *UI) ShowProviders(ttsProvider, sttProvider string) {
	fmt.Fprintf(u.out, "  %s\n", dimStyle.Render(fmt.Sprintf("TTS: %s | STT: %s", ttsProvider, sttProvider)))
}

func (u *UI) onSTTPhase(e events.STTPhase) {
	if e.Elapsed > 0 {
		u.status(fmt.Sprintf("    %s (%.1fs)", e.Phase, e.Elapsed.Seconds()))
		return
	}
	switch e.Phase {
	case "listening":
		fmt.Fprintf(u.out, "  %s\r", statusStyle.Render("🎙 Listening..."))
	case "hearing":
		fmt.Fprintf(u.out, "  %s\r", statusStyle.Render("🎙 Hearing you..."))
	case "transcribing":
		fmt.Fprintf(u.out, "  %s\r", dimStyle.Render("● Transcribing..."))
	}
}

func (u *UI) onTranscriptPartial(e events.TranscriptPartial) {
	fmt.Fprintf(u.out, "\033[2K  %s %s\r", dimStyle.Render("You (partial):"), dimStyle.Render(e.Text))
}

func (u *UI) onUserInput(e events.UserInput) {
	fmt.Fprintf(u.out, "\033[2K  %s %s\n", userStyle.Render("You:"), e.Text)
}

func (u *UI) onThinkingStarted(_ events.ThinkingStarted) {
	fmt.Fprintf(u.out, "  %s\r", dimStyle.Render(fmt.Sprintf("● %s thinking...", u.name)))
}

func (u *UI) onThinkingComplete(e events.ThinkingComplete) {
	u.status(fmt.Sprintf("    model complete (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onResponseStreamingStarted(e events.ResponseStreamingStarted) {
	u.status(fmt.Sprintf("    first response chunk (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onSpeechSegmentReady(_ events.SpeechSegmentReady) {
	fmt.Fprintf(u.out, "  %s\r", dimStyle.Render("● Queuing speech..."))
}

func (u *UI) onGeneratingVoice(_ events.GeneratingVoice) {
	fmt.Fprintf(u.out, "  %s\r", dimStyle.Render("● Synthesizing voice..."))
}

func (u *UI) onVoiceGenerated(e events.VoiceGenerated) {
	u.status(fmt.Sprintf("    voice ready (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onSpeakingStarted(_ events.SpeakingStarted) {
	fmt.Fprintf(u.out, "  %s\r", statusStyle.Render("● Speaking..."))
}

func (u *UI) onSpeakingComplete(e events.SpeakingComplete) {
	label := "playback"
	if e.Interrupted {
		label = "playback interrupted"
	}
	u.status(fmt.Sprintf("    %s (%.1fs)", label, e.Elapsed.Seconds()))
}

func (u *UI) onSpeakingInterrupted(e events.SpeakingInterrupted) {
	u.status(fmt.Sprintf("    speech interrupted (%s)", e.Reason))
}

func (u *UI) onResponseReady(e events.ResponseReady) {
	fmt.Fprintf(u.out, "  %s %s\n\n", agentStyle.Render(u.name+":"), e.Response)
}

func (u *UI) onCleared(_ events.ConversationCleared) {
	fmt.Fprintln(u.out, dimStyle.Render("  Conversation cleared."))
}

func (u *UI) onError(e events.Error) {
	msg := e.Message
	if e.Stage != "" {
		msg = fmt.Sprintf("[%s] %s", e.Stage, e.Message)
	}
	fmt.Fprintf(u.out, "  %s\n\n", errorStyle.Render("Error: "+msg))
}

func (u *UI) onInfo(e events.Info) {
	fmt.Fprintf(u.out, "  %s\n", dimStyle.Render(e.Message))
}

func (u *UI) onToolCallStarted(e events.ToolCallStarted) {
	msg := "🔧 " + e.Name
	if e.Summary != "" {
		msg += " (" + e.Summary + ")"
	}
	fmt.Fprintf(u.out, "  %s\n", dimStyle.Render(msg))
}

func (u *UI) onVoiceGateStripped(e events.VoiceGateStripped) {
	fmt.Fprintf(u.out, "  %s\n", dimStyle.Render(fmt.Sprintf("voice gate stripped %d tool-syntax line(s) from speech", e.Lines)))
}

func (u *UI) onToolCallFinished(e events.ToolCallFinished) {
	msg := "✓ " + e.Name
	if e.Err != "" {
		msg = "✗ " + e.Name + ": " + e.Err
	} else if e.Preview != "" {
		msg += " → " + e.Preview
	}
	fmt.Fprintf(u.out, "  %s\n", dimStyle.Render(msg))
}
