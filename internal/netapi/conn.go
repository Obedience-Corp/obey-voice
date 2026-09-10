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
	// audioBinary is set with audio_output binary:true. The fanout then
	// writes OVA1 binary TTS chunks instead of JSON+base64. Cleared when
	// stream mode turns off.
	audioBinary atomic.Bool
	// audioBlocked is set for every stream client when the shared turn is
	// interrupted. It stays set until the next turn starts, preventing tail
	// chunks from the canceled turn from crossing the reset boundary.
	audioBlocked atomic.Bool
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
	//
	// stagedGen is the watchdog generation of the queued turn the buffer
	// belongs to, and 0 when nothing is queued: generations start at 1, so it
	// is the marker that the turn exists as well as its identity. Marker and
	// audio live under one lock deliberately — see openQueuedTurn.
	stagedMu    sync.Mutex
	staged      []float32
	stagedGen   uint64
	stagedFinal bool
}

func (c *streamConn) evict(reason string) {
	c.once.Do(func() { c.kick <- reason })
}

func (c *streamConn) wantsAudio() bool {
	return c.audioStream.Load()
}

func (c *streamConn) wantsBinaryAudio() bool {
	return c.audioStream.Load() && c.audioBinary.Load()
}

// stagedMaxSamples caps the PCM held for one queued voice turn at the same
// ceiling the utterance cap uses. Past it, further frames are refused rather
// than the head being dropped: the start of an utterance is the part STT
// cannot do without.
const stagedMaxSamples = int(maxVoiceUtterance/time.Second) * audio.SampleRate

// A queued turn is one thing under one lock: the marker that it is waiting, the
// PCM it has streamed and its end-of-utterance flag are taken or dropped
// together. Held apart, the deadline firing for that turn could clear the audio
// after the dequeue had taken the marker and before it read the buffer, and the
// promoted turn would start on silence with its owner told nothing.

// openQueuedTurn arms the buffer for the voice turn gen identifies, which is
// about to be queued.
func (c *streamConn) openQueuedTurn(gen uint64) {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	c.staged, c.stagedGen, c.stagedFinal = nil, gen, false
}

// stage buffers samples for a queued turn. It reports false once that turn has
// been taken or abandoned — the claim has been granted, so the caller must
// write live instead.
func (c *streamConn) stage(samples []float32) bool {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	if c.stagedGen == 0 {
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
// was still queued. False once that turn is gone.
func (c *streamConn) markStagedFinal() bool {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	if c.stagedGen == 0 {
		return false
	}
	c.stagedFinal = true
	return true
}

// takeQueuedTurn hands the whole queued turn to the dispatch goroutine that is
// promoting it: marker, audio and final flag leave in one step, so a deadline
// still in flight for that turn finds nothing left to take and reports nothing.
// ok is false when there is no queued turn — it was abandoned, or already
// taken. Taking also stops further staging, so a frame either lands in this
// batch or is written live afterwards, never both and never neither.
func (c *streamConn) takeQueuedTurn() (samples []float32, final, ok bool) {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	if c.stagedGen == 0 {
		return nil, false, false
	}
	samples, final = c.staged, c.stagedFinal
	c.clearLocked()
	return samples, final, true
}

// abandonQueuedTurn drops the queued turn while it is still the one gen names,
// and reports whether it was: the deadline that calls this may be firing for a
// turn that has since been dequeued or replaced by another.
func (c *streamConn) abandonQueuedTurn(gen uint64) bool {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	if c.stagedGen != gen {
		return false
	}
	c.clearLocked()
	return true
}

// dropStaged discards whatever is queued, whichever turn it belongs to: the
// socket closed, or a live voice_end retired it.
func (c *streamConn) dropStaged() {
	c.stagedMu.Lock()
	defer c.stagedMu.Unlock()
	c.clearLocked()
}

func (c *streamConn) clearLocked() {
	c.staged, c.stagedGen, c.stagedFinal = nil, 0, false
}
