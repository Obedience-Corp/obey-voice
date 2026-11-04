package netapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

type audioGenerationContextKey struct{}

func withAudioGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, audioGenerationContextKey{}, generation)
}

func audioGenerationFromContext(ctx context.Context) uint64 {
	if ctx == nil {
		return 0
	}
	generation, _ := ctx.Value(audioGenerationContextKey{}).(uint64)
	return generation
}

// Audio format constants for the WebSocket wire protocol. PCM s16le mono is
// the smallest common denominator a phone browser or ffplay can play without
// a demuxer.
const (
	audioWireFormat   = "pcm_s16le"
	audioWireChannels = 1
	// maxAudioChunkFrames caps one envelope so a single base64 payload stays
	// small on slow mobile links (~32 KiB raw ≈ ~43 KiB base64).
	maxAudioChunkFrames = 16 * 1024
)

// AudioFanout is an audio.Engine that tees TTS PCM to:
//  1. an optional local speaker (real Player, or nil to mute the host), and
//  2. any WebSocket clients that opted into audio_output mode "stream".
//
// It is the Phase 3 seam: pipeline.Player stays an Engine plus SinkReporter.
//
// Local engine ownership: when ownLocal is true, Close() closes the local
// engine. When false, the caller (typically buildPipeline's cleanup) owns it.
type AudioFanout struct {
	local    audio.Engine
	ownLocal bool
	hub      *hub

	// segmentID is a monotonic counter so clients can group chunks that
	// belong to one PlayStream call.
	segmentID atomic.Uint64

	// accepted is how many stream-client sends succeeded this PlayStream.
	// Pipeline reads it for TTFAElapsed only; Started() stays pcmReady.
	accepted atomic.Int32
}

var _ audio.SinkReporter = (*AudioFanout)(nil)

// NewAudioFanout builds a fanout that does not own local (caller closes it).
// local may be nil (host speaker muted).
func NewAudioFanout(local audio.Engine) *AudioFanout {
	return &AudioFanout{local: local, ownLocal: false}
}

// NewOwnedAudioFanout builds a fanout that closes local on Close().
func NewOwnedAudioFanout(local audio.Engine) *AudioFanout {
	return &AudioFanout{local: local, ownLocal: true}
}

// AttachHub wires the server's connection hub. Safe to call once before serve.
func (a *AudioFanout) AttachHub(h *hub) {
	a.hub = h
}

// SinkKind reports who can hear this fanout right now. Local speaker wins
// when present; otherwise any opted-in stream client; otherwise none.
func (a *AudioFanout) SinkKind() string {
	if a == nil {
		return audio.SinkNone
	}
	if a.local != nil {
		return audio.SinkLocalSpeaker
	}
	if a.hub != nil && a.hub.hasStreamClients() {
		return audio.SinkStreamClient
	}
	return audio.SinkNone
}

// AcceptedCount is the number of stream-client sends that succeeded for the
// in-flight PlayStream. Zero means every chunk dropped or nobody opted in.
func (a *AudioFanout) AcceptedCount() int {
	if a == nil {
		return 0
	}
	return int(a.accepted.Load())
}

