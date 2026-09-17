package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	SampleRate = 16000
	Channels   = 1
	// ChunkSize is the number of samples per read (100ms at 16kHz).
	ChunkSize = 1600
	// captureStallTimeout is how long callbacks may go missing before the
	// capture device is reopened. Changing OS input volume on PipeWire/ALSA
	// often restarts the graph and never resumes the old miniaudio stream.
	captureStallTimeout = 2 * time.Second
)

// Capture handles microphone input using miniaudio.
//
// Two pieces of state answer two different questions. active is what the caller
// asked for: it is set by Start and cleared only by Stop/Close. running is
// whether a device is open right now. Recovery from a stalled stream closes and
// reopens the device, so running goes false and true again without the caller
// ever asking for it — and a reopen that fails must leave active set, or the
// microphone is dead for the rest of the session with nothing left to retry it.
type Capture struct {
	mu               sync.Mutex
	ingestMu         sync.Mutex // serialize callback processing, ring assignment and publication
	subsMu           sync.RWMutex
	ctx              *malgo.AllocatedContext
	device           *malgo.Device
	buf              *RingBuffer
	raw              *RingBuffer // pre-frontend PCM, aligned with unread buf samples
	preserve         *RingBuffer // NLMS residual, aligned with unread buf samples
	frontend         Frontend
	active           bool          // the caller wants capture running
	running          bool          // a device is open
	watchDone        chan struct{} // closed by Stop so the cancellation watcher exits
	subs             map[int]chan []float32
	frameSubs        map[int]*captureFrameSubscription
	callbackSeq      int64
	nextSub          int
	frameSeq         atomic.Int64
	ingested         atomic.Uint64
	deviceGeneration atomic.Uint64
	deviceName       string
	actualDeviceName string
	deviceRate       int
	deviceChannels   int
	lastData         atomic.Int64 // unix nano of last onData; 0 until Start

	// open and closeDevice are seams: production opens a malgo device, tests
	// substitute a fake so reopen behavior is exercised without hardware.
	open        func() error
	closeDevice func()

	// Logf reports device reopen attempts and their outcome. A microphone that
	// silently stops delivering callbacks is otherwise undiagnosable.
	//
	// It is nil by default, which is silent. Recovery runs while a Bubble Tea
	// program may own the terminal, and a line written to a raw stderr lands on
	// top of that surface — exactly during the mixer move this recovers from.
	// Callers pick a destination that suits their path: log.Printf where stderr
	// is either free or already redirected into the diagnostics log, and an
	// explicit file logger on a TUI path that does not redirect it.
	Logf func(format string, args ...any)
}

// NegotiatedDevice describes the opened stream separately from the requested
// client format. Backend resampling means this is not necessarily the native
// hardware format. Unknown device identity remains explicitly unknown.
type NegotiatedDevice struct {
	RequestedName    string `json:"requested_name"`
	Name             string `json:"name"`
	IdentityKnown    bool   `json:"identity_known"`
	SampleRate       int    `json:"sample_rate"`
	Channels         int    `json:"channels"`
	ClientSampleRate int    `json:"client_sample_rate"`
	ClientChannels   int    `json:"client_channels"`
}

// NegotiatedDevice returns the last successfully opened capture device.
func (c *Capture) NegotiatedDevice() NegotiatedDevice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return NegotiatedDevice{RequestedName: c.deviceName, Name: c.actualDeviceName, IdentityKnown: c.actualDeviceName != "", SampleRate: c.deviceRate, Channels: c.deviceChannels, ClientSampleRate: SampleRate, ClientChannels: Channels}
}

// NewCapture creates a new mic capture instance.
func NewCapture() *Capture {
	return NewCaptureWithDevice("")
}

