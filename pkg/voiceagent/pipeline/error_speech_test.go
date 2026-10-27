package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestProviderErrorsDoNotSynthesizeOrPlaySpeech(t *testing.T) {
	for _, textMode := range []bool{false, true} {
		for _, recovered := range []bool{false, true} {
			bus := events.NewBus()
			tts := &fakeTTS{}
			player := newFakePlayer(time.Millisecond)
			var reports []events.Error
			events.Subscribe(bus, func(e events.Error) { reports = append(reports, e) })
			p := &Pipeline{STT: &fakeSTT{text: "hello"}, Brain: &fakeBrain{streamErr: errors.New("provider unavailable"), recovered: recovered}, TTS: tts, Player: player, Events: bus}
			var err error
			if textMode {
				err = p.RunTurnTextMode(t.Context(), "hello")
			} else {
				_, err = p.RunTurn(t.Context())
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(tts.CallTimes()) != 0 || len(player.FinishedTimes()) != 0 {
				t.Fatal("provider failure generated audio")
			}
			if len(reports) != 1 || !strings.Contains(reports[0].Message, "provider unavailable") {
				t.Fatalf("failure details=%v", reports)
			}
			_ = player.Close()
		}
	}
}

func TestNarrationErrorsStayTextOnly(t *testing.T) {
	for _, which := range []string{"source failure", "empty source", "job failure", "empty job result"} {
		t.Run(which, func(t *testing.T) {
			bus := events.NewBus()
			tts := &fakeTTS{}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			var reports []events.Error
			events.Subscribe(bus, func(e events.Error) { reports = append(reports, e) })
			p := &Pipeline{Events: bus, TTS: tts, Player: player}
			n := &Narrator{pipeline: p}
			switch which {
			case "source failure", "empty source":
				n.planSource = func(context.Context, string) ([]narrateUnit, error) {
					if which == "source failure" {
						return nil, errors.New("permission denied")
					}
					return nil, nil
				}
				n.startPlayback(t.Context(), "fixture")
			case "job failure":
				n.storyJobID = "fixture"
				n.handleJobDone("fixture", job.StateFailed, "", errors.New("worker unavailable"))
			case "empty job result":
				n.storyJobID = "fixture"
				n.handleJobDone("fixture", job.StateDone, "", nil)
			}
			if len(reports) != 1 || reports[0].Message == "" || len(tts.CallTimes()) != 0 {
				t.Fatalf("reports=%v synthesis=%v", reports, tts.CallTimes())
			}
		})
	}
}

func TestWorkerFailureKeepsFullDetailsWithoutAutomaticSpeech(t *testing.T) {
	bus := events.NewBus()
	tts := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	jobs := job.NewTable(t.Context())
	defer jobs.Close()
	p := &Pipeline{Events: bus, TTS: tts, Player: player}
	a := NewAgency(p, jobs, nil, &config.Config{}, nil)
	var report events.JobFailed
	events.Subscribe(bus, func(e events.JobFailed) { report = e })
	detail := "provider request failed: " + strings.Repeat("context ", 30) + "authentication expired"
	a.onJobDone("fixture", job.StateFailed, "", errors.New(detail))
	a.tryFlushPending()
	if report.Err != detail || len(tts.CallTimes()) != 0 {
		t.Fatalf("report=%+v speech=%v", report, tts.CallTimes())
	}
}
