package voiceagent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

type fixtureInput struct{}

func (*fixtureInput) Available() bool { return true }
func (*fixtureInput) Start(context.Context) (utterance.Session, error) {
	return nil, errors.New("not started in construction test")
}

type fixtureFrameCapture struct{ stubCapture }

func (*fixtureFrameCapture) ReadFrame(context.Context) (audio.Frame, error) {
	return audio.Frame{}, io.EOF
}
func (*fixtureFrameCapture) Close() error { return nil }

func TestNewFinalizedInputSkipsRecognizerAndDoesNotStartReader(t *testing.T) {
	for _, factory := range []bool{false, true} {
		t.Run(map[bool]string{false: "injected", true: "factory"}[factory], func(t *testing.T) {
			input := &fixtureInput{}
			source := &fixtureFrameCapture{}
			opts := Options{Config: &config.Config{STTProvider: "must-not-load", ModelsDir: t.TempDir()}, Events: events.NewBus(), Brain: &stubBrain{}, Worker: &stubBrain{}, Capture: source, Silent: true}
			calls := 0
			if factory {
				opts.InputFactory = func(got audio.FrameSource, vad *audio.VAD) (utterance.Provider, error) {
					calls++
					if got != source || vad != nil {
						t.Fatal("factory did not receive original capture/VAD")
					}
					return input, nil
				}
			} else {
				opts.Input = input
			}
			agent, cleanup, err := New(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if agent.Input != input || agent.STT != nil {
				t.Fatal("constructor loaded Standard recognition")
			}
			if factory && calls != 1 {
				t.Fatal("factory not called exactly once")
			}
		})
	}
}

func TestNewRejectsAmbiguousInputBeforeConstructingResources(t *testing.T) {
	input := &fixtureInput{}
	factory := func(audio.FrameSource, *audio.VAD) (utterance.Provider, error) {
		t.Fatal("factory called on invalid options")
		return nil, nil
	}
	for _, opts := range []Options{
		{Input: input, STT: &stubSTT{}},
		{Input: input, InputFactory: factory},
		{STT: &stubSTT{}, InputFactory: factory},
	} {
		opts.Config = &config.Config{BrainProvider: "must-not-construct"}
		opts.Events = events.NewBus()
		if _, _, err := New(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatal(err)
		}
	}
}

func TestNewFinalizedInputFactoryFailureAndCaptureBoundary(t *testing.T) {
	for _, frames := range []bool{false, true} {
		opts := Options{Config: &config.Config{}, Events: events.NewBus(), Brain: &stubBrain{}, Worker: &stubBrain{}, Capture: &stubCapture{}, Silent: true}
		called := false
		opts.InputFactory = func(audio.FrameSource, *audio.VAD) (utterance.Provider, error) {
			called = true
			return nil, errors.New("fixture preparation rejected")
		}
		if frames {
			opts.Capture = &fixtureFrameCapture{}
		}
		agent, cleanup, err := New(t.Context(), opts)
		if err == nil || agent != nil || cleanup != nil || called != frames {
			t.Fatalf("frames=%v called=%v err=%v", frames, called, err)
		}
	}
}
