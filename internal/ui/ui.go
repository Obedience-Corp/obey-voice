package ui

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// UI handles terminal output for the voice assistant.
type UI struct {
	bus   *events.Bus
	name  string
	out   io.Writer
	plain bool
}

// New creates a UI writing to stdout and subscribes it to the event bus.
func New(bus *events.Bus, agentName string) *UI {
	return NewWithOutput(bus, agentName, os.Stdout)
}

// NewWithOutput is New with an explicit sink. serve passes its human-output
// writer: stdout there belongs to the --banner-json protocol, and --log-file
// expects the conversation mirror in the file rather than on a pipe the
// spawning app may already have closed.
//
// A sink that is not a console gets plain records: no cursor control, one
// newline-terminated line per event, and no transient status frames at all.
// Redrawing "Listening..." in place is animation, and a file or pipe that
// accumulates those as carriage-return fragments is not a log anyone can read
// or tail. Console output is unchanged.
func NewWithOutput(bus *events.Bus, agentName string, out io.Writer) *UI {
	if out == nil {
		out = os.Stdout
	}
	return newUI(bus, agentName, out, !isConsole(out))
}

// newUI is the shared constructor; plain is the console/file decision, taken
// by the exported constructors and set explicitly by tests.
func newUI(bus *events.Bus, agentName string, out io.Writer, plain bool) *UI {
	u := &UI{bus: bus, name: agentName, out: out, plain: plain}

	events.Subscribe(bus, u.onSTTPhase)
	events.Subscribe(bus, u.onTranscriptPartial)
	events.Subscribe(bus, u.onUserInput)
	events.Subscribe(bus, u.onThinkingStarted)
	events.Subscribe(bus, u.onThinkingComplete)
	events.Subscribe(bus, u.onResponseStreamingStarted)
	events.Subscribe(bus, u.onResponseSpeakableStarted)
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
	events.Subscribe(bus, u.onJobQueued)
	events.Subscribe(bus, u.onJobStarted)
	events.Subscribe(bus, u.onJobFinished)
	events.Subscribe(bus, u.onJobFailed)
	events.Subscribe(bus, u.onJobAborted)
	events.Subscribe(bus, u.onVoiceGateStripped)

	return u
}

// isConsole reports whether w is a character device — a terminal that can act
// on cursor control, rather than a file or a pipe that only stores bytes.
func isConsole(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// status prints a dim metric/phase line, clearing any transient text first.
func (u *UI) status(text string) {
	if u.plain {
		fmt.Fprintf(u.out, "%s\n", dimStyle.Render(text))
		return
	}
	fmt.Fprintf(u.out, "\033[2K%s\n", dimStyle.Render(text))
}

// transient writes an in-place status frame. A plain sink drops it: it is
// cursor animation, not a record, and it carries nothing the durable events
// around it do not already say.
func (u *UI) transient(text string) {
	if u.plain {
		return
	}
	fmt.Fprintf(u.out, "  %s\r", text)
}

// transientClear is transient for a frame that must erase a longer one first.
func (u *UI) transientClear(text string) {
	if u.plain {
		return
	}
	fmt.Fprintf(u.out, "\033[2K  %s\r", text)
}

// record writes one durable line, erasing any transient frame under it.
func (u *UI) record(text string) {
	if u.plain {
		fmt.Fprintf(u.out, "  %s\n", text)
		return
	}
	fmt.Fprintf(u.out, "\033[2K  %s\n", text)
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
		u.transient(statusStyle.Render("🎙 Listening..."))
	case "hearing":
		u.transient(statusStyle.Render("🎙 Hearing you..."))
	case "transcribing":
		u.transient(dimStyle.Render("● Transcribing..."))
	}
}

func (u *UI) onTranscriptPartial(e events.TranscriptPartial) {
	u.transientClear(dimStyle.Render("You (partial):") + " " + dimStyle.Render(e.Text))
}

func (u *UI) onUserInput(e events.UserInput) {
	u.record(userStyle.Render("You:") + " " + e.Text)
}

func (u *UI) onThinkingStarted(_ events.ThinkingStarted) {
	u.transient(dimStyle.Render(fmt.Sprintf("● %s thinking...", u.name)))
}

func (u *UI) onThinkingComplete(e events.ThinkingComplete) {
	u.status(fmt.Sprintf("    model complete (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onResponseStreamingStarted(e events.ResponseStreamingStarted) {
	u.status(fmt.Sprintf("    first token (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onResponseSpeakableStarted(e events.ResponseSpeakableStarted) {
	u.status(fmt.Sprintf("    first speakable (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onSpeechSegmentReady(e events.SpeechSegmentReady) {
	if e.Elapsed > 0 {
		u.status(fmt.Sprintf("    first admit (%.1fs)", e.Elapsed.Seconds()))
	}
	u.transient(dimStyle.Render("● Queuing speech..."))
}

func (u *UI) onGeneratingVoice(_ events.GeneratingVoice) {
	u.transient(dimStyle.Render("● Synthesizing voice..."))
}

func (u *UI) onVoiceGenerated(e events.VoiceGenerated) {
	u.status(fmt.Sprintf("    synth first frame (%.1fs)", e.Elapsed.Seconds()))
}

func (u *UI) onSpeakingStarted(e events.SpeakingStarted) {
	if e.Elapsed > 0 {
		label := "audible"
		if e.AudibleSink == "stream_client" {
			label = "audible_tx"
		}
		u.status(fmt.Sprintf("    %s (%.1fs)", label, e.Elapsed.Seconds()))
	}
	u.transient(statusStyle.Render("● Speaking..."))
}

func (u *UI) onSpeakingComplete(e events.SpeakingComplete) {
	switch e.AudibleSink {
	case "none":
		u.status(fmt.Sprintf("    synth drain (no sink) (%.1fs)", e.Elapsed.Seconds()))
		return
	case "stream_client":
		// Fanout drain is not playback; TTFA was audible_tx at sink-start.
		return
	}
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
	u.dimLine(jobPrefix(e.JobID) + msg)
}

// jobPrefix marks a line as a background worker's rather than the live brain's.
func jobPrefix(jobID string) string {
	if jobID == "" {
		return ""
	}
	return "[" + jobID + "] "
}

func (u *UI) dimLine(text string) {
	fmt.Fprintf(u.out, "  %s\n", dimStyle.Render(text))
}

// A job prints once when accepted and once when it actually begins; the two
// are back to back unless it had to wait behind another job.
func (u *UI) onJobQueued(e events.JobQueued) {
	u.dimLine(fmt.Sprintf("⚙ %s queued: %s", e.ID, e.Task))
}

func (u *UI) onJobStarted(e events.JobStarted) {
	u.dimLine(fmt.Sprintf("⚙ %s started: %s", e.ID, e.Task))
}

func (u *UI) onJobFinished(e events.JobFinished) {
	u.dimLine(fmt.Sprintf("✓ %s finished (%s)", e.ID, e.Elapsed.Round(time.Second)))
}

func (u *UI) onJobFailed(e events.JobFailed) {
	u.dimLine(fmt.Sprintf("✗ %s failed: %s", e.ID, e.Err))
}

func (u *UI) onJobAborted(e events.JobAborted) {
	u.dimLine(fmt.Sprintf("■ %s killed after %s", e.ID, e.Elapsed.Round(time.Second)))
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
	u.dimLine(jobPrefix(e.JobID) + msg)
}
