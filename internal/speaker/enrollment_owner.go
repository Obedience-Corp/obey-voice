package speaker

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Owner returns the profile flagged as the machine's human, if any.
//
// Absence is a normal state, not an error: a fresh install has no profiles,
// and removing the owner deliberately leaves the seat empty rather than
// promoting whoever happens to sort first.
func (e *Enrollment) Owner() (Profile, bool) {
	if e == nil {
		return Profile{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.profiles {
		if p.Owner {
			return p, true
		}
	}
	return Profile{}, false
}

// backfillOwner adopts the lone profile of a store written before the owner
// flag existed, so Settings does not report "nothing registered" over a
// voiceprint that can only be the user's own.
//
// Two conditions narrow it to that case and no other. legacy means the file
// carried no `owner:` key at all, which separates a pre-flag store from one
// whose owner was deliberately removed and left a guest behind — that seat
// stays empty. And exactly one profile, because with two there is nothing to
// read the answer off and map order is not a human.
//
// The adopted flag lives in memory until the next save persists it: load is a
// read path, and LoadEnrollment exists so an unenrolled install stays untouched.
func (e *Enrollment) backfillOwner(legacy bool) {
	if !legacy || len(e.profiles) != 1 {
		return
	}
	for slug, p := range e.profiles {
		p.Owner = true
		e.profiles[slug] = p
	}
}

// SetOwner moves the owner flag onto name, clearing it everywhere else, so the
// store can never claim two owners. Enrollment data is untouched: this is a
// role change, not a re-enroll.
func (e *Enrollment) SetOwner(name string) error {
	slug, err := profileSlug(name)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	target, ok := e.profiles[slug]
	if !ok {
		return fmt.Errorf("speaker: set owner %q: %w", name, ErrNotEnrolled)
	}
	if target.Owner && e.singleOwnerLocked(slug) {
		return nil
	}

	prev := make(map[string]Profile, len(e.profiles))
	for key, p := range e.profiles {
		prev[key] = p
		if p.Owner && key != slug {
			p.Owner = false
			e.profiles[key] = p
		}
	}
	target.Owner = true
	e.profiles[slug] = target

	if err := e.saveLocked(); err != nil {
		// profiles.yaml is the record; an unsaved flag must not linger in
		// memory where List would report an ownership that does not exist.
		for key, p := range prev {
			e.profiles[key] = p
		}
		return err
	}
	return nil
}

// singleOwnerLocked reports whether slug is the only owner. Callers hold mu.
func (e *Enrollment) singleOwnerLocked(slug string) bool {
	for key, p := range e.profiles {
		if p.Owner && key != slug {
			return false
		}
	}
	return true
}

// ownerKeysAbsent reports whether profiles.yaml predates the owner flag. Every
// save since writes `owner:` on every profile, false included, so a file with
// none was recorded before ownership was a concept — the one case where a lone
// profile can be assumed to be the machine's human rather than a guest left
// behind by a deliberate removal.
func ownerKeysAbsent(raw []byte) bool {
	var probe struct {
		Profiles []struct {
			Owner *bool `yaml:"owner"`
		} `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return false
	}
	for _, p := range probe.Profiles {
		if p.Owner != nil {
			return false
		}
	}
	return true
}