// NewCaptureWithDevice creates a capture routed to deviceName. An empty name
// follows the operating-system default.
func NewCaptureWithDevice(deviceName string) *Capture {
	ring := SampleRate * 30 // 30 seconds
	c := &Capture{
		buf:        NewRingBuffer(ring),
		raw:        NewRingBuffer(ring),
		preserve:   NewRingBuffer(ring),
		subs:       make(map[int]chan []float32),
		frameSubs:  make(map[int]*captureFrameSubscription),
		deviceName: deviceName,
	}
	c.open = c.openMalgoDevice
	c.closeDevice = c.closeMalgoDevice
	return c
}

func (c *Capture) logf(format string, args ...any) {
	if c.Logf == nil {
		return
	}
	c.Logf(format, args...)
}

// Start begins capturing audio from the configured input device.
func (c *Capture) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil
	}
	if err := c.openDeviceLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.running = true
	c.active = true
	c.lastData.Store(time.Now().UnixNano())

	// One waiter per Start: recovery reopens the device without going through
	// Start, so a mixer restart must not leak a goroutine — but a Start after a
	// full Stop needs its own watcher, because the previous one has exited.
	var done chan struct{}
	if c.watchDone == nil {
		done = make(chan struct{})
		c.watchDone = done
	}
	c.mu.Unlock()

	if done != nil {
		go func() {
			select {
			case <-ctx.Done():
				c.Stop()
			case <-done:
			}
		}()
	}
	// A context cancelled while the device was opening would otherwise leave the
	// new device running: the watcher above may already have observed Done.
	if err := ctx.Err(); err != nil {
		c.Stop()
		return err
	}
	return nil
}

// SetDevice rebinds a running capture to name. An empty name selects the
// operating-system default. The Capture itself, including its ring buffer and
// subscriptions, remains unchanged.
func (c *Capture) SetDevice(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if c.running && c.deviceName == name {
		return nil
	}
	if !c.active {
		return fmt.Errorf("capture is stopped")
	}

	previousName := c.deviceName
	if c.running {
		c.closeDevice()
	}
	c.running = false
	c.buf.Clear()
	c.deviceName = name

	if err := ctx.Err(); err != nil {
		return c.restoreDeviceLocked(previousName, err)
	}

	if err := c.openDeviceLocked(); err != nil {
		return c.restoreDeviceLocked(previousName, err)
	}
	if err := ctx.Err(); err != nil {
		c.closeDevice()
		c.running = false
		return c.restoreDeviceLocked(previousName, err)
	}

	c.running = true
	c.lastData.Store(time.Now().UnixNano())
	return nil
}

// restoreDeviceLocked restores the device that was active before SetDevice.
// c.mu must be held. On rollback failure, the Capture remains active but
// closed so normal recovery can retry the previous device.
func (c *Capture) restoreDeviceLocked(name string, original error) error {
	c.deviceName = name
	if err := c.openDeviceLocked(); err != nil {
		c.running = false
		return errors.Join(original, fmt.Errorf("restore capture device %q: %w", deviceLabel(name), err))
	}
	c.running = true
	c.lastData.Store(time.Now().UnixNano())
	return original
}

// openDeviceLocked advances the physical device generation before Start can
// invoke a callback. The caller has closed/joined the previous device and holds
// c.mu. Failed opens may consume a generation; positions never imply that the
// unknown interval between devices contained silence.
func (c *Capture) openDeviceLocked() error {
	c.deviceGeneration.Add(1)
	return c.open()
}

func (c *Capture) openMalgoDevice() error {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("init audio context: %w", err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = Channels
	deviceConfig.SampleRate = SampleRate
	releaseDeviceID, err := selectDevice(mctx.Context, malgo.Capture, c.deviceName, &deviceConfig.Capture)
	if err != nil {
		_ = mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("select capture device: %w", err)
	}
	defer releaseDeviceID()

	generation := c.deviceGeneration.Load()
	onData := func(outputSamples, inputSamples []byte, frameCount uint32) {
		// Observe arrival before conversion, frontend work or ring locking.
		// malgo supplies no ADC timestamp; retain this limitation explicitly.
		callbackAt := time.Now()
		c.lastData.Store(callbackAt.UnixNano())
		c.ingestCapture(bytesToFloat32(inputSamples), callbackAt, generation)
	}

	device, err := malgo.InitDevice(mctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: onData,
	})
	if err != nil {
		mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("init capture device: %w", err)
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		mctx.Uninit()
		mctx.Free()
		return fmt.Errorf("start capture: %w", err)
	}

	c.ctx = mctx
	c.device = device
	// selectDevice requires an exact enumerated name and opens its DeviceID;
	// it never falls back for an explicit name. The OS default remains unknown.
	c.actualDeviceName = c.deviceName
	c.deviceRate = int(device.SampleRate())
	c.deviceChannels = int(device.CaptureChannels())
	return nil
}

