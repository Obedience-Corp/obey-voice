package audio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	playbackChannels         = 1
	playbackCompactionFrames = 4096
	// playbackPeriodFrames and playbackPeriods describe the device ring buffer.
	// Their product is how much audio onData has queued but not yet made
	// audible, which is the bulk of the AEC reference delay.
	playbackPeriodFrames = 512
	playbackPeriods      = 3

	// incrementalPreRoll is how much audio an incremental segment buffers
	// before it becomes audible.
	//
	// It is a fixed head start, not one scaled to the reply's length, because
	// this path is only taken where synthesis is measured to run *faster* than
	// realtime: the buffer grows from here rather than shrinking, so the only
	// thing that has to be survived is a late delivery, not a rate deficit. A
	// producer slower than realtime needs a pre-buffer proportional to the
	// whole utterance (audio_len × (RTF − 1)) — that engine must not take this
	// path at all, which is the streaming-eligibility policy's job.
	//
	// Why 150 ms. The device's own ring is playbackPeriods × playbackPeriodFrames,
	// which is ~32 ms at 48 kHz and ~64 ms at 24 kHz — so this is roughly two
	// to five ring-fulls of slack behind whatever miniaudio has already queued.
	// (Counting it in periods is rate-dependent and therefore the wrong unit:
	// 150 ms is 7 periods at 24 kHz and 14 at 48 kHz. Time is the invariant.)
	// Against it: the measured CUDA producer delivers 1 s of audio every 0.55 s,
	// so the gap it has to cover is a scheduling hiccup, not a shortfall. In
	// favour of more: it is also the whole budget for a LAN round trip, and the
	// Topology B path fights network jitter rather than scheduler jitter — an
	// arriving chunk that is 200 ms late is unremarkable on wifi and would run
	// this dry.
	//
	// It stays a constant until that is measured rather than guessed. The
	// hardware pass reports per-segment Underruns, which is exactly the signal
	// that would justify raising it, and qwen_tts_stream is where a knob would
	// hang if it does.
	incrementalPreRoll = 150 * time.Millisecond
)

// PlaybackResult reports the terminal state of a queued playback segment.
type PlaybackResult struct {
	AttemptID    string
	CaptureEpoch uint64
	Err          error
	Interrupted  bool
	// AudioSeconds is the duration of the audio the synthesizer produced for
	// this segment, measured at the stream's own sample rate before any
	// resampling. It is the denominator of the realtime factor, so it reports
	// what was synthesized even when playback was interrupted before the
	// samples were heard.
	AudioSeconds float64
	// Underruns counts how many times this segment ran dry: the device asked
	// for audio, the segment had none buffered, and synthesis had not finished
	// producing it. Each one is an audible gap *inside* a sentence.
	//
	// A whole-utterance segment cannot report one — its audio is complete
	// before it becomes audible. Only an incremental stream can, which is
	// exactly why the count exists: it is the failure mode streaming
	// introduces, and dead-air accounting cannot see it because that only
	// measures the silence *between* segments.
	Underruns int
	// SynthesisEndedAt is when the last frame of this segment arrived from the
	// synthesizer, or the zero time when the player did not observe an end
	// (a barge-in that discarded the segment first).
	//
	// A whole-utterance segment becomes audible only after synthesis ends, so
	// the caller can time that from its own side. A streamed one is audible
	// long before, so the only honest numerator for its realtime factor is
	// this stamp.
	SynthesisEndedAt time.Time
}

// Playback exposes playback lifecycle signals for a single queued segment.
type Playback struct {
	started <-chan struct{}
	done    <-chan PlaybackResult
}

// NewPlayback constructs a playback handle from lifecycle channels.
func NewPlayback(started <-chan struct{}, done <-chan PlaybackResult) *Playback {
	return &Playback{started: started, done: done}
}

// Started closes when audible playback begins for the segment.
func (p *Playback) Started() <-chan struct{} {
	return p.started
}

// Done delivers the terminal playback result.
func (p *Playback) Done() <-chan PlaybackResult {
	return p.done
}

// Engine is the playback interface used by the pipeline.
// Engine plays synthesized PCM. PlayStream takes ownership of stream even when
// it returns an error: implementations must drain (or cancel) the stream so
// synth producers are not left blocked on a full frames channel.
type Engine interface {
	PlayStream(ctx context.Context, stream *PCMStream) (*Playback, error)
	Stop()
	IsPlaying() bool
	Close() error
}

