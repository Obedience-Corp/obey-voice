package playback

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type debugAudioEvent struct {
	format     *debugAudioMetadata
	callback   *[]byte
	sourceID   uint64
	source     []float32
	sampleRate int
	elapsed    time.Duration
	requested  int
	written    int
	sequence   uint64
}

const diagnosticDuration = time.Minute
const diagnosticMaxBytes uint64 = 32 << 20
const diagnosticRetention = 24 * time.Hour

type playerDebugRecorder struct {
	bytes            atomic.Uint64
	limitSignaled    atomic.Bool
	writeErrors      atomic.Uint64
	writeFailureOnce sync.Once
	limitReason      string
	timer            *time.Timer
	dir              string
	started          time.Time
	events           chan debugAudioEvent
	pool             sync.Pool
	mu               sync.RWMutex
	closed           bool
	closeOnce        sync.Once
	wg               sync.WaitGroup
	sequence         atomic.Uint64
	sourceSequence   atomic.Uint64
	dropped          atomic.Uint64
	// aecRefDrops is far-end blocks the playback callback could not hand to
	// the AEC worker. Non-zero means the canceller ran on a gap-filled
	// reference, which is the first thing to check when a debug-audio capture
	// shows self-barge that the ERLE tests do not reproduce.
	aecRefDrops atomic.Uint64
}

type debugAudioMetadata struct {
	CreatedAt           time.Time `json:"created_at"`
	DeviceName          string    `json:"device_name"`
	RequestedSampleRate int       `json:"requested_sample_rate"`
	DeviceSampleRate    int       `json:"device_sample_rate"`
	Channels            int       `json:"channels"`
	Format              string    `json:"format"`
	PID                 int       `json:"pid"`
}

type debugCallbackMetadata struct {
	Sequence        uint64 `json:"sequence"`
	ElapsedMicros   int64  `json:"elapsed_micros"`
	RequestedFrames int    `json:"requested_frames"`
	WrittenFrames   int    `json:"written_frames"`
	SilenceFrames   int    `json:"silence_frames"`
}

func newPlayerDebugRecorder(root, deviceName string, requestedRate, deviceRate, deviceChannels int) (*playerDebugRecorder, error) {
	pruneDiagnosticCaptures(root)
	stamp := time.Now().Format("20060102T150405.000000000")
	dir := filepath.Join(root, fmt.Sprintf("playback-%s-pid%d", stamp, os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create player debug directory: %w", err)
	}
	metadataChannels := deviceChannels
	if deviceChannels <= 0 {
		deviceChannels = playbackChannels
	}
	metadata := debugAudioMetadata{
		CreatedAt:           time.Now(),
		DeviceName:          deviceName,
		RequestedSampleRate: requestedRate,
		DeviceSampleRate:    deviceRate,
		Channels:            metadataChannels,
		Format:              "pcm_s16le",
		PID:                 os.Getpid(),
	}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), append(encoded, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write audio debug metadata: %w", err)
	}

	r := &playerDebugRecorder{
		dir:     dir,
		started: time.Now(),
		events:  make(chan debugAudioEvent, 512),
	}
	r.bytes.Store(4096) // reserve metadata, summary, and initial WAV header
	r.pool.New = func() any {
		buf := make([]byte, 0, 4096)
		return &buf
	}
	r.wg.Add(1)
	r.timer = time.AfterFunc(diagnosticDuration, func() { r.finish("duration_limit") })
	go r.run(deviceRate, deviceChannels)
	time.AfterFunc(diagnosticRetention, func() { r.close(); _ = os.RemoveAll(dir) })
	return r, nil
}

func (r *playerDebugRecorder) newSourceID() uint64 {
	if r == nil {
		return 0
	}
	return r.sourceSequence.Add(1)
}

func (r *playerDebugRecorder) captureSource(sampleRate int, samples []float32) {
	if r == nil {
		return
	}
	r.captureSourceChunk(r.newSourceID(), sampleRate, samples)
}

// captureSourceChunk queues source PCM immediately, preserving the prefix when
// capture stops before synthesis finishes. A stable ID joins one segment's
// chunks into one WAV. Producers never perform file I/O or wait for the writer.
func (r *playerDebugRecorder) captureSourceChunk(id uint64, sampleRate int, samples []float32) {
	if r == nil || id == 0 || len(samples) == 0 {
		return
	}
	if !r.reserve(uint64(len(samples))*4 + 256) {
		return
	}
	r.enqueue(debugAudioEvent{sourceID: id, source: append([]float32(nil), samples...), sampleRate: sampleRate, sequence: r.sequence.Add(1)})
}