// ingestCapture is shared by the real callback and deterministic ordering
// tests. Arrival and device identity are supplied by the callback, never by a
// later ring reader. Both subscriptions retain the original processed callback
// PCM; frame subscriptions additionally preserve the ring source position.
func (c *Capture) ingestCapture(samples []float32, callbackAt time.Time, generation uint64) {
	c.ingestMu.Lock()
	defer c.ingestMu.Unlock()
	c.ingested.Add(uint64(len(samples)))
	if c.raw != nil {
		c.raw.writeCapture(slices.Clone(samples), callbackAt, generation)
	}
	if c.frontend != nil {
		samples = c.frontend.ProcessCapture(samples)
	}
	if c.preserve != nil {
		residual := samples
		if r, ok := c.frontend.(ResidualReporter); ok {
			if res := r.LastResidual(); len(res) == len(samples) {
				residual = res
			}
		}
		c.preserve.Write(slices.Clone(residual))
	}
	span := c.buf.writeCapture(samples, callbackAt, generation)
	c.publish(samples)
	if len(samples) > 0 {
		c.callbackSeq++
		c.publishFrame(samples, span, c.callbackSeq)
	}
}

// Stop stops audio capture and releases resources. It is the caller's "I am
// done": recovery will not reopen the device afterwards.
func (c *Capture) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.active = false
	if c.watchDone != nil {
		close(c.watchDone)
		c.watchDone = nil
	}
	if !c.running {
		return
	}
	c.closeDevice()
	c.running = false
}

func (c *Capture) closeMalgoDevice() {
	if c.device != nil {
		c.device.Uninit()
		c.device = nil
	}
	if c.ctx != nil {
		c.ctx.Uninit()
		c.ctx.Free()
		c.ctx = nil
	}
}

// recoverIfStalled reopens the capture device when callbacks have gone missing
// for captureStallTimeout (a mixer volume change often kills the stream). A
// Capture the caller stopped is left alone.
//
// A failed reopen is not terminal: the device stays closed but the Capture
// stays active, the stall stamp is refreshed, and the next stall window tries
// again. The likely causes — a PipeWire graph restart, a USB mic unplugged —
// resolve on their own, and a recorder that gave up after one attempt showed
// "listening" against a dead microphone indefinitely.
func (c *Capture) recoverIfStalled(ctx context.Context) error {
	c.mu.Lock()
	if !c.active || !c.stalled() {
		c.mu.Unlock()
		return nil
	}
	c.lastData.Store(time.Now().UnixNano())
	if c.running {
		c.closeDevice()
		c.running = false
	}
	device := c.deviceName
	err := c.openDeviceLocked()
	if err == nil {
		c.running = true
		c.lastData.Store(time.Now().UnixNano())
	}
	c.mu.Unlock()

	if err != nil {
		c.logf("obey-voice: capture from %q stalled for %s; reopen failed, retrying in %s: %v",
			deviceLabel(device), captureStallTimeout, captureStallTimeout, err)
		return err
	}
	c.logf("obey-voice: capture from %q stalled for %s; device reopened", deviceLabel(device), captureStallTimeout)

	// Cancellation between the close and the reopen would otherwise leave the
	// new device running: the watcher fired while no device was open.
	if err := ctx.Err(); err != nil {
		c.Stop()
		return err
	}
	return nil
}

func deviceLabel(name string) string {
	if name == "" {
		return "system default"
	}
	return name
}