// PlayStream drains the TTS stream, pushes wire chunks to stream clients, and
// optionally forwards a teed copy to the local speaker. Matches the Engine
// contract: returns only after the first frames are ready (or the stream fails).
func (a *AudioFanout) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if stream == nil {
		return nil, errors.New("nil pcm stream")
	}

	a.accepted.Store(0)

	// Resolve the admitted response before waiting for PCM readiness. The
	// stream can be delayed while a selection changes; that must not make its
	// later chunks inherit the current selection. The hub generation is the
	// matching reset/resume boundary for this PlayStream call.
	identity := events.ResponseFromContext(ctx)
	// Dispatcher-admitted turns carry an immutable boundary token. Keep the
	// hub snapshot fallback for direct AudioFanout users and narrow tests; the
	// serve path always supplies the token before the runner can synthesize.
	generation := audioGenerationFromContext(ctx)
	if generation == 0 && a.hub != nil {
		generation = a.hub.audioGenerationSnapshot()
	}

	sampleRate, err := stream.WaitReady(ctx)
	if err != nil {
		return nil, err
	}

	segID := a.segmentID.Add(1)

	// Optional local speaker: start PlayStream in its own goroutine up front
	// so its initial-buffer wait cannot block the TTS drain loop.
	var localStream *audio.PCMStream
	var localReady <-chan localStart
	if a.local != nil {
		// Derive, never NewPCMStream: the local speaker is handed a different
		// object from the one the synthesizer produced, so everything that says
		// *how* this audio must be played has to travel with it. Copying the
		// sample rate by hand — which this used to do — silently dropped both
		// the Stage B incremental flag and the stall budget, leaving chunk
		// playback and stall-to-fallback inert under `serve` (the Mac app path)
		// while every other path had them.
		localStream = stream.Derive(ctx)
		ch := make(chan localStart, 1)
		localReady = ch
		go func() {
			pb, err := a.local.PlayStream(ctx, localStream)
			ch <- localStart{playback: pb, err: err}
		}()
	}

	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	seg := &fanoutSegment{started: started, done: done}

	go a.pump(ctx, stream, localStream, localReady, sampleRate, segID, generation, identity, seg)

	playback := audio.NewPlayback(started, done)
	didStart, terminal := playback.WaitStart(ctx.Done())
	if !didStart {
		if terminal == nil {
			return nil, ctx.Err()
		}
		if terminal.Err != nil {
			return nil, terminal.Err
		}
		if terminal.Interrupted {
			return nil, context.Canceled
		}
		return nil, errors.New("pcm stream produced no samples")
	}
	if terminal != nil {
		// WaitStart observed completion while checking a simultaneous start. Keep
		// that exact terminal receipt available to the returned handle's owner.
		receipt := make(chan audio.PlaybackResult, 1)
		receipt <- *terminal
		close(receipt)
		playback = audio.NewPlayback(started, receipt)
	}
	return playback, nil
}

type localStart struct {
	playback *audio.Playback
	err      error
}

// pump is linear: drain TTS → optional wire encode → optional local Write →
// close local → wait local Done → finish. Wire fanout is a pure side-effect.
func (a *AudioFanout) pump(
	ctx context.Context,
	src *audio.PCMStream,
	local *audio.PCMStream,
	localReady <-chan localStart,
	sampleRate int,
	segID uint64,
	generation uint64,
	identity events.ResponseEnvelope,
	seg *fanoutSegment,
) {
	var (
		hadAudio      bool
		localPlayback *audio.Playback
		localFailed   bool
	)

	// Resolve local start without blocking the drain (poll) or at end (block).
	pollLocal := func() {
		if localReady == nil || localPlayback != nil || localFailed {
			return
		}
		select {
		case start := <-localReady:
			if start.err != nil {
				localFailed = true
				return
			}
			localPlayback = start.playback
		default:
		}
	}
	awaitLocalResult := func(fallback audio.PlaybackResult) audio.PlaybackResult {
		pollLocal()
		if localPlayback == nil && localReady != nil && !localFailed {
			select {
			case start := <-localReady:
				if start.err != nil {
					return fallback
				}
				localPlayback = start.playback
			case <-ctx.Done():
				return audio.PlaybackResult{Interrupted: true, Err: ctx.Err()}
			}
		}
		if localPlayback == nil {
			return fallback
		}
		select {
		case r := <-localPlayback.Done():
			return r
		case <-ctx.Done():
			return audio.PlaybackResult{Interrupted: true, Err: ctx.Err()}
		}
	}

	attempt := audio.PlaybackAttemptFromContext(ctx)
	finish := func(result audio.PlaybackResult) {
		result = attempt.Result(result)
		if !attempt.Valid() {
			result.Interrupted = true
		}
		if hadAudio {
			a.emitAudioEnd(segID, generation, identity, result)
		}
		seg.finish(result, hadAudio)
	}

	for {
		pollLocal()
		select {
		case <-ctx.Done():
			if local != nil {
				local.CloseWithError(ctx.Err())
			}
			finish(awaitLocalResult(audio.PlaybackResult{Interrupted: true, Err: ctx.Err()}))
			return

		case frames, ok := <-src.Frames():
			if !ok {
				if local != nil {
					local.CloseWithError(src.Err())
				}
				finish(awaitLocalResult(audio.PlaybackResult{Err: src.Err()}))
				return
			}
			if len(frames) == 0 {
				continue
			}

			// Wire side-effect: skip all encode work when no stream clients.
			if !attempt.Write(func() { a.emitAudioChunks(segID, sampleRate, generation, identity, frames) }) {
				if local != nil {
					local.CloseWithError(context.Canceled)
				}
				finish(audio.PlaybackResult{Interrupted: true, Err: context.Canceled})
				return
			}

			if local != nil && !localFailed {
				if err := local.Write(frames); err != nil && ctx.Err() == nil {
					local.CloseWithError(err)
					local = nil
					localFailed = true
				}
			}

			if !hadAudio {
				hadAudio = true
				seg.markStarted()
			}
		}
	}
}

