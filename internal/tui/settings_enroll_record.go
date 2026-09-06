package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/internal/speaker"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

const (
	// enrollCaptureQueue is the subscriber depth. Capture drops on a full
	// queue rather than blocking the device thread, so this is sized for the
	// whole clip: a dropped frame here is a hole in a voiceprint.
	enrollCaptureQueue = 64
	// enrollProgressInterval throttles the overlay to ~10 Hz. The elapsed
	// readout is the only proof the mic is live; it does not need every frame.
	enrollProgressInterval = 100 * time.Millisecond
	// enrollSilenceTimeout fails a clip when the device delivers nothing at
	// all — a stopped capture graph would otherwise hang the overlay until Esc.
	enrollSilenceTimeout = 10 * time.Second
)

// enrollClipSamples is the per-clip sample count at the capture rate.
func enrollClipSamples() int {
	return int(enrollClipLength * audio.SampleRate / time.Second)
}

// recordEnrollment records the clips, enrolls them, and seeds a running live
// engine. Everything here runs on the Cmd goroutine; the update loop only sees
// bridge progress and one enrollDoneMsg.
func (m settingsModel) recordEnrollment(ctx context.Context, name string, events *eventBridge) tea.Cmd {
	captureFor, openStore, newEngine := m.captureFor, m.enrollmentStore, m.speakerEngine
	seeder, gap := m.liveSeeder, m.enrollGap
	claimOwner := m.enroll.claimOwner
	// Read the window from config on the update loop: the worker must embed at
	// the same window live analysis verifies at, never a literal.
	windowMS := speaker.FromAppConfig(m.cfg).LiveWindowMS()

	return func() tea.Msg {
		// The bridge always closes, so the update loop's armed read resolves
		// whichever way the registration ends.
		defer events.send(enrollClosedMsg{})

		wavs, cleanup, err := recordEnrollClips(ctx, captureFor, events, gap)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return enrollDoneMsg{err: err}
		}
		events.send(enrollEnrollingMsg{})

		profile, store, err := enrollRecordedClips(ctx, newEngine, openStore, name, windowMS, wavs, claimOwner)
		if err != nil {
			return enrollDoneMsg{err: err}
		}
		seeded, seedErr := seedLiveEngine(seeder, store, profile)
		return enrollDoneMsg{profile: profile, seeded: seeded, seedErr: seedErr}
	}
}

// recordEnrollClips writes enrollClips WAVs of enrollClipSeconds each into a
// temporary directory, returning the paths and the cleanup that removes them.
// cleanup is returned even on failure so a partial run leaves nothing behind.
func recordEnrollClips(
	ctx context.Context,
	captureFor func() (speaker.CaptureSource, func(), error),
	events *eventBridge,
	gap time.Duration,
) (paths []string, cleanup func(), err error) {
	capture, release, err := openEnrollCapture(captureFor)
	if err != nil {
		return nil, nil, err
	}
	// Released before the model loads: enrollment must not hold the mic open
	// through a first-run TitaNet download.
	defer release()

	dir, err := os.MkdirTemp("", "obey-voice-enroll-")
	if err != nil {
		return nil, nil, fmt.Errorf("create clip directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	paths = make([]string, 0, enrollClips)
	for clip := 1; clip <= enrollClips; clip++ {
		if clip > 1 && gap > 0 {
			select {
			case <-ctx.Done():
				return nil, cleanup, ctx.Err()
			case <-time.After(gap):
			}
		}
		events.send(enrollProgressMsg{clip: clip})
		samples, err := recordEnrollClip(ctx, capture, clip, events)
		if err != nil {
			return nil, cleanup, err
		}
		path := filepath.Join(dir, fmt.Sprintf("clip-%d.wav", clip))
		if err := audio.WriteWAVFloat32(path, audio.SampleRate, samples); err != nil {
			return nil, cleanup, fmt.Errorf("write clip %d: %w", clip, err)
		}
		paths = append(paths, path)
	}
	return paths, cleanup, nil
}

// openEnrollCapture resolves the microphone for a registration, normalizing
// every "there is no mic here" shape into errNoMicrophone so the status line
// can say the one thing the user can act on.
func openEnrollCapture(captureFor func() (speaker.CaptureSource, func(), error)) (speaker.CaptureSource, func(), error) {
	if captureFor == nil {
		return nil, nil, errNoMicrophone
	}
	capture, release, err := captureFor()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errNoMicrophone, err)
	}
	if release == nil {
		release = func() {}
	}
	if capture == nil {
		release()
		return nil, nil, errNoMicrophone
	}
	return capture, release, nil
}

