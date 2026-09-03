package speaker

import "fmt"

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
