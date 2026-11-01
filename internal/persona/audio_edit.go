package persona

// UpdateAudio changes only per-persona audio choices. Standard brain/voice and
// worker fields remain intact, including saved choices while native is enabled.
func UpdateAudio(id string, audio *Audio) (*Profile, error) {
	if err := audio.Validate(); err != nil {
		return nil, err
	}
	p, err := Load(id)
	if err != nil {
		return nil, err
	}
	if audio == nil {
		p.Audio = nil
	} else {
		copy := *audio
		p.Audio = &copy
	}
	if err := Write(p, false); err != nil {
		return nil, err
	}
	return p, nil
}
