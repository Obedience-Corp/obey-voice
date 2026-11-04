package playback

import (
	"context"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"log/slog"
	"time"

	"github.com/gen2brain/malgo"
)

// referenceDelaySamples estimates, in capture-rate samples, how long after
// onData hands frames to miniaudio those frames become audible.
//
// The estimate is deliberately low. The canceller pairs mic sample k with
// far-end sample k-D and must find the true echo at tap (Dtrue - D):
// under-estimating spends taps, which we have, while over-estimating asks the
// filter to predict the mic from reference audio that has not been pushed yet,
// which no number of taps can fix. So count only the periods certainly still
// queued ahead of this callback's frames, and let echoCancellerTaps absorb the
// rest of the ring plus driver and acoustic latency.
//
// periodFrames is what the backend actually asks for, which is not necessarily
// what the config requested — miniaudio, ALSA and CoreAudio all quantize it.
// Only a smaller observed period is adopted: a smaller ring makes the constant
// an over-estimate, the one error taps cannot absorb, while a larger one just
// deepens an under-estimate the tap budget already covers. Zero (no callback
// yet) falls back to the request.
//
// Period *count* stays an assumption: malgo exposes SampleRate and
// PlaybackChannels but not the negotiated period count, so a backend that
// silently opens fewer than playbackPeriods buffers would still be
// over-estimated here.
func referenceDelaySamples(deviceRate, periodFrames int) int {
	if deviceRate <= 0 {
		return 0
	}
	if periodFrames <= 0 || periodFrames > playbackPeriodFrames {
		periodFrames = playbackPeriodFrames
	}
	queuedFrames := (playbackPeriods - 1) * periodFrames
	return queuedFrames * audio.SampleRate / deviceRate
}

// applyReferenceDelay tells the front-end the current device's reference
// offset. Called from both sides of the pair: the front-end can be registered
// before a device exists, and the device rate is only known after negotiation.
// Also called per segment, so a backend whose real period only becomes visible
// once the callback runs corrects the offset from the next utterance on.
func (p *Player) applyReferenceDelay() {
	p.mu.Lock()
	frontend := p.frontend
	deviceRate := p.sampleRate
	p.mu.Unlock()

	delayer, ok := frontend.(audio.ReferenceDelayer)
	if !ok || deviceRate <= 0 {
		return
	}
	delayer.SetReferenceDelay(referenceDelaySamples(deviceRate, int(p.observedPeriodFrames.Load())))
}

func (p *Player) ensureDevice(sampleRate int) (int, error) {
	return p.ensureDeviceGeneration(sampleRate, nil)
}