func (a *AudioFanout) emitAudioChunks(segID uint64, sampleRate int, generation uint64, identity events.ResponseEnvelope, frames []float32) {
	if a.hub == nil || !a.hub.hasStreamClients() {
		return
	}
	for len(frames) > 0 {
		n := len(frames)
		if n > maxAudioChunkFrames {
			n = maxAudioChunkFrames
		}
		pcm := float32ToPCM16LE(frames[:n])
		jsonMsg, err := marshalAudioChunk(segID, sampleRate, pcm, audioWireIdentity{generation: generation, envelope: identity})
		if err != nil {
			frames = frames[n:]
			continue
		}
		binMsg := marshalBinaryAudioChunk(segID, sampleRate, pcm)
		binV2Msg := marshalBinaryAudioChunkV2(segID, sampleRate, generation, identity.ResponseID, pcm)
		if accepted := a.hub.broadcastPCMAt(jsonMsg, binMsg, binV2Msg, generation); accepted > 0 {
			a.accepted.Add(int32(accepted))
		}
		frames = frames[n:]
	}
}

func (a *AudioFanout) emitAudioEnd(segID uint64, generation uint64, identity events.ResponseEnvelope, result audio.PlaybackResult) {
	if a.hub == nil || !a.hub.hasStreamClients() {
		return
	}
	reason := "complete"
	if result.Interrupted {
		reason = "interrupted"
	} else if result.Err != nil {
		reason = "error"
	}
	msg, err := marshalAudioEnd(segID, reason, audioWireIdentity{generation: generation, envelope: identity})
	if err == nil {
		a.hub.broadcastAudio(msg, generation)
	}
}

// Stop interrupts local playback when present. Remote stream clients stop
// only when the turn context is canceled (pipeline interrupt) — Stop alone
// does not tear down in-flight wire chunks.
func (a *AudioFanout) Stop() {
	if a.local != nil {
		a.local.Stop()
	}
}

// IsPlaying reports local speaker state (remote clients are not "playing" here).
func (a *AudioFanout) IsPlaying() bool {
	if a.local == nil {
		return false
	}
	return a.local.IsPlaying()
}

// Close releases the local engine only when this fanout owns it.
func (a *AudioFanout) Close() error {
	if !a.ownLocal || a.local == nil {
		return nil
	}
	return a.local.Close()
}

type fanoutSegment struct {
	started  chan struct{}
	done     chan audio.PlaybackResult
	once     sync.Once
	doneOnce sync.Once
}

func (s *fanoutSegment) markStarted() {
	s.once.Do(func() { close(s.started) })
}

// finish delivers the terminal result. When hadAudio is false, only done is
// signaled so PlayStream can surface "no samples" instead of a fake start.
func (s *fanoutSegment) finish(result audio.PlaybackResult, hadAudio bool) {
	s.doneOnce.Do(func() {
		if hadAudio {
			s.markStarted()
		}
		s.done <- result
		close(s.done)
	})
}