func (c *Capture) stalled() bool {
	ns := c.lastData.Load()
	if ns == 0 {
		return false
	}
	return time.Since(time.Unix(0, ns)) >= captureStallTimeout
}

// Read returns the next chunk of audio samples.
// Returns nil if no data is available yet.
func (c *Capture) Read() []float32 {
	return c.buf.Read(ChunkSize)
}

// ReadFrame implements FrameSource for live capture: it returns the next chunk
// as a SourceLive frame, ErrNoFrameReady when no audio is buffered yet, and
// never sets Final. A live source ends only when ctx is canceled or Close is
// called, never on ordinary silence.
func (c *Capture) ReadFrame(ctx context.Context) (Frame, error) {
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}
	samples, spans, gap := c.buf.readCapture(ChunkSize)
	c.discardRaw(len(samples))
	if samples == nil {
		// A failed reopen is reported by recoverIfStalled and retried on the
		// next stall window, so it must not end the STT session: the loop keeps
		// polling and picks the microphone back up when it returns.
		_ = c.recoverIfStalled(ctx)
		return Frame{}, ErrNoFrameReady
	}
	var startedAt time.Time
	if len(spans) > 0 {
		startedAt = spans[0].EstimatedStart
	}
	return Frame{
		Samples:      samples,
		SampleRate:   SampleRate,
		Channels:     Channels,
		Duration:     SamplesDuration(len(samples)),
		Sequence:     c.frameSeq.Add(1),
		StartedAt:    startedAt,
		SourceKind:   SourceLive,
		CaptureSpans: spans,
		CaptureGap:   gap,
	}, nil
}

// Close implements FrameSource by stopping capture and releasing the device.
func (c *Capture) Close() error {
	c.Stop()
	return nil
}

// Reset drains the ring buffer to discard stale audio between turns.
func (c *Capture) Reset() {
	// Read returns nil when fewer than ChunkSize samples remain, so repeatedly
	// reading whole frames leaves a partial tail in the ring. Clear the ring
	// atomically instead; a stale tail must not become the first frame of the
	// next listening scope.
	c.buf.Clear()
	if c.raw != nil {
		c.raw.Clear()
	}
	if c.preserve != nil {
		c.preserve.Clear()
	}
}

// ResetKeeping discards stale audio while retaining at most the newest
// duration of the current capture. It is used only for a listen handoff where
// the retained tail may contain the onset of the next utterance.
func (c *Capture) ResetKeeping(duration time.Duration) {
	if duration <= 0 {
		c.buf.Clear()
		if c.raw != nil {
			c.raw.Clear()
		}
		if c.preserve != nil {
			c.preserve.Clear()
		}
		return
	}
	samples := int(duration * SampleRate / time.Second)
	c.buf.KeepLast(samples)
	if c.raw != nil {
		c.raw.KeepLast(samples)
	}
	if c.preserve != nil {
		c.preserve.KeepLast(samples)
	}
}

// ReplaceUnreadWithRaw swaps unread processed capture for pre-frontend PCM.
// keep bounds the retained tail so thinking-phase audio is not transcribed as
// the interrupting phrase. keep<=0 retains the aligned unread tail.
func (c *Capture) ReplaceUnreadWithRaw(keep time.Duration) {
	if c == nil || c.raw == nil || c.buf == nil {
		return
	}
	c.ingestMu.Lock()
	defer c.ingestMu.Unlock()
	raw := c.raw.snapshotUnread()
	if len(raw) == 0 {
		return
	}
	n := len(raw)
	if keep > 0 {
		n = min(n, int(keep*SampleRate/time.Second))
	} else {
		n = min(n, c.buf.Available())
	}
	if n <= 0 {
		return
	}
	c.buf.Clear()
	c.buf.Write(raw[len(raw)-n:])
}