func (p *Player) ensureDeviceGeneration(sampleRate int, epoch *uint64) (rate int, resultErr error) {
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, context.Canceled) {
			p.mu.Lock()
			if !p.closed {
				p.lifecycleState = "failed"
				p.lastError = resultErr.Error()
			}
			generation := p.generation
			p.mu.Unlock()
			slog.Warn("playback lifecycle", "event", "open_failed", "generation", generation, "error", resultErr)
		}
	}()
	p.mu.Lock()
	if epoch != nil && *epoch != p.epoch {
		p.mu.Unlock()
		return 0, context.Canceled
	}
	if p.closed {
		p.mu.Unlock()
		return 0, errors.New("audio player is closed")
	}
	if p.device != nil {
		rate := p.sampleRate
		p.mu.Unlock()
		return rate, nil
	}
	p.generation++
	generation := p.generation
	p.lifecycleState = "opening"
	p.startRecoveryWorkerLocked()
	debugRoot := p.debugRoot
	existingDebug := p.debug
	testOpen := p.testOpen
	p.mu.Unlock()

	if testOpen != nil {
		device, rate, channels, err := testOpen(sampleRate)
		if err != nil {
			return 0, err
		}
		p.mu.Lock()
		p.device = device
		p.sampleRate = rate
		p.channels = channels
		p.clientRate = sampleRate
		p.lifecycleState = "ready"
		p.openedAt = time.Now()
		p.lastError = ""
		p.mu.Unlock()
		p.lastCallback.Store(time.Now().UnixNano())
		return rate, nil
	}
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return 0, err
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceConfig.Playback.Format = malgo.FormatS16
	deviceConfig.Alsa.NoMMap = 1
	// Larger periods reduce callback pressure on multi-channel displays. They
	// also set the AEC reference offset: onData fills this buffer well before
	// the frames are audible (see applyReferenceDelay).
	deviceConfig.PeriodSizeInFrames = playbackPeriodFrames
	deviceConfig.Periods = playbackPeriods
	releaseDeviceID, err := audio.SelectPlaybackDevice(ctx.Context, p.deviceName, &deviceConfig.Playback)
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return 0, fmt.Errorf("select playback device: %w", err)
	}
	defer releaseDeviceID()
	actualDeviceName, preferredRate, nativeChannels, err := playbackDeviceDetails(ctx.Context, p.deviceName, sampleRate)
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return 0, err
	}
	// Never hard-code mono here: multi-channel devices (Studio Display Speakers
	// advertise 8ch) crackled when CoreAudio invented a mono upmix.
	channels := choosePlaybackChannels(nativeChannels)

	device, openedRate, openedChannels, err := openPlaybackDevice(ctx.Context, deviceConfig, channels, nativeChannels, sampleRate, preferredRate, func(out, in []byte, frames uint32) {
		p.mu.Lock()
		valid := p.generation == generation && !p.closed
		p.mu.Unlock()
		if !valid {
			clearBytes(out)
			return
		}
		p.observeCallback(time.Now())
		p.onData(out, in, frames)
	}, func() { p.signalRecovery(generation, "unexpected_stop") })
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return 0, err
	}

	deviceRate := int(device.SampleRate())
	if deviceRate <= 0 {
		deviceRate = openedRate
	}
	deviceChannels := int(device.PlaybackChannels())
	if deviceChannels <= 0 {
		deviceChannels = openedChannels
	}
	if existingDebug != nil {
		existingDebug.configureFormat(actualDeviceName, sampleRate, deviceRate, deviceChannels)
	}
	if err := p.startPlaybackDevice(deviceRate, deviceChannels, device.Start); err != nil {
		device.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		return 0, err
	}
	debug := existingDebug
	if debug == nil && debugRoot != "" {
		debug, err = newPlayerDebugRecorder(debugRoot, actualDeviceName, sampleRate, deviceRate, deviceChannels)
		if err != nil {
			_ = device.Stop()
			device.Uninit()
			_ = ctx.Uninit()
			ctx.Free()
			p.clearPlaybackFormat()
			return 0, fmt.Errorf("start audio debug capture: %w", err)
		}
	}

	routeID, _ := playbackRouteID(ctx.Context, p.deviceName)
	p.mu.Lock()
	p.ctx = ctx
	p.device = device
	p.routeID = routeID
	p.actualDeviceName = actualDeviceName
	p.clientRate = sampleRate
	p.clientChannels = int(channels)
	p.sampleRate = deviceRate
	p.channels = deviceChannels
	p.debug = debug
	p.lifecycleState = "ready"
	p.openedAt = time.Now()
	p.lastError = ""
	p.lastCallback.Store(time.Now().UnixNano())
	p.mu.Unlock()
	slog.Info("playback lifecycle", "event", "opened", "generation", generation, "requested_device", p.deviceName, "selected_device", actualDeviceName, "source_rate", sampleRate, "client_rate", deviceRate, "client_channels", deviceChannels, "backend_format", "unknown", "observed_period_frames", p.observedPeriodFrames.Load(), "gain", p.volumeHundredths.Load())
	// Refresh the initial offset using the period observed during device start.
	p.applyReferenceDelay()
	return deviceRate, nil
}

