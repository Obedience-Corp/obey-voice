package netapi

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// One /v1/stream connection: its outbound queues, and the audio it is holding
// for a voice turn that has not started yet.

type streamConn struct {
	out   chan []byte // event / control envelopes
	audio chan []byte // TTS audio_chunk / audio_end / audio_reset only
	kick  chan string
	once  sync.Once

	// token is the bearer that opened this stream (primary or D2 device).
	// Used to evict only that device's sockets on DELETE /v1/devices/{id}.
	token string

	// audioStream is a per-connection preference set by the audio_output
	// control message. atomic so the hub hot path never takes a second lock.
	audioStream atomic.Bool
	// audioBlocked is set for every stream client when the shared turn is
	// interrupted. It stays set until the next turn starts, preventing tail
	// chunks from the canceled turn from crossing the reset boundary.
	audioBlocked atomic.Bool
	// voiceQueued marks a voice turn this connection has queued behind
	// another turn and that has not claimed the mic yet. Cleared when the
	// turn is dequeued or when the connection goes away — a cleared flag
	// abandons the queued turn.
	voiceQueued atomic.Bool

	// watch bounds the voice turn this connection has open, so a client that
	// stops sending audio without ever sending voice_end cannot hold the
	// exclusive mic forever.
	watch voiceWatchdog

	// staged holds the PCM this connection streamed while its voice turn was
	// waiting in the queue. The claim is only granted on dequeue, so those
	// frames have nowhere to go yet; they are replayed into the ingress the
	// instant the turn starts. Without this a client that streams straight
	// after voice_start — which the bundled web client does — would lose its
	// whole utterance whenever the pipeline happened to be busy.
	stagedMu    sync.Mutex
	staged      []float32
	stagedFinal bool
	stagingOpen bool
}

func (c *streamConn) evict(reason string) {
	c.once.Do(func() { c.kick <- reason })
}

func (c *streamConn) wantsAudio() bool {
	return c.audioStream.Load()
}

// stagedMaxSamples caps the PCM held for one queued voice turn at the same
// ceiling the utterance cap uses. Past it, further frames are refused rather
// than the head being dropped: the start of an utterance is the part STT
// cannot do without.
const stagedMaxSamples = int(maxVoiceUtterance/time.Second) * audio.SampleRate

// openStaging arms the buffer for a voice turn that is about to be queued.
func (c *streamConn) openStaging() {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	c.staged = nil
	c.stagedFinal = false
	c.stagingOpen = true
}

// stage buffers samples for a queued turn. It reports false once staging has
// closed — the claim has been granted, so the caller must write live instead.
func (c *streamConn) stage(samples []float32) bool {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	if !c.stagingOpen {
		return false
	}
	if room := stagedMaxSamples - len(c.staged); room > 0 {
		if len(samples) > room {
			samples = samples[:room]
		}
		c.staged = append(c.staged, samples...)
	}
	return true
}

// markStagedFinal records that the client ended its utterance while the turn
// was still queued. False once staging has closed.
func (c *streamConn) markStagedFinal() bool {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	if !c.stagingOpen {
		return false
	}
	c.stagedFinal = true
	return true
}

// closeStaging hands back everything buffered and stops further staging, so
// no frame can be written twice or land after the replay.
func (c *streamConn) closeStaging() (samples []float32, final bool) {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	samples, final = c.staged, c.stagedFinal
	c.staged, c.stagedFinal, c.stagingOpen = nil, false, false
	return samples, final
}

// dropStaged discards a queued turn's audio when that turn is abandoned.
func (c *streamConn) dropStaged() {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	c.staged, c.stagedFinal, c.stagingOpen = nil, false, false
}
