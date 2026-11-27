package voiceagent

import (
	"fmt"
	"slices"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/kws"
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
	if opts.NameSummonAcousticWindow > 0 && (gate == nil || !cfg.AcousticNameEnabled) {
		return nil, fmt.Errorf("voiceagent: Options.NameSummonAcousticWindow needs acoustic_name_enabled and an agent that name summon applies to (name_summon_enabled, voice input, ExternalAddressing unset)")
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
	gate, err := summon.NewGate(cfg.AgentName, cfg.NameSummonAliases,
		summon.WithFollowUp(opts.NameSummonFollowUp), summon.WithAcousticEvidence(acousticWindow(cfg, opts)))
	if err != nil {
		return nil, fmt.Errorf("voiceagent: name_summon_enabled needs agent_name or name_summon_aliases: %w", err)
	}
	if cfg.AcousticNameEnabled && len(spellableNames(gate)) == 0 {
		return nil, fmt.Errorf("voiceagent: acoustic_name_enabled cannot listen for any of %q: %w", gate.Names(), kws.CheckName(gate.Names()[0]))
	}
	return gate, nil
}

// acousticWindow is how early a keyword hit must arrive to count as an
// address, or zero when the gate reads the transcript alone.
func acousticWindow(cfg *config.Config, opts Options) time.Duration {
	switch {
	case !cfg.AcousticNameEnabled || opts.NameSummonAcousticWindow < 0:
		return 0
	case opts.NameSummonAcousticWindow > 0:
		return opts.NameSummonAcousticWindow
	}
	window := summon.DefaultAcousticWindow
	// The hit time counts from the start of the audio handed to the
	// recogniser, which begins vad_pre_roll_ms before the speech. The default
	// was measured with measuredPreRoll, so it moves with the setting. Only
	// when New builds the recogniser: an injected one segments its own audio.
	if opts.STT == nil && opts.Input == nil && opts.InputFactory == nil {
		window += time.Duration(cfg.VADPreRollMS)*time.Millisecond - measuredPreRoll
	}
	return window
}

// measuredPreRoll is the vad_pre_roll_ms that summon.DefaultAcousticWindow was
// measured with, which is also that setting's default.
const measuredPreRoll = 300 * time.Millisecond

// spellableNames are the gate's names the keyword model can listen for.
func spellableNames(gate *summon.Gate) []string {
	var names []string
	for _, name := range gate.Names() {
		if kws.CheckName(name) == nil {
			names = append(names, name)
		}
	}
	return names
}

// withAcousticNames points the recogniser's keyword spotter at the names the
// gate answers to. Without a gate the config passes through untouched, and the
// spotter keeps listening for what it always did.
func withAcousticNames(cfg *config.Config, gate *summon.Gate, logf func(string, ...any)) *config.Config {
	if gate == nil || !cfg.AcousticNameEnabled {
		return cfg
	}
	names := spellableNames(gate)
	var skipped []string
	for _, name := range gate.Names() {
		if !slices.Contains(names, name) {
			skipped = append(skipped, name)
		}
	}
	if len(skipped) > 0 {
		logf("acoustic name detection listens for %q; it cannot spell %q, which rely on the transcript", names, skipped)
	}
	local := *cfg
	local.AcousticNames = names
	return &local
}

// installNameSummon puts gate on the pipeline and, when a follow-up window was
// asked for, feeds it the end of each reply. The returned function detaches it
// from the event bus.
func installNameSummon(p *pipeline.Pipeline, bus *events.Bus, gate *summon.Gate, followUp time.Duration, acoustic bool) (detach func()) {
	p.AddressGate = gate.Veto
	if acoustic {
		// Consulted instead of AddressGate, with what the recogniser's keyword
		// spotter heard. Left unset otherwise, so a host that swaps AddressGate
		// after New is not overruled by a hook it never asked for.
		p.AcousticAddressGate = gate.VetoHeard
	}
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