// startPlaybackDevice publishes callback state before starting miniaudio and
// never holds p.mu across start. Device.Start primes the playback buffer by
// invoking onData synchronously on some backends; holding p.mu here would
// deadlock that callback before Start can return. The rate must be published
// alongside channels so priming silence advances AEC at the negotiated clock,
// rather than the ingress's fallback 16kHz clock.
func (p *Player) startPlaybackDevice(deviceRate, deviceChannels int, start func() error) error {
	p.mu.Lock()
	p.sampleRate = deviceRate
	p.channels = deviceChannels
	p.mu.Unlock()
	p.applyReferenceDelay()

	if err := start(); err != nil {
		p.clearPlaybackFormat()
		return err
	}
	return nil
}

// clearPlaybackFormat rolls back a failed open after native callbacks stop.
// A retry must not inherit that device's format, observed period or AEC hint.
func (p *Player) clearPlaybackFormat() {
	p.mu.Lock()
	p.sampleRate = 0
	p.channels = 0
	frontend := p.frontend
	p.mu.Unlock()
	p.observedPeriodFrames.Store(0)
	if delayer, ok := frontend.(audio.ReferenceDelayer); ok {
		delayer.SetReferenceDelay(0)
	}
}

// openPlaybackDevice tries client layouts/rates in an order tuned for Studio
// Display Speakers and similar multi-channel CoreAudio devices:
//
//  1. stereo @ TTS rate (no Obey Voice resample; CoreAudio converts rate)
//  2. stereo @ 44.1 kHz (device clock; linear resample from 24 kHz)
//  3. stereo @ preferred/native rate from enumeration
//  4. stereo @ 48 kHz last (exact 2× path was harsher on the affected machine)
//  5. advertised multi-channel layout at the rate that worked for stereo
//
// Mono is only used when the device is truly mono. Silent mono fallback on
// multi-channel hardware is the 2026-07 crackle regression.
func openPlaybackDevice(
	ctx malgo.Context,
	base malgo.DeviceConfig,
	channels, nativeChannels uint32,
	sourceRate int,
	preferredRate uint32,
	onData func([]byte, []byte, uint32),
	onStop ...func(),
) (*malgo.Device, int, int, error) {
	rateCandidates := playbackRateCandidates(sourceRate, preferredRate)
	channelCandidates := []uint32{channels}
	if nativeChannels > 2 && nativeChannels != channels {
		channelCandidates = append(channelCandidates, nativeChannels)
	}
	if channels != 1 && nativeChannels <= 1 {
		channelCandidates = append(channelCandidates, 1)
	}

	var lastErr error
	for _, ch := range channelCandidates {
		for _, rate := range rateCandidates {
			cfg := base
			cfg.Playback.Channels = ch
			cfg.SampleRate = rate
			callbacks := malgo.DeviceCallbacks{Data: onData}
			if len(onStop) > 0 {
				callbacks.Stop = onStop[0]
			}
			dev, err := malgo.InitDevice(ctx, cfg, callbacks)
			if err != nil {
				lastErr = err
				continue
			}
			return dev, int(rate), int(ch), nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no playback rate/channel combination succeeded")
	}
	return nil, 0, 0, lastErr
}

// playbackRateCandidates orders sample rates to try when opening the device.
// Prefer the TTS rate (no in-process resample), then 44.1 kHz (common Apple
// clock). 48 kHz is last: exact 2× upsampling still sounded worse on the
// affected Studio Display than 44.1 with linear conversion.
func playbackRateCandidates(sourceRate int, preferredRate uint32) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	add := func(r uint32) {
		if r == 0 || seen[r] {
			return
		}
		seen[r] = true
		out = append(out, r)
	}
	if sourceRate > 0 {
		add(uint32(sourceRate))
	}
	add(44100)
	if preferredRate != 0 && preferredRate != 48000 {
		add(preferredRate)
	}
	add(48000)
	if preferredRate == 48000 {
		add(preferredRate)
	}
	return out
}
