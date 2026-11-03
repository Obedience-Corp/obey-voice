package playback

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// StartDiagnosticCapture opts into at most one minute and 32 MiB of PCM.
// At most three directories are retained. Expiration runs after 24 hours while
// the process is alive, or when a subsequent capture prunes old directories.
func (p *Player) StartDiagnosticCapture(ctx context.Context) (audio.DiagnosticCaptureStatus, error) {
	if err := ctx.Err(); err != nil {
		return audio.DiagnosticCaptureStatus{}, err
	}
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return audio.DiagnosticCaptureStatus{}, errors.New("audio player is closed")
	}
	if p.debug != nil && p.debug.status().Active {
		s := p.debug.status()
		p.mu.Unlock()
		return s, nil
	}
	old := p.debug
	root, name, rate, channels := p.debugRoot, p.actualDeviceName, p.sampleRate, p.channels
	sourceRate := p.clientRate
	p.mu.Unlock()
	old.close()
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return audio.DiagnosticCaptureStatus{}, err
		}
		root = filepath.Join(cache, "obey-voice", "playback-diagnostics")
	}
	recorder, err := newPlayerDebugRecorder(root, name, sourceRate, rate, channels)
	if err != nil {
		return audio.DiagnosticCaptureStatus{}, err
	}
	p.mu.Lock()
	p.debug = recorder
	p.mu.Unlock()
	return recorder.status(), nil
}

func (p *Player) StopDiagnosticCapture() audio.DiagnosticCaptureStatus {
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
	p.mu.Lock()
	recorder := p.debug
	p.mu.Unlock()
	recorder.close()
	return recorder.status()
}

func (p *Player) DiagnosticCaptureStatus() audio.DiagnosticCaptureStatus {
	p.mu.Lock()
	recorder := p.debug
	p.mu.Unlock()
	return recorder.status()
}