// SnapshotStartupWindows copies the bounded unread raw tail and the cropped
// NLMS NearEnd residual. Residual is nil when no NearEnd hop exists.
func (c *Capture) SnapshotStartupWindows(keep time.Duration) (raw, residual []float32) {
	if c == nil {
		return nil, nil
	}
	c.ingestMu.Lock()
	defer c.ingestMu.Unlock()
	var rawAll, resAll, primAll []float32
	if c.raw != nil {
		rawAll = c.raw.snapshotUnread()
	}
	if c.preserve != nil {
		resAll = c.preserve.snapshotUnread()
	}
	if c.buf != nil {
		primAll = c.buf.snapshotUnread()
	}
	n := len(rawAll)
	if keep > 0 {
		n = min(n, int(keep*SampleRate/time.Second))
	}
	if n <= 0 {
		return nil, nil
	}
	raw = slices.Clone(rawAll[len(rawAll)-n:])
	if len(resAll) >= n && len(primAll) >= n {
		residual = CropNearEndResidual(resAll[len(resAll)-n:], primAll[len(primAll)-n:])
	}
	return raw, residual
}

// Ingested is the number of capture samples written since Start.
func (c *Capture) Ingested() uint64 {
	if c == nil {
		return 0
	}
	return c.ingested.Load()
}

// Newest copies the newest n processed samples still unread.
func (c *Capture) Newest(n int) []float32 {
	if c == nil || c.buf == nil || n <= 0 {
		return nil
	}
	c.ingestMu.Lock()
	defer c.ingestMu.Unlock()
	all := c.buf.snapshotUnread()
	if n > len(all) {
		n = len(all)
	}
	if n <= 0 {
		return nil
	}
	return slices.Clone(all[len(all)-n:])
}

// ReplaceUnread clears processed capture and writes samples as the next listen.
func (c *Capture) ReplaceUnread(samples []float32) {
	if c == nil || c.buf == nil {
		return
	}
	c.ingestMu.Lock()
	defer c.ingestMu.Unlock()
	c.buf.Clear()
	if len(samples) > 0 {
		c.buf.Write(samples)
	}
}

func (c *Capture) discardRaw(n int) {
	if c.raw == nil || n <= 0 {
		return
	}
	if c.raw.Available() >= n {
		_ = c.raw.Read(n)
		return
	}
	c.raw.Clear()
}

// SetFrontend installs an audio front-end for live capture processing.
func (c *Capture) SetFrontend(frontend Frontend) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frontend = frontend
}

// InputGain implements GainReporter by reporting the gain the installed
// front-end has already applied to the frames this source hands out, so an
// energy threshold downstream can be stated in microphone units. 1 when no
// front-end is installed or it does not amplify.
func (c *Capture) InputGain() float64 {
	c.mu.Lock()
	frontend := c.frontend
	c.mu.Unlock()
	if g, ok := frontend.(GainReporter); ok {
		return g.InputGain()
	}
	return 1
}

// NearEnd reports the installed front-end's near-end preserve flag.
func (c *Capture) NearEnd() bool {
	c.mu.Lock()
	frontend := c.frontend
	c.mu.Unlock()
	if n, ok := frontend.(NearEndReporter); ok {
		return n.NearEnd()
	}
	return false
}

// Subscribe registers a non-blocking listener for live capture chunks.
//
// Read-only contract: the []float32 delivered on the returned channel is shared
// — publish fans the SAME backing slice to every subscriber for a given frame.
// Consumers must treat it as read-only; a subscriber that mutates it in place
// corrupts the frame every other subscriber observes. (The ring buffer is
// unaffected: RingBuffer.Write copies element-wise into its own storage.) A
// consumer that needs to retain or mutate samples must copy first.
func (c *Capture) Subscribe(buffer int) (int, <-chan []float32) {
	if buffer <= 0 {
		buffer = 1
	}

	c.subsMu.Lock()
	defer c.subsMu.Unlock()

	id := c.nextSub
	c.nextSub++
	ch := make(chan []float32, buffer)
	c.subs[id] = ch
	return id, ch
}

type captureFrameSubscription struct {
	channel chan Frame
	gap     CaptureGap
}

