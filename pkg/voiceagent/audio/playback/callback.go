package playback

func (p *Player) onData(outputSamples, inputSamples []byte, frameCount uint32) {
	clearBytes(outputSamples)
	p.observedPeriodFrames.Store(int32(frameCount))
	hundredths := p.volumeHundredths.Load()

	p.mu.Lock()
	channels := p.channels
	if channels <= 0 {
		channels = playbackChannels
	}
	monoNeed := int(frameCount) * 2
	if cap(p.monoScratch) < monoNeed {
		p.monoScratch = make([]byte, monoNeed)
	} else {
		p.monoScratch = p.monoScratch[:monoNeed]
	}
	monoBuf := p.monoScratch
	debug := p.debug
	ingress := p.aecIngress
	deviceRate := p.sampleRate
	p.mu.Unlock()
	clearBytes(monoBuf)

	framesRemaining := int(frameCount)
	monoOffset := 0
	writtenFrames := 0
	active := false
	defer func() {
		if !active {
			return
		}
		if debug != nil {
			debug.captureCallback(outputSamples, int(frameCount), writtenFrames)
		}
		// Callback-safe AEC handoff: copy into a pooled slot only (no alloc,
		// no Frontend mutex). Worker resamples with preserved phase and pushes.
		if ingress != nil && writtenFrames > 0 {
			ingress.TryEnqueueS16(monoBuf[:writtenFrames*2], deviceRate)
		}
	}()

	for framesRemaining > 0 {
		segment := p.currentSegment()
		if segment == nil {
			p.playing.Store(false)
			return
		}
		active = true

		var written int
		var finished bool
		if !segment.attempt.Write(func() {
			written, finished = segment.writeTo(monoBuf[monoOffset:], framesRemaining)
			applyVolumeS16(monoBuf[monoOffset:monoOffset+written*2], written, hundredths)
			expandMonoS16LE(monoBuf[monoOffset:monoOffset+written*2], written, channels, outputSamples[writtenFrames*channels*2:])
		}) {
			segment.interrupt()
			finished = true
		}
		if written == 0 {
			if finished {
				p.finishSegment(segment)
				continue
			}

			p.playing.Store(false)
			return
		}

		p.playing.Store(true)
		writtenFrames += written
		monoOffset += written * 2
		framesRemaining -= written

		if finished {
			p.finishSegment(segment)
		}
	}
}

// expandMonoS16LE writes mono S16LE frames into an interleaved multi-channel
// S16LE buffer. Channel 0 (and channel 1 when present) carry the mono signal;
// additional channels are left silent so multi-channel devices do not play
// garbage on rear/height buses.
func expandMonoS16LE(mono []byte, frames, channels int, out []byte) {
	if frames <= 0 || channels <= 0 {
		return
	}
	if channels == 1 {
		copy(out, mono[:frames*2])
		return
	}
	for i := 0; i < frames; i++ {
		s0 := mono[i*2]
		s1 := mono[i*2+1]
		base := i * channels * 2
		// Front L
		out[base] = s0
		out[base+1] = s1
		if channels > 1 {
			// Front R (duplicate mono)
			out[base+2] = s0
			out[base+3] = s1
		}
		// Remaining channels stay 0 from clearBytes.
	}
}

func (p *Player) currentSegment() *playbackSegment {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.current == nil && len(p.queue) > 0 {
		p.current = p.queue[0]
		p.queue = p.queue[1:]
	}

	return p.current
}

func (p *Player) finishSegment(segment *playbackSegment) {
	p.mu.Lock()
	if p.current == segment {
		p.current = nil
	}
	hasMore := p.current != nil || len(p.queue) > 0
	p.mu.Unlock()

	if !hasMore {
		p.playing.Store(false)
	}

	segment.complete()
}
