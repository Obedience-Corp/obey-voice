package playback

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
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

// Player handles in-process audio playback through miniaudio.
type nativeOutput interface {
	Stop() error
	Uninit()
}

type Player struct {
	testRoute    func() (string, error)
	testOpen     func(int) (nativeOutput, int, int, error)
	openedAt     time.Time
	routeID      string
	callbackGaps atomic.Uint64
	underruns    atomic.Uint64
	recording    audio.RecordingFeed
	mu           sync.Mutex
	// deviceMu serializes native device initialization and teardown. Never hold
	// mu while calling malgo Start/Stop/Uninit: miniaudio may synchronously call
	// onData, which also needs mu.
	deviceMu         sync.Mutex
	generation       uint64
	epoch            uint64
	workers          map[*playbackSegment]context.CancelFunc
	lifecycleState   string
	recoveryReason   string
	lastError        string
	recoveryAttempts int
	recoveryCh       chan recoverySignal
	recoveryDone     chan struct{}
	lastCallback     atomic.Int64
	ctx              *malgo.AllocatedContext
	device           nativeOutput
	sampleRate       int
	channels         int // device client channel count (mono TTS is expanded into this)
	frontend         audio.Frontend
	// aecIngress receives clock-aligned mono PCM from onData and pushes the
	// capture-rate far-end reference on a worker (callback-safe).
	aecIngress       *aecRefIngress
	current          *playbackSegment
	queue            []*playbackSegment
	playing          atomic.Bool
	closed           bool
	deviceName       string
	actualDeviceName string
	clientRate       int
	clientChannels   int
	debugRoot        string
	debug            *playerDebugRecorder
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

// NegotiatedDevice reports the last opened device and its negotiated format.
func (p *Player) NegotiatedDevice() audio.NegotiatedDevice {
	p.mu.Lock()
	defer p.mu.Unlock()
	return audio.NegotiatedDevice{RequestedName: p.deviceName, Name: p.actualDeviceName, IdentityKnown: p.actualDeviceName != "", SampleRate: p.sampleRate, Channels: p.channels, ClientSampleRate: p.clientRate, ClientChannels: p.clientChannels}
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player {
	return NewPlayerWithDevice("")
}

// NewPlayerWithDevice creates a player routed to deviceName. An empty name
// follows the operating-system default.
func NewPlayerWithDevice(deviceName string) *Player {
	p := &Player{deviceName: deviceName, debugRoot: audio.DebugAudioDir()}
	p.volumeHundredths.Store(defaultVolumeHundredths)
	return p
}

// SetFrontend installs an audio front-end that can observe playback
// reference audio for echo cancellation or similar processing.
func (p *Player) SetFrontend(frontend audio.Frontend) {
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
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
func (p *Player) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if stream == nil {
		return nil, errors.New("nil pcm stream")
	}

	producerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	segment := newPlaybackSegment()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		cancel()
		go drainPlaybackStream(producerCtx, stream)
		return nil, errors.New("audio player is closed")
	}
	if generation, bound := audio.PlaybackGeneration(ctx, p); bound && generation != p.epoch {
		p.mu.Unlock()
		cancel()
		go drainPlaybackStream(producerCtx, stream)
		return nil, context.Canceled
	}
	epoch := p.epoch
	if p.workers == nil {
		p.workers = make(map[*playbackSegment]context.CancelFunc)
	}
	p.workers[segment] = cancel
	p.mu.Unlock()
	segment.attempt = audio.PlaybackAttemptFromContext(ctx)
	go func() {
		select {
		case <-segment.finishedCh:
		case <-ctx.Done():
		}
		cancel()
		segment.mu.Lock()
		underruns := segment.underruns
		segment.mu.Unlock()
		p.underruns.Add(uint64(underruns))
		if underruns > 0 {
			p.logLifecycle("segment_underruns", "render_starvation", nil)
		}
		p.mu.Lock()
		delete(p.workers, segment)
		p.mu.Unlock()
	}()
	go runPlaybackWorker(ctx, segment, stream, func() {
		defer func() {
			if ctx.Err() != nil {
				go drainPlaybackStream(producerCtx, stream)
			}
		}()
		p.pumpSegmentGeneration(ctx, segment, stream, epoch)
	})

	if err := segment.waitReady(ctx); err != nil {
		cancel()
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, errors.New("audio player is closed")
	}

	if ctx.Err() != nil || epoch != p.epoch || !segment.attempt.Valid() {
		segment.interrupt()
		return nil, context.Canceled
	}
	p.queue = append(p.queue, segment)
	return segment.playback(), nil
}

// Stop interrupts current playback and clears any queued audio.
func (p *Player) Stop() {
	defer p.logLifecycle("stopped", "intentional_stop", nil)
	p.mu.Lock()
	p.epoch++
	for _, cancel := range p.workers {
		cancel()
	}
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
	defer p.logLifecycle("closed", "shutdown", nil)
	p.Stop()

	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.lifecycleState = "closed"
	if p.recoveryDone != nil {
		close(p.recoveryDone)
		p.recoveryDone = nil
	}
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

func (p *Player) SubscribeRecording(buffer int, enabled func() bool) (<-chan audio.Frame, func() audio.RecordingStats) {
	return p.recording.SubscribeRecording(buffer, enabled)
}

func drainPlaybackStream(ctx context.Context, stream *audio.PCMStream) {
	for {
		select {
		case _, ok := <-stream.Frames():
			if !ok {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