// SubscribeFrames registers a non-blocking listener for original capture
// callbacks, not the fixed-size chunks returned by ReadFrame. Each frame has
// one complete CaptureSpan assigned by the same ring write as its PCM.
// Sequence counts nonempty callbacks in this Capture's lifetime, independently
// of ReadFrame's read sequence. Ring reads/resets do not drain this channel.
//
// Samples share backing storage with legacy and other frame subscribers. The
// one-span CaptureSpans slice is also shared across frame subscribers. Both
// must be treated as read-only; copy before modifying. Frame scalar fields and
// CaptureGap are subscriber-local values. A full channel drops the new callback
// and reports that subscriber's accumulated loss
// in CaptureGap on its next successful delivery. Unsubscribe uses the same IDs
// as Subscribe; Stop/Close preserve subscriptions just as for the legacy API.
func (c *Capture) SubscribeFrames(buffer int) (int, <-chan Frame) {
	if buffer <= 0 {
		buffer = 1
	}
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	id := c.nextSub
	c.nextSub++
	ch := make(chan Frame, buffer)
	if c.frameSubs == nil {
		c.frameSubs = make(map[int]*captureFrameSubscription)
	}
	c.frameSubs[id] = &captureFrameSubscription{channel: ch}
	return id, ch
}

func (c *Capture) publishFrame(samples []float32, span CaptureSpan, sequence int64) {
	// The write lock protects subscriber-local loss accounting as well as sends
	// against Unsubscribe. All sends are nonblocking.
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	if len(c.frameSubs) == 0 {
		return
	}
	frame := Frame{Samples: samples, SampleRate: SampleRate, Channels: Channels, StartedAt: span.EstimatedStart, Duration: SamplesDuration(len(samples)), Sequence: sequence, SourceKind: SourceLive, CaptureSpans: []CaptureSpan{span}}
	for _, sub := range c.frameSubs {
		frame.CaptureGap = sub.gap
		select {
		case sub.channel <- frame:
			sub.gap = CaptureGap{}
		default:
			sub.gap.SubscriptionDroppedSamples += uint64(len(samples))
			sub.gap.SubscriptionDroppedCallbacks++
			sub.gap.SubscriptionDeviceDiscontinuity = sub.gap.SubscriptionDeviceDiscontinuity || span.DeviceDiscontinuity
		}
	}
}

// Unsubscribe removes either kind of capture listener.
func (c *Capture) Unsubscribe(id int) {
	// Close while holding the write lock: publish sends under the read lock, so
	// the close cannot interleave with a send. Closing outside the lock let the
	// malgo callback thread send on a just-closed channel and panic (a send on a
	// closed channel counts as ready in a select — default does not protect).
	c.subsMu.Lock()
	defer c.subsMu.Unlock()

	if ch, ok := c.subs[id]; ok {
		delete(c.subs, id)
		close(ch)
	}
	if sub, ok := c.frameSubs[id]; ok {
		delete(c.frameSubs, id)
		close(sub.channel)
	}
}

// IsRunning returns whether capture is active.
func (c *Capture) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *Capture) publish(samples []float32) {
	// Every subscriber receives the SAME samples slice (no per-subscriber copy):
	// the read-only contract on Subscribe is what keeps that safe. Copying here
	// would add an allocation per subscriber on the real-time malgo callback
	// thread, so the contract is documented rather than enforced.
	//
	// Hold the read lock across the sends. They are non-blocking (buffered +
	// default), so the callback thread never stalls, and Unsubscribe's
	// delete+close (under the write lock) cannot race a send.
	c.subsMu.RLock()
	defer c.subsMu.RUnlock()

	for _, ch := range c.subs {
		select {
		case ch <- samples:
		default:
		}
	}
}

// bytesToFloat32 converts S16LE audio bytes to float32 samples in [-1.0, 1.0].
func bytesToFloat32(data []byte) []float32 {
	samples := make([]float32, len(data)/2)
	for i := range samples {
		s := int16(binary.LittleEndian.Uint16(data[i*2:]))
		samples[i] = float32(s) / float32(math.MaxInt16)
	}
	return samples
}