// Player handles in-process audio playback through miniaudio.
type Player struct {
	mu sync.Mutex
	// deviceMu serializes native device initialization and teardown. Never hold
	// mu while calling malgo Start/Stop/Uninit: miniaudio may synchronously call
	// onData, which also needs mu.
	deviceMu   sync.Mutex
	ctx        *malgo.AllocatedContext
	device     *malgo.Device
	sampleRate int
	channels   int // device client channel count (mono TTS is expanded into this)
	frontend   Frontend
	// aecIngress receives clock-aligned mono PCM from onData and pushes the
	// capture-rate far-end reference on a worker (callback-safe).
	aecIngress *aecRefIngress
	current    *playbackSegment
	queue      []*playbackSegment
	playing    atomic.Bool
	closed     bool
	deviceName string
	debugRoot  string
	debug      *playerDebugRecorder
	// monoScratch holds one callback period of mono S16 while onData expands
	// into the multi-channel device buffer. Sized lazily under the callback.
	monoScratch []byte
	// observedPeriodFrames is the frame count miniaudio actually asks for.
	// Backends quantize or override PeriodSizeInFrames, and AEC alignment now
	// depends on the ring geometry, so the request is no longer trusted as the
	// truth. Written from the device callback, read when publishing the delay.
	observedPeriodFrames atomic.Int32
	// volumeHundredths is linear playback gain × 100, applied in onData on
	// mono S16 before expand so a settings slider takes effect mid-utterance
	// and AEC hears the same samples the speaker does. 100 is unity.
	volumeHundredths atomic.Int32
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player {
	return NewPlayerWithDevice("")
}

// NewPlayerWithDevice creates a player routed to deviceName. An empty name
// follows the operating-system default.
func NewPlayerWithDevice(deviceName string) *Player {
	p := &Player{deviceName: deviceName, debugRoot: debugAudioDir()}
	p.volumeHundredths.Store(defaultVolumeHundredths)
	return p
}

// SetFrontend installs an audio front-end that can observe playback
// reference audio for echo cancellation or similar processing.
func (p *Player) SetFrontend(frontend Frontend) {
	p.mu.Lock()
	old := p.aecIngress
	p.aecIngress = nil
	p.frontend = frontend
	p.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if frontend == nil {
		return
	}
	ingress := newAECRefIngress(frontend)
	p.mu.Lock()
	if p.closed || p.frontend != frontend {
		p.mu.Unlock()
		ingress.Close()
		return
	}
	p.aecIngress = ingress
	p.mu.Unlock()
	p.applyReferenceDelay()
}

// PlayStream queues a synthesized PCM stream for playback.
func (p *Player) PlayStream(ctx context.Context, stream *PCMStream) (*Playback, error) {
	if stream == nil {
		return nil, errors.New("nil pcm stream")
	}

	segment := newPlaybackSegment()
	segment.attempt = PlaybackAttemptFromContext(ctx)
	go runPlaybackWorker(ctx, segment, stream, func() {
		p.pumpSegment(ctx, segment, stream)
	})

	if err := segment.waitReady(ctx); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, errors.New("audio player is closed")
	}

	if ctx.Err() != nil || !segment.attempt.Valid() {
		segment.interrupt()
		return nil, context.Canceled
	}
	p.queue = append(p.queue, segment)
	return segment.playback(), nil
}

// Stop interrupts current playback and clears any queued audio.
func (p *Player) Stop() {
	p.mu.Lock()
	segments := make([]*playbackSegment, 0, len(p.queue)+1)
	if p.current != nil {
		segments = append(segments, p.current)
		p.current = nil
	}
	segments = append(segments, p.queue...)
	p.queue = nil
	p.mu.Unlock()

	p.playing.Store(false)
	for _, segment := range segments {
		segment.interrupt()
	}
}

// IsPlaying returns whether audio is currently audible.
func (p *Player) IsPlaying() bool {
	return p.playing.Load()
}

// Close releases audio resources.
func (p *Player) Close() error {
	p.Stop()

	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	device := p.device
	p.device = nil
	ctx := p.ctx
	p.ctx = nil
	debug := p.debug
	p.debug = nil
	ingress := p.aecIngress
	p.aecIngress = nil
	p.frontend = nil
	p.mu.Unlock()

	// Stop the device before the worker: until Stop returns, onData can still
	// run with a snapped ingress pointer, and enqueueing into a worker that has
	// already exited just fills the ready queue until blocks drop. Non-blocking
	// either way, but this keeps "Close stops the worker" true.
	if device != nil {
		_ = device.Stop()
		device.Uninit()
	}

	if ingress != nil {
		ingress.Close()
		debug.setAECReferenceDrops(ingress.droppedBlocks())
	}

	if ctx != nil {
		_ = ctx.Uninit()
		ctx.Free()
	}
	if debug != nil {
		debug.close()
	}

	return nil
}