// captureCallback copies the exact pre-silenced buffer returned to miniaudio.
// It never performs file I/O on the real-time callback thread. If the debug
// writer cannot keep up, the event is dropped and counted in summary.json.
func (r *playerDebugRecorder) captureCallback(output []byte, requested, written int) {
	if r == nil {
		return
	}
	if !r.reserve(uint64(len(output)) + 256) {
		return
	}
	pooled := r.pool.Get().(*[]byte)
	if cap(*pooled) < len(output) {
		*pooled = make([]byte, len(output))
	} else {
		*pooled = (*pooled)[:len(output)]
	}
	copy(*pooled, output)
	event := debugAudioEvent{
		callback:  pooled,
		elapsed:   time.Since(r.started),
		requested: requested,
		written:   written,
		sequence:  r.sequence.Add(1),
	}
	if !r.enqueue(event) {
		*pooled = (*pooled)[:0]
		r.pool.Put(pooled)
	}
}

func (r *playerDebugRecorder) enqueue(event debugAudioEvent) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return false
	}
	select {
	case r.events <- event:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

// setAECReferenceDrops records the ingress drop count for summary.json. Called
// on teardown, before close writes the summary.
func (r *playerDebugRecorder) setAECReferenceDrops(n uint64) {
	if r == nil {
		return
	}
	r.aecRefDrops.Store(n)
}

func (r *playerDebugRecorder) close() { r.finish("stopped") }

func (r *playerDebugRecorder) finish(reason string) {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.limitReason = reason
		if r.timer != nil {
			r.timer.Stop()
		}
		close(r.events)
		r.mu.Unlock()
		r.wg.Wait()
	})
}