func float32ToPCM16LE(samples []float32) []byte {
	out := make([]byte, len(samples)*2)
	for i, sample := range samples {
		if sample > 1 {
			sample = 1
		} else if sample < -1 {
			sample = -1
		}
		v := int16(sample * float32(math.MaxInt16))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

type audioWireIdentity struct {
	generation uint64
	envelope   events.ResponseEnvelope
}

func addAudioIdentity(env map[string]any, identity []audioWireIdentity) {
	if len(identity) == 0 {
		return
	}
	env["generation"] = identity[0].generation
	if identity[0].envelope.ResponseID != "" {
		env["response_id"] = identity[0].envelope.ResponseID
		env["response_envelope"] = identity[0].envelope
	}
}

func marshalAudioChunk(segID uint64, sampleRate int, pcm []byte, identity ...audioWireIdentity) ([]byte, error) {
	env := map[string]any{
		"type":        "audio_chunk",
		"segment_id":  segID,
		"format":      audioWireFormat,
		"sample_rate": sampleRate,
		"channels":    audioWireChannels,
		"data":        base64.StdEncoding.EncodeToString(pcm),
	}
	addAudioIdentity(env, identity)
	return json.Marshal(env)
}

func marshalAudioEnd(segID uint64, reason string, identity ...audioWireIdentity) ([]byte, error) {
	env := map[string]any{
		"type":       "audio_end",
		"segment_id": segID,
		"reason":     reason,
	}
	addAudioIdentity(env, identity)
	return json.Marshal(env)
}

// Binary TTS chunk: OVA1 + sample_rate u32le + segment_id u64le + pcm_s16le.
const (
	binaryAudioMagic               = "OVA1"
	binaryAudioHeaderSize          = 16
	binaryAudioV2Magic             = "OVA2"
	binaryAudioV2HeaderSize        = 28 // magic + rate + segment + generation + id length
	audioProtocolOVA1       uint32 = 1
	audioProtocolOVA2       uint32 = 2
)

func isBinaryAudioFrame(msg []byte) bool {
	return len(msg) >= 4 && (string(msg[:4]) == binaryAudioMagic || string(msg[:4]) == binaryAudioV2Magic)
}

func marshalBinaryAudioChunk(segID uint64, sampleRate int, pcm []byte) []byte {
	out := make([]byte, binaryAudioHeaderSize+len(pcm))
	copy(out[0:4], binaryAudioMagic)
	binary.LittleEndian.PutUint32(out[4:8], uint32(sampleRate))
	binary.LittleEndian.PutUint64(out[8:16], segID)
	copy(out[16:], pcm)
	return out
}

// marshalBinaryAudioChunkV2 is the negotiated identity-carrying frame:
// OVA2 + sample_rate u32le + segment_id u64le + generation u64le +
// response_id_len u32le + response_id UTF-8 + pcm_s16le. The identity is a
// full string; it is deliberately not hashed or truncated.
func marshalBinaryAudioChunkV2(segID uint64, sampleRate int, generation uint64, responseID string, pcm []byte) []byte {
	id := []byte(responseID)
	out := make([]byte, binaryAudioV2HeaderSize+len(id)+len(pcm))
	copy(out[0:4], binaryAudioV2Magic)
	binary.LittleEndian.PutUint32(out[4:8], uint32(sampleRate))
	binary.LittleEndian.PutUint64(out[8:16], segID)
	binary.LittleEndian.PutUint64(out[16:24], generation)
	binary.LittleEndian.PutUint32(out[24:28], uint32(len(id)))
	copy(out[28:28+len(id)], id)
	copy(out[28+len(id):], pcm)
	return out
}

// ParseBinaryAudioChunk unpacks an OVA1 TTS frame. ok is false if the magic
// or length is wrong.
func ParseBinaryAudioChunk(raw []byte) (segID uint64, sampleRate int, pcm []byte, ok bool) {
	if len(raw) < binaryAudioHeaderSize || string(raw[:4]) != binaryAudioMagic {
		return 0, 0, nil, false
	}
	sampleRate = int(binary.LittleEndian.Uint32(raw[4:8]))
	segID = binary.LittleEndian.Uint64(raw[8:16])
	return segID, sampleRate, raw[16:], true
}

// ParseBinaryAudioChunkV2 unpacks an OVA2 identity-carrying frame.
func ParseBinaryAudioChunkV2(raw []byte) (segID uint64, sampleRate int, generation uint64, responseID string, pcm []byte, ok bool) {
	if len(raw) < binaryAudioV2HeaderSize || string(raw[:4]) != binaryAudioV2Magic {
		return 0, 0, 0, "", nil, false
	}
	idLen := int(binary.LittleEndian.Uint32(raw[24:28]))
	if idLen < 0 || binaryAudioV2HeaderSize+idLen > len(raw) {
		return 0, 0, 0, "", nil, false
	}
	sampleRate = int(binary.LittleEndian.Uint32(raw[4:8]))
	segID = binary.LittleEndian.Uint64(raw[8:16])
	generation = binary.LittleEndian.Uint64(raw[16:24])
	responseID = string(raw[28 : 28+idLen])
	return segID, sampleRate, generation, responseID, raw[28+idLen:], true
}
