package playback

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

type recoverySignal struct {
	generation uint64
	reason     string
}

// ResetOutput cancels playback and releases the native output. The next stream
// opens a fresh device while preserving frontend ownership and volume.
func (p *Player) ResetOutput(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.resetOutputLocked("manual_reset")
}

func (p *Player) resetOutputLocked(reason string) error {
	p.logLifecycle("reset_requested", reason, nil)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("audio player is closed")
	}
	// Invalidate callback and pump generations before stopping native callbacks.
	p.generation++
	p.lifecycleState = "idle"
	p.recoveryReason = reason
	if reason == "manual_reset" {
		p.recoveryAttempts = 0
		p.lastError = ""
	}
	device, ctx, debug, ingress, frontend := p.device, p.ctx, p.debug, p.aecIngress, p.frontend
	p.device, p.ctx, p.aecIngress = nil, nil, nil
	p.actualDeviceName, p.clientRate, p.clientChannels = "", 0, 0
	p.mu.Unlock()
	p.Stop()
	var stopErr error
	if device != nil {
		stopErr = device.Stop()
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
	if resetter, ok := frontend.(audio.PlaybackReferenceResetter); ok {
		resetter.ResetPlaybackReference()
	}
	p.clearPlaybackFormat()
	p.mu.Lock()
	p.monoScratch = nil
	if frontend != nil && p.frontend == frontend && p.aecIngress == nil {
		p.aecIngress = newAECRefIngress(frontend)
	}

	generation := p.generation
	p.mu.Unlock()
	slog.Info("playback lifecycle", "event", "reset", "generation", generation, "reason", reason, "error", stopErr)
	// Uninitialization completes even when Stop reports that the device was
	// already stopped; a fresh lazy open remains available.
	return nil
}

func (p *Player) OutputStatus() audio.OutputStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.lifecycleState
	if state == "" {
		state = "idle"
	}
	return audio.OutputStatus{Generation: p.generation, State: state, RecoveryReason: p.recoveryReason, LastError: p.lastError, RecoveryAttempts: p.recoveryAttempts, Device: audio.NegotiatedDevice{RequestedName: p.deviceName, Name: p.actualDeviceName, IdentityKnown: p.actualDeviceName != "", SampleRate: p.sampleRate, Channels: p.channels, ClientSampleRate: p.clientRate, ClientChannels: p.clientChannels}}
}

func (p *Player) startRecoveryWorkerLocked() {
	if p.recoveryCh != nil {
		return
	}
	p.recoveryCh = make(chan recoverySignal, 1)
	p.recoveryDone = make(chan struct{})
	go p.recoveryWorker(p.recoveryCh, p.recoveryDone)
}

func (p *Player) signalRecovery(generation uint64, reason string) {
	p.mu.Lock()
	ch := p.recoveryCh
	valid := !p.closed && generation == p.generation
	p.mu.Unlock()
	if !valid || ch == nil {
		return
	}
	select {
	case ch <- recoverySignal{generation, reason}:
	default:
	}
}

func (p *Player) recoveryWorker(signals <-chan recoverySignal, done <-chan struct{}) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	routeTicks := 0
	for {
		select {
		case <-done:
			return
		case signal := <-signals:
			p.recoverOutput(signal, done)
		case now := <-tick.C:
			p.mu.Lock()
			generation, ready := p.generation, p.device != nil && !p.closed
			if ready && now.Sub(p.openedAt) > 30*time.Second && !p.callbackStalled(now) {
				p.recoveryAttempts = 0
			}
			p.mu.Unlock()
			if !ready {
				continue
			}
			if p.callbackStalled(now) {
				p.recoverOutput(recoverySignal{generation, "callback_stall"}, done)
				continue
			}
			routeTicks++
			if routeTicks%5 == 0 {
				p.checkOutputRoute(generation)
			}
		}
	}
}

func (p *Player) checkOutputRoute(generation uint64) {
	p.deviceMu.Lock()
	p.mu.Lock()
	ctx, name, actual := p.ctx, p.deviceName, p.routeID
	probe := p.testRoute
	valid := p.device != nil && p.generation == generation && !p.closed
	p.mu.Unlock()
	if !valid || (ctx == nil && probe == nil) {
		p.deviceMu.Unlock()
		return
	}
	var current string
	var err error
	if probe != nil {
		current, err = probe()
	} else {
		current, err = playbackRouteID(ctx.Context, name)
	}
	p.deviceMu.Unlock()
	if err != nil || current != actual {
		p.signalRecovery(generation, "output_route_changed")
	}
}

func (p *Player) recoverOutput(signal recoverySignal, done <-chan struct{}) {
	p.deviceMu.Lock()
	p.mu.Lock()
	valid := !p.closed && p.device != nil && signal.generation == p.generation
	rate := p.clientRate
	p.mu.Unlock()
	if !valid {
		p.deviceMu.Unlock()
		return
	}
	_ = p.resetOutputLocked(signal.reason)
	p.mu.Lock()
	epoch := p.epoch
	firstAttempt := p.recoveryAttempts + 1
	p.lifecycleState = "recovering"
	p.mu.Unlock()
	p.deviceMu.Unlock()
	// Recovery never requeues canceled speech. Only the silent native output is
	// reopened, with a bounded retry budget on the owning worker.
	if firstAttempt > 3 {
		p.mu.Lock()
		if !p.closed && p.epoch == epoch {
			p.lifecycleState = "failed"
			p.lastError = "audio output repeatedly lost; reset audio output to retry"
		}
		p.mu.Unlock()
		return
	}
	for attempt := firstAttempt; attempt <= 3; attempt++ {
		if attempt > 1 {
			timer := time.NewTimer(time.Duration(attempt-1) * 250 * time.Millisecond)
			select {
			case <-done:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		p.mu.Lock()
		if p.closed || p.epoch != epoch {
			p.mu.Unlock()
			return
		}
		p.recoveryAttempts = attempt
		p.mu.Unlock()
		_, err := p.ensureDeviceGeneration(rate, &epoch)
		p.mu.Lock()
		if err != nil && !p.closed && p.epoch == epoch {
			p.lastError = err.Error()
			p.lifecycleState = "failed"
		}
		generation := p.generation
		p.mu.Unlock()
		slog.Info("playback lifecycle", "event", "recovery", "generation", generation, "reason", signal.reason, "attempt", attempt, "error", err)
		if err == nil {
			return
		}
	}
}

func (p *Player) callbackStalled(now time.Time) bool {
	last := p.lastCallback.Load()
	return last != 0 && now.Sub(time.Unix(0, last)) > 3*time.Second
}

func (p *Player) observeCallback(now time.Time) {
	previous := p.lastCallback.Swap(now.UnixNano())
	if previous != 0 && now.Sub(time.Unix(0, previous)) > 200*time.Millisecond {
		p.callbackGaps.Add(1)
	}
}
func (p *Player) logLifecycle(event, reason string, err error) {
	status := p.OutputStatus()
	slog.Info("playback lifecycle", "event", event, "generation", status.Generation, "state", status.State, "reason", reason, "requested_device", status.Device.RequestedName, "selected_device", status.Device.Name, "source_rate", status.Device.ClientSampleRate, "client_rate", status.Device.SampleRate, "client_channels", status.Device.Channels, "backend_format", "unknown", "observed_period_frames", p.observedPeriodFrames.Load(), "callback_gaps", p.callbackGaps.Load(), "underruns", p.underruns.Load(), "gain", p.Volume(), "error", err)
}