func (r *playerDebugRecorder) run(deviceRate, deviceChannels int) {
	defer r.wg.Done()
	if deviceChannels <= 0 {
		deviceChannels = playbackChannels
	}
	wav, wavErr := newPCM16WAVWriter(filepath.Join(r.dir, "device-output.wav"), deviceRate, deviceChannels)
	callbacks, callbackErr := os.OpenFile(filepath.Join(r.dir, "callbacks.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if wavErr != nil {
		r.recordWriteFailure()
	}
	if callbackErr != nil {
		r.recordWriteFailure()
	}
	var callbackWriter *bufio.Writer
	if callbackErr == nil {
		callbackWriter = bufio.NewWriter(callbacks)
	}

	var sourceIndex int
	for event := range r.events {
		switch {
		case event.format != nil:
			if wav != nil {
				wav.sampleRate = event.format.DeviceSampleRate
				wav.channels = event.format.Channels
			}
			encoded, _ := json.MarshalIndent(event.format, "", "  ")
			if err := os.WriteFile(filepath.Join(r.dir, "metadata.json"), append(encoded, '\n'), 0600); err != nil {
				r.recordWriteFailure()
			}
		case len(event.source) > 0:
			path := filepath.Join(r.dir, fmt.Sprintf("source-%04d-%dhz.wav", event.sourceID, event.sampleRate))
			created, err := appendSourceWAV(path, event.sampleRate, event.source)
			if created {
				sourceIndex++
			}
			if err != nil {
				r.recordWriteFailure()
			}
		case event.callback != nil && len(*event.callback) > 0:
			if wavErr == nil {
				if err := wav.Write(*event.callback); err != nil {
					r.recordWriteFailure()
				}
			}
			if callbackWriter != nil {
				meta := debugCallbackMetadata{
					Sequence:        event.sequence,
					ElapsedMicros:   event.elapsed.Microseconds(),
					RequestedFrames: event.requested,
					WrittenFrames:   event.written,
					SilenceFrames:   event.requested - event.written,
				}
				encoded, _ := json.Marshal(meta)
				if _, err := callbackWriter.Write(append(encoded, '\n')); err != nil {
					r.recordWriteFailure()
				}
			}
			*event.callback = (*event.callback)[:0]
			r.pool.Put(event.callback)
		}
	}
	if wavErr == nil {
		if err := wav.Close(); err != nil {
			r.recordWriteFailure()
		}
	}
	if callbackWriter != nil {
		if err := callbackWriter.Flush(); err != nil {
			r.recordWriteFailure()
		}
	}
	if callbacks != nil {
		if err := callbacks.Close(); err != nil {
			r.recordWriteFailure()
		}
	}
	summary, _ := json.MarshalIndent(map[string]any{
		"dropped_debug_events":         r.dropped.Load(),
		"dropped_aec_reference_blocks": r.aecRefDrops.Load(),
		"source_segments":              sourceIndex,
		"captured_bytes_budget":        r.bytes.Load(),
		"write_errors":                 r.writeErrors.Load(),
		"limit_reason":                 r.limitReason,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(r.dir, "summary.json"), append(summary, '\n'), 0600); err != nil {
		r.recordWriteFailure()
	}
}

type pcm16WAVWriter struct {
	file       *os.File
	sampleRate int
	channels   int
	dataBytes  uint32
	closed     bool
}

func newPCM16WAVWriter(path string, sampleRate, channels int) (*pcm16WAVWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	w := &pcm16WAVWriter{file: file, sampleRate: sampleRate, channels: channels}
	if err := w.writeHeader(0); err != nil {
		file.Close()
		return nil, err
	}
	return w, nil
}

func (w *pcm16WAVWriter) Write(p []byte) error {
	if w == nil || w.closed || len(p) == 0 {
		return nil
	}
	n, err := w.file.Write(p)
	w.dataBytes += uint32(n)
	return err
}

func (w *pcm16WAVWriter) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	if _, err := w.file.Seek(0, 0); err != nil {
		_ = w.file.Close()
		return err
	}
	if err := w.writeHeader(w.dataBytes); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}

func (w *pcm16WAVWriter) writeHeader(dataBytes uint32) error {
	byteRate := uint32(w.sampleRate * w.channels * 2)
	blockAlign := uint16(w.channels * 2)
	values := []any{
		[]byte("RIFF"), uint32(36) + dataBytes, []byte("WAVE"),
		[]byte("fmt "), uint32(16), uint16(1), uint16(w.channels),
		uint32(w.sampleRate), byteRate, blockAlign, uint16(16),
		[]byte("data"), dataBytes,
	}
	for _, value := range values {
		if err := binary.Write(w.file, binary.LittleEndian, value); err != nil {
			return err
		}
	}
	return nil
}

// reserve bounds memory copies and disk payload before touching the input. The
// allowance includes per-event WAV/JSON overhead; exhaustion closes off-thread.
func (r *playerDebugRecorder) reserve(n uint64) bool {
	r.mu.RLock()
	closed := r.closed
	r.mu.RUnlock()
	if closed {
		return false
	}
	for {
		used := r.bytes.Load()
		if n > diagnosticMaxBytes || used > diagnosticMaxBytes-n {
			r.dropped.Add(1)
			if r.limitSignaled.CompareAndSwap(false, true) {
				go r.finish("byte_limit")
			}
			return false
		}
		if r.bytes.CompareAndSwap(used, used+n) {
			return true
		}
	}
}

func (r *playerDebugRecorder) status() audio.DiagnosticCaptureStatus {
	if r == nil {
		return audio.DiagnosticCaptureStatus{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return audio.DiagnosticCaptureStatus{Active: !r.closed, Directory: r.dir, StartedAt: r.started, ExpiresAt: r.started.Add(diagnosticDuration), Bytes: r.bytes.Load(), DroppedEvents: r.dropped.Load(), LimitReason: r.limitReason}
}

func pruneDiagnosticCaptures(root string) {
	entries, _ := os.ReadDir(root)
	type capture struct {
		path     string
		modified time.Time
	}
	var captures []capture
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "playback-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if time.Since(info.ModTime()) >= diagnosticRetention {
			_ = os.RemoveAll(path)
			continue
		}
		captures = append(captures, capture{path, info.ModTime()})
	}
	sort.Slice(captures, func(i, j int) bool { return captures[i].modified.After(captures[j].modified) })
	for i := 2; i < len(captures); i++ {
		_ = os.RemoveAll(captures[i].path)
	}
}

// configureFormat is queued before native Start when capture was opted into
// before lazy device initialization. Both metadata and the finalized WAV header
// then describe the actual negotiated callback PCM rather than a guessed rate.
func (r *playerDebugRecorder) configureFormat(name string, sourceRate, rate, channels int) {
	if r == nil {
		return
	}
	meta := &debugAudioMetadata{CreatedAt: r.started, DeviceName: name, RequestedSampleRate: sourceRate, DeviceSampleRate: rate, Channels: channels, Format: "pcm_s16le", PID: os.Getpid()}
	if !r.enqueue(debugAudioEvent{format: meta}) && r.status().Active {
		r.finish("format_update_failed")
	}
}

// Opening only for a chunk bounds open file descriptors even with many queued
// segments. Close finalizes the header on every chunk, including interrupted
// captures; all of this runs exclusively on the recorder worker.
func appendSourceWAV(path string, rate int, samples []float32) (bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return false, err
	}
	created := info.Size() == 0
	w := &pcm16WAVWriter{file: file, sampleRate: rate, channels: 1}
	if created {
		if err := w.writeHeader(0); err != nil {
			file.Close()
			return false, err
		}
	} else {
		if info.Size() < 44 {
			file.Close()
			return false, fmt.Errorf("source WAV header incomplete")
		}
		w.dataBytes = uint32(info.Size() - 44)
		if _, err := file.Seek(0, 2); err != nil {
			file.Close()
			return false, err
		}
	}
	pcm := float32ToPCM16(samples)
	encoded := make([]byte, len(pcm)*2)
	for i, sample := range pcm {
		binary.LittleEndian.PutUint16(encoded[i*2:], uint16(sample))
	}
	writeErr := w.Write(encoded)
	closeErr := w.Close()
	if writeErr != nil {
		return created, writeErr
	}
	return created, closeErr
}

// Called only on the writer worker. Teardown runs separately so waiting for
// that worker cannot deadlock itself, and no file operation reaches callbacks.
func (r *playerDebugRecorder) recordWriteFailure() {
	r.writeErrors.Add(1)
	r.dropped.Add(1)
	r.writeFailureOnce.Do(func() { go r.finish("write_error") })
}
