package voiceagent

import (
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/summon"
)

// NameSummon returns the gate New installed for name_summon_enabled, or nil
// when name summon is off for this agent. A host uses it to stand the gate
// down for a turn the user opened another way; the methods of a nil gate do
// nothing, so this needs no check:
//
//	agent.NameSummon().SetBypass(true) // button held
func (a *Agent) NameSummon() *summon.Gate {
	return a.nameSummon
}

// newNameSummonGate acts on name_summon_enabled. It returns nil when this
// agent gets no gate, and is called before New builds anything so a refusal
// costs no model load and opens no device.
//
// It fails rather than let New run without a gate the config asked for. An
// agent that answers a whole room, or its own voice, is the failure name
// summon exists to prevent, and a host cannot see a warning it was never
// shown.
func newNameSummonGate(cfg *config.Config, opts Options) (*summon.Gate, error) {
	gate, err := buildNameSummonGate(cfg, opts)
	if err != nil {
		return nil, err
	}
	if opts.NameSummonFollowUp < 0 || (gate == nil && opts.NameSummonFollowUp != 0) {
		return nil, fmt.Errorf("voiceagent: Options.NameSummonFollowUp needs a positive duration and an agent that name summon applies to (name_summon_enabled, voice input, ExternalAddressing unset)")
	}
	return gate, nil
}

func buildNameSummonGate(cfg *config.Config, opts Options) (*summon.Gate, error) {
	if !cfg.NameSummonEnabled || opts.ExternalAddressing {
		return nil, nil
	}
	// Text turns are never gated: typing to the agent is addressing it. Text
	// mode still hears speech when the host injected where it comes from.
	if opts.TextOnly && opts.STT == nil && opts.Input == nil {
		return nil, nil
	}
	// Only the arrangement New itself builds can be judged here: its own
	// microphone and its own playback, joined by the front-end it constructs.
	// A host that injects Capture, or reads speech from an injected provider
	// in text mode, owns its audio path and its echo cancellation with it.
	ownsAudioLoop := !opts.TextOnly && !opts.Silent && opts.Capture == nil
	if ownsAudioLoop && config.NameSummonSelfTriggerRisk(cfg) {
		return nil, fmt.Errorf("voiceagent: name_summon_enabled needs voice_frontend set to %s or %s: with no echo cancellation the agent hears its own name in its own playback and summons itself",
			config.VoiceFrontendWebRTC, config.VoiceFrontendNLMS)
	}
	gate, err := summon.NewGate(cfg.AgentName, cfg.NameSummonAliases, summon.WithFollowUp(opts.NameSummonFollowUp))
	if err != nil {
		return nil, fmt.Errorf("voiceagent: name_summon_enabled needs agent_name or name_summon_aliases: %w", err)
	}
	return gate, nil
}

// installNameSummon puts gate on the pipeline and, when a follow-up window was
// asked for, feeds it the end of each reply. The returned function detaches it
// from the event bus.
func installNameSummon(p *pipeline.Pipeline, bus *events.Bus, gate *summon.Gate, followUp time.Duration) (detach func()) {
	p.AddressGate = gate.Veto
	if followUp <= 0 {
		return func() {}
	}
	// ResponseReady marks the reply of an agent that does not speak;
	// SpeakingComplete arrives once per spoken segment, so the last one is the
	// moment the agent actually stopped talking.
	return bus.SubscribeAll(func(e events.Event) {
		switch e.(type) {
		case events.ResponseReady, events.SpeakingComplete:
			gate.ReplyEnded()
		}
	})
}
