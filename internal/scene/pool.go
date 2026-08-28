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
//
// Two Qwen tiers in one scene would mean two resident workers of several
// gigabytes each to record a conversation measured in seconds. It fails closed
// rather than quietly loading both.
func ValidatePool(cast []CastRuntime) error {
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
