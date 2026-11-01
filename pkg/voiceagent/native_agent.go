package voiceagent

import (
	"context"
	"errors"
	"sync"
)

// NewAgent builds ordinary voice/text delivery from a prepared native binding.
// The explicit authorize callback verifies host integration/history readiness;
// protocol probes alone never activate a production persona. It does not require
// general answer accuracy beyond the documented experimental opt-in contract.
//
// Config/brain/speech/input-factory overrides are rejected because these were
// already prepared together. Input may be supplied by an isolated recording
// fixture or remote host. Otherwise the existing capture/VAD builds native input.
// On success cleanup owns candidate retirement and pipeline brains. As with New,
// the host must cancel/join foreground turns before calling cleanup. The optional
// LiveTTS manager remains host-owned. Failed construction leaves the candidate
// caller-owned for retry/discard, and returns no partial agent.
func (n *NativeCandidate) NewAgent(ctx context.Context, opts Options, authorize func(context.Context, *NativeCandidate) error) (*Agent, func(), error) {
	if err := n.ValidatePrepared(ctx); err != nil {
		return nil, nil, err
	}
	if n.cfg == nil {
		return nil, nil, errors.New("native candidate has no prepared configuration")
	}
	if authorize == nil {
		return nil, nil, errors.New("native agent requires an explicit integration readiness decision")
	}
	if opts.Config != nil || opts.Brain != nil || opts.STT != nil || opts.TTS != nil || opts.TTSFallback != nil || opts.InputFactory != nil {
		return nil, nil, errors.New("native agent cannot override its prepared config, brain, speech or input factory")
	}
	if opts.Input != nil && !opts.TextOnly && opts.Capture == nil {
		return nil, nil, errors.New("injected native input requires its capture monitor")
	}
	if opts.Input != nil && !opts.Input.Available() {
		return nil, nil, errors.New("native finalized input is unavailable")
	}
	if !opts.TextOnly && opts.Input == nil && !n.cfg.VADEnabled && opts.VAD == nil {
		return nil, nil, errors.New("native capture requires voice activity detection; enable it or select Standard mode")
	}
	if err := authorize(ctx, n); err != nil {
		return nil, nil, err
	}
	if err := n.ValidatePrepared(ctx); err != nil {
		return nil, nil, err
	}
	opts.Config = n.cfg.Clone()
	opts.Brain = n.Brain
	opts.TTS = n.Voice.Primary
	if !opts.TextOnly && opts.Input == nil {
		opts.InputFactory = n.InputFactory
	}
	agent, cleanup, err := New(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if opts.LiveTTS != nil && !opts.LiveTTS.Adopt(n.Voice) {
		cleanup()
		return nil, nil, errors.New("native voice manager is shutting down")
	}
	agent.Native = n.Policy
	n.TransferBrainOwnership()
	var once sync.Once
	closeAgent := func() {
		once.Do(func() {
			report := func(err error) {
				if err != nil && opts.Logf != nil {
					opts.Logf("native cleanup: %v", err)
				}
			}
			report(agent.CloseWork())
			cleanup()
			report(n.Close())
			report(agent.CloseBrains())
		})
	}
	return agent, closeAgent, nil
}
