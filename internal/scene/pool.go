package scene

import (
	"fmt"
	"strings"
)

// PoolKey identifies one TTS worker. Two cast members with the same key share
// a provider: a second Qwen worker is gigabytes of resident model to say the
// same sentences a per-turn voice already covers.
type PoolKey struct {
	Provider string
	Tier     string
}

// String names the worker for errors and progress lines.
func (k PoolKey) String() string {
	if k.Tier == "" {
		return k.Provider
	}
	return k.Provider + "/" + k.Tier
}

// PoolKeyFor is the worker a template speaks through. Only Qwen ships more than
// one model behind one provider name, so only Qwen's tier splits the pool.
func PoolKeyFor(t SynthTemplate) PoolKey {
	if t.Provider == providerQwen {
		return PoolKey{Provider: t.Provider, Tier: t.Tier}
	}
	return PoolKey{Provider: t.Provider}
}

// multiVoiceProviders are the TTS backends that honour a per-turn voice, which
// is what lets two cast members share one worker. D8 locks a scene's mouths to
// these two.
//
// Satisfying the typed-request interface is not the same as honouring the
// request: audio8 accepts one and then ignores the voice entirely, answering in
// its packaged reference voice whatever it was asked for. A backend like that
// speaks one voice per worker.
var multiVoiceProviders = map[string]bool{
	providerKokoro: true,
	providerQwen:   true,
}

// SpeaksOneVoice reports whether this worker ignores the per-turn voice, so
// everything it says comes out in whatever its construction config chose.
func (k PoolKey) SpeaksOneVoice() bool {
	return !multiVoiceProviders[k.Provider]
}

// PoolMember is one worker to construct, paired with the cast member whose
// applied config constructs it.
type PoolMember struct {
	Key  PoolKey
	Cast CastRuntime
}

// PoolPlan is the ordered, deduplicated set of workers a cast needs. Cast order
// decides which member's config constructs a shared worker; every turn then
// overrides the parts that differ with its own template.
func PoolPlan(cast []CastRuntime) []PoolMember {
	seen := map[PoolKey]bool{}
	plan := make([]PoolMember, 0, len(cast))
	for _, member := range cast {
		key := PoolKeyFor(member.Template)
		if seen[key] {
			continue
		}
		seen[key] = true
		plan = append(plan, PoolMember{Key: key, Cast: member})
	}
	return plan
}

// ValidatePool rejects a cast this build cannot pool, before anything
// constructs a provider or downloads a model.
func ValidatePool(cast []CastRuntime) error {
	if err := validateQwenTiers(cast); err != nil {
		return err
	}
	return validateVoiceCapacity(cast)
}

// validateQwenTiers refuses two Qwen tiers in one scene: that would be two
// resident workers of several gigabytes each to record a conversation measured
// in seconds. It fails closed rather than quietly loading both.
func validateQwenTiers(cast []CastRuntime) error {
	tier, owner := "", ""
	for _, member := range cast {
		if member.Template.Provider != providerQwen {
			continue
		}
		if owner == "" {
			tier, owner = member.Template.Tier, member.ID
			continue
		}
		if !strings.EqualFold(member.Template.Tier, tier) {
			return fmt.Errorf("scene: cast %q speaks qwen3-tts %s and %q speaks %s: one scene runs one qwen tier",
				owner, tier, member.ID, member.Template.Tier)
		}
	}
	return nil
}

// validateVoiceCapacity refuses a cast that would put two speakers behind one
// worker that cannot tell them apart.
//
// Such a worker speaks both parts in the same voice while the cast still looks
// like two distinct mouths, so --require-distinct-voices would pass on a
// recording that has one voice. Refusing here, before any model loads, is the
// only place that guarantee can be kept.
func validateVoiceCapacity(cast []CastRuntime) error {
	owner := map[PoolKey]string{}
	for _, member := range cast {
		key := PoolKeyFor(member.Template)
		if !key.SpeaksOneVoice() {
			continue
		}
		if first, ok := owner[key]; ok {
			return fmt.Errorf("scene: cast %q and %q both speak through %s, which ignores the per-turn voice and would read both parts in one voice: give one of them a different provider",
				first, member.ID, key)
		}
		owner[key] = member.ID
	}
	return nil
}
