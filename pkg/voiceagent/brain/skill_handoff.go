package brain

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

// recentSkillTurns is how many user turns an activation stays current. A
// request and the "yes" that confirms it are separate turns, and only the
// first one names anything a skill can match.
const recentSkillTurns = 3

// ActivatedSkillsProvider is a live brain that chooses Agent Skills itself and
// can say which ones the recent conversation activated.
type ActivatedSkillsProvider interface {
	RecentActivatedSkills() []string
}

type activatedSkillsKey struct{}

// WithActivatedSkills hands a worker the skills the live conversation already
// activated for the request it is being given. The worker injects them without
// asking its own model to confirm: that decision was made by the model that
// heard the request, and a smaller worker model can decline it.
func WithActivatedSkills(ctx context.Context, names []string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	return context.WithValue(ctx, activatedSkillsKey{}, append([]string(nil), names...))
}

// ActivatedSkills reports the skills handed over with WithActivatedSkills.
func ActivatedSkills(ctx context.Context) []string {
	names, _ := ctx.Value(activatedSkillsKey{}).([]string)
	return names
}

// RecentActivatedSkills implements ActivatedSkillsProvider.
func (o *OllamaBrain) RecentActivatedSkills() []string {
	o.stateMu.Lock()
	defer o.stateMu.Unlock()
	return append([]string(nil), o.recentSkills...)
}

// rememberActivatedSkills records one turn's activation, or ages the last one
// out when the turn activated nothing.
func (o *OllamaBrain) rememberActivatedSkills(names []string) {
	o.stateMu.Lock()
	defer o.stateMu.Unlock()
	if len(names) > 0 {
		o.recentSkills, o.recentSkillsAge = append([]string(nil), names...), 0
		return
	}
	if o.recentSkillsAge++; o.recentSkillsAge >= recentSkillTurns {
		o.recentSkills = nil
	}
}

// handedSkills resolves handed-over names against this brain's own catalog. A
// name the worker's directory does not provide is dropped.
func (o *OllamaBrain) handedSkills(ctx context.Context) []skills.Skill {
	var out []skills.Skill
	for _, name := range ActivatedSkills(ctx) {
		for _, skill := range o.skills {
			if skill.Name == name {
				out = append(out, skill)
				break
			}
		}
	}
	return out
}

// mergeSkills appends the skills of more that first does not already hold.
func mergeSkills(first, more []skills.Skill) []skills.Skill {
	out := append([]skills.Skill(nil), first...)
	for _, skill := range more {
		known := false
		for _, have := range out {
			known = known || have.Name == skill.Name
		}
		if !known {
			out = append(out, skill)
		}
	}
	return out
}
