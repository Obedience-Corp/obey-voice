package voiceagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// NativeCandidateOptions describes an isolated binding under preparation. The
// conversation owns Media/Store across persona switches. Lifetime belongs to
// that binding, not to the foreground turn that requested selection.
type NativeCandidateOptions struct {
	Lifetime       context.Context
	Media          *chatmedia.AudioSession
	Store          *chatmedia.Store
	Cache          *nativeaudio.SupportCache
	Controls       string
	Transcript     string
	RetainAudio    bool
	PublishCaption func(context.Context, func() error) error
}

// NativeCandidate owns selected HTTP/chat/speech resources. Preparation performs
// quiet protocol checks only; it neither opens capture/playback nor persists or
// activates a persona. Protocol support does not qualify quality/context safety.
// Call Close on failed preparation, replacement or shutdown, after quiescing the
// input consumer. Its Voice borrows the candidate's speech lifetime.
type NativeCandidate struct {
	Brain        brain.Provider
	Voice        *TTSSet
	Policy       *pipeline.NativePolicy
	Setup        nativeaudio.SetupResult
	cfg          *config.Config
	cancel       context.CancelFunc
	closeSpeech  func()
	closed       atomic.Bool
	brainAdopted atomic.Bool
	once         sync.Once
	closeErr     error
	scopeMu      sync.Mutex
	scope        work.WorkScope
}

func PrepareNativeCandidate(ctx context.Context, cfg *config.Config, opts NativeCandidateOptions) (*NativeCandidate, error) {
	if cfg == nil || opts.Lifetime == nil || opts.Media == nil || opts.Cache == nil {
		return nil, errors.New("native candidate requires config, binding lifetime, media and support cache")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := opts.Lifetime.Err(); err != nil {
		return nil, err
	}
	if opts.Controls != "standard" && opts.Controls != "conversation" {
		return nil, errors.New("invalid native controls")
	}
	if opts.Transcript != "auto" && opts.Transcript != "required" && opts.Transcript != "off" {
		return nil, errors.New("invalid native transcript policy")
	}
	if opts.Controls == "standard" && opts.Transcript == "off" {
		return nil, errors.New("standard controls require native transcription")
	}
	if opts.Controls == "conversation" && opts.Transcript == "auto" && opts.PublishCaption == nil {
		return nil, errors.New("automatic captions require a binding-fenced publisher")
	}
	if opts.RetainAudio && opts.Store == nil {
		return nil, errors.New("recording retention requires a session store")
	}
	snap := cfg.Clone()
	if snap.BrainProvider != "ollama" || snap.TTSProvider != config.OpenAISpeechProviderName || snap.TTSFallbackProvider != "disabled" || snap.TTSLookahead != 1 || snap.AcousticNameEnabled || (opts.Controls == "conversation" && snap.GroupParticipationEnabled) {
		return nil, errors.New("native candidate config must isolate same-model speech, single lookahead and compatible controls")
	}
	client, err := nativeaudio.NewClient(snap.OllamaHost, snap.OllamaModel)
	if err != nil {
		return nil, err
	}
	scheduler := nativeaudio.NewScheduler()
	if err := client.BindScheduler(scheduler); err != nil {
		client.Close()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(opts.Lifetime)
	prepareCtx, cancelPrepare := context.WithCancel(ctx)
	stopLifetime := context.AfterFunc(lifetime, cancelPrepare)
	defer stopLifetime()
	defer cancelPrepare()
	candidate := &NativeCandidate{cfg: snap, cancel: cancel}
	candidate.Policy = &pipeline.NativePolicy{Lifetime: lifetime, PublishCaption: opts.PublishCaption, Controls: opts.Controls, Transcript: opts.Transcript, RetainAudio: opts.RetainAudio, Media: opts.Media, Store: opts.Store, Client: client, Scheduler: scheduler}
	success := false
	defer func() {
		if !success {
			_ = candidate.Close()
		}
	}()
	required := nativeaudio.Requirements{ASR: opts.Controls == "standard" || opts.Transcript == "required", Tools: snap.VoiceToolsEnabled && opts.Controls == "standard"}
	speech, closeSpeech, result, err := opts.Cache.Prepare(prepareCtx, client, snap, required)
	if err != nil {
		return nil, fmt.Errorf("prepare native routes: %w", err)
	}
	candidate.closeSpeech = closeSpeech
	candidate.Setup = result
	candidate.Voice = &TTSSet{Primary: speech}
	candidate.Brain, err = brain.NewNativeOllama(prepareCtx, snap, client.HTTPClient(), opts.Media)
	if err != nil {
		return nil, fmt.Errorf("prepare native brain: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := lifetime.Err(); err != nil {
		return nil, err
	}
	success = true
	return candidate, nil
}

// InputFactory uses New's existing finalized capture and VAD. No recognizer is
// created. Reader acquisition still occurs only when the pipeline starts input.
func (n *NativeCandidate) InputFactory(source audio.FrameSource, vad *audio.VAD) (utterance.Provider, error) {
	if n == nil || n.closed.Load() {
		return nil, errors.New("native candidate is closed")
	}
	return stt.NewNativeCapture(n.cfg, source, vad, n.Policy.Media, false)
}

// Close retires producers before releasing provider resources. It does not close
// conversation media, which may still back canonical history in another mode.
func (n *NativeCandidate) Close() error {
	if n == nil {
		return nil
	}
	n.once.Do(func() {
		n.closed.Store(true)
		if n.cancel != nil {
			n.cancel()
		}
		if n.Policy != nil {
			n.closeErr = n.Policy.Retire(context.Background())
		}
		if n.closeSpeech != nil {
			n.closeSpeech()
		}
		if n.Brain != nil && !n.brainAdopted.Load() {
			n.closeErr = errors.Join(n.closeErr, brain.CloseProvider(n.Brain))
		}
	})
	return n.closeErr
}

// TransferBrainOwnership is called only after durable pipeline publication.
// The host must then close the pipeline's current/retired brains, as for a
// Standard replacement. Candidate retirement still owns selected HTTP/speech.
func (n *NativeCandidate) TransferBrainOwnership() {
	if n != nil {
		n.brainAdopted.Store(true)
	}
}

// ValidatePrepared checks the lifetime/resource fence immediately before the
// host persists selection. It performs no new inference or capability probes.
func (n *NativeCandidate) ValidatePrepared(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n == nil || n.closed.Load() || n.Brain == nil || n.Voice == nil || n.Voice.Primary == nil || !n.Voice.Primary.Available() || n.Policy == nil || n.Policy.Lifetime == nil || !n.Setup.ProtocolReady {
		return errors.New("native candidate is no longer prepared")
	}
	return n.Policy.Lifetime.Err()
}
