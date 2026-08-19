package netapi

import (
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// The serve-side bound on one push-to-talk turn.
//
// voice_end and a *detected* disconnect used to be the only things that handed
// the exclusive mic back, and a half-open socket is neither. A Mac client whose
// socket died mid-turn left the claim standing: the recogniser sat waiting for
// frames that were never coming, and every later turn queued behind a turn
// nobody would ever end. The deadline below ends it instead.

const (
	// defaultVoiceSilence bounds the wait for the next audio_input frame of a
	// turn that has started. It matches the STT start timeout (config
	// listen_timeout), which asks the same question one layer down.
	defaultVoiceSilence = 10 * time.Second

	// maxVoiceUtterance is the ceiling the endpoint policy puts on a single
	// utterance (config phrase_time_limit); the staging buffer holds exactly
	// this much audio for a queued turn.
	maxVoiceUtterance = 30 * time.Second

	// defaultVoiceTurnMax bounds the whole turn, because frames refresh the
	// silence window: a client that streams forever without ever sending
	// voice_end would otherwise hold the mic forever. It is the longest turn
	// the endpoint policy can still make an utterance out of — the utterance
	// ceiling plus the window it waits for speech to begin.
	defaultVoiceTurnMax = defaultVoiceSilence + maxVoiceUtterance
)

// voiceSilentReason labels a turn serve ended because its client stopped
// feeding it audio, distinguishing it from the remote_interrupt a client-driven
// barge-in reports.
const voiceSilentReason = "client_silent"

// voiceWatchdog is one connection's turn deadline. A single timer carries both
// bounds — the silence window, which every frame pushes out, and the turn cap,
// which nothing does — so there is never a second timer racing the first.
type voiceWatchdog struct {
	mu      sync.Mutex
	timer   *time.Timer
	gen     uint64
	silence time.Duration
	frame   time.Time // armed at, or the last frame seen
	hard    time.Time // the turn cap, measured from voice_start
}

// arm starts the deadline for a turn that has just begun. fire runs at most
// once per armed turn, on the timer goroutine, and never after disarm.
func (w *voiceWatchdog) arm(silence, max time.Duration, fire func()) {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stop()
	w.gen++
	gen := w.gen
	w.silence, w.frame, w.hard = silence, now, now.Add(max)
	w.timer = time.AfterFunc(w.left(now), func() { w.expire(gen, fire) })
}

// refresh pushes the silence window out on an inbound frame, never past the
// turn cap. It does nothing when no turn is armed.
func (w *voiceWatchdog) refresh() {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer == nil {
		return
	}
	w.frame = now
	w.timer.Reset(w.left(now))
}

// disarm ends the deadline: the turn closed itself, or the connection did.
func (w *voiceWatchdog) disarm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stop()
}

func (w *voiceWatchdog) stop() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

// left is whichever deadline comes first. Non-positive means both have passed.
func (w *voiceWatchdog) left(now time.Time) time.Duration {
	left := w.frame.Add(w.silence).Sub(now)
	if hard := w.hard.Sub(now); hard < left {
		left = hard
	}
	return left
}

// expire ends the turn the timer was armed for. gen pins it to that turn, and a
// frame that landed while it waited on the lock rearms it rather than cutting a
// turn that is still streaming.
func (w *voiceWatchdog) expire(gen uint64, fire func()) {
	w.mu.Lock()
	if w.timer == nil || w.gen != gen {
		w.mu.Unlock()
		return
	}
	if left := w.left(time.Now()); left > 0 {
		w.timer.Reset(left)
		w.mu.Unlock()
		return
	}
	w.stop()
	w.mu.Unlock()
	fire()
}

// armVoiceTurn starts the deadline for a turn this connection just opened.
func (s *Server) armVoiceTurn(conn *streamConn) {
	conn.watch.arm(s.voiceSilence, s.voiceTurnMax, func() { s.voiceTurnStalled(conn) })
}

// voiceTurnStalled ends a turn whose client stopped sending audio: the mic goes
// back, anything the connection had queued is abandoned, and every client is
// told the turn is over so none of them waits on a reply that is not coming.
//
// A claim whose utterance the recogniser has already finalized is only taken
// back, never reported: that turn ran on audio the user really spoke and is
// still producing its reply. Telling a client a turn ended that had already
// ended is the same lie an idle interrupt refuses to tell.
func (s *Server) voiceTurnStalled(conn *streamConn) {
	queued := conn.voiceQueued.Swap(false)
	conn.dropStaged()
	live := s.hub.utteranceOpen(conn)
	s.hub.releaseMic(conn)
	if !queued && !live {
		return
	}
	if live {
		s.dispatcher.Interrupt()
	}
	s.opts.Bus.Emit(events.TurnInterrupted{Reason: voiceSilentReason})
}