// recordEnrollClip accumulates one clip from the capture fan-out.
//
// The subscription opens here and closes with the clip, because capture keeps
// publishing through the pause between clips. A fan-out held open across that
// pause hands the next clip a queue of stale gap audio, and the clip fills its
// quota from the backlog — losing the tail of what the user actually said. Each
// WAV therefore starts at the moment the overlay says "recording clip N".
func recordEnrollClip(ctx context.Context, capture speaker.CaptureSource, clip int, events *eventBridge) ([]float32, error) {
	subID, chunks := capture.Subscribe(enrollCaptureQueue)
	defer capture.Unsubscribe(subID)

	want := enrollClipSamples()
	samples := make([]float32, 0, want)
	nextReport := time.Now()
	for len(samples) < want {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case chunk, ok := <-chunks:
			if !ok {
				return nil, fmt.Errorf("clip %d: microphone stopped", clip)
			}
			// append copies the values out of the frame capture fans to every
			// subscriber; retaining the slice itself would alias it.
			samples = append(samples, chunk...)
			if now := time.Now(); !now.Before(nextReport) {
				nextReport = now.Add(enrollProgressInterval)
				events.send(enrollProgressMsg{clip: clip, elapsed: audio.SamplesDuration(len(samples))})
			}
		case <-time.After(enrollSilenceTimeout):
			return nil, fmt.Errorf("clip %d: no audio from the microphone", clip)
		}
	}
	return samples[:want], nil
}

// enrollRecordedClips loads the embedding model, persists the profile, and
// returns the open store so the caller can read back what it wrote.
func enrollRecordedClips(
	ctx context.Context,
	newEngine func(context.Context) (speaker.Engine, func(), error),
	openStore func() (*speaker.Enrollment, error),
	name string,
	windowMS int,
	wavs []string,
	claimOwner bool,
) (speaker.Profile, *speaker.Enrollment, error) {
	if newEngine == nil || openStore == nil {
		return speaker.Profile{}, nil, fmt.Errorf("speaker enrollment is not wired on this screen")
	}
	engine, closeEngine, err := newEngine(ctx)
	if err != nil {
		return speaker.Profile{}, nil, err
	}
	if closeEngine != nil {
		defer closeEngine()
	}
	rev, ok := engine.(speaker.EnrolledSeeder)
	if !ok {
		return speaker.Profile{}, nil, fmt.Errorf("speaker engine cannot report its embedding revision")
	}
	store, err := openStore()
	if err != nil {
		return speaker.Profile{}, nil, err
	}
	profile, err := speaker.EnrollFromWAVs(ctx, engine, store, name, rev.EmbeddingRev(), windowMS, wavs)
	if err != nil {
		return speaker.Profile{}, nil, err
	}
	// "Register your voice" is a claim of ownership. A store whose owner was
	// removed (or that only ever held guests enrolled from the CLI) gets one
	// back here, so the rows do not read "nothing registered" immediately
	// after a successful registration.
	//
	// Guest enroll never claims the seat. Add still auto-owns the first
	// profile of an empty store, which is the case after the last owner is
	// removed — ClearOwner puts that flag back down.
	if claimOwner {
		if _, owned := store.Owner(); !owned {
			if err := store.SetOwner(profile.Name); err != nil {
				return profile, store, err
			}
			profile.Owner = true
		}
	} else if profile.Owner {
		if err := store.ClearOwner(); err != nil {
			return profile, store, err
		}
		profile.Owner = false
	}
	return profile, store, nil
}

// seedLiveEngine hands the profile to a running live engine so the next window
// resolves it by name. No conversation (Settings from the launcher, labels off)
// is not a failure: the next engine build seeds from the store.
func seedLiveEngine(seeder func() LiveSpeakerController, store *speaker.Enrollment, profile speaker.Profile) (bool, error) {
	if seeder == nil || store == nil {
		return false, nil
	}
	controller := seeder()
	if controller == nil {
		return false, nil
	}
	embeddings, err := store.Embeddings(profile.Name)
	if err != nil {
		return false, err
	}
	if err := controller.SeedEnrolled(profile.Name, profile.ModelRev, embeddings); err != nil {
		return false, err
	}
	return true, nil
}
