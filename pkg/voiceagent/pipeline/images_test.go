package pipeline

import (
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestWithheldPictureIsSaidOnceForEachPersona(t *testing.T) {
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Events: bus}
	desk, chart := chatmedia.Ref{ID: "a", Name: "desk.png"}, chatmedia.Ref{ID: "b", Name: "chart.png"}
	venessa := &turnMetrics{response: events.ResponseEnvelope{PersonaID: "venessa", DisplayName: "Venessa"}}

	p.imagesWithheldHook(venessa)("lfm3:1.7b", []chatmedia.Ref{desk})
	p.imagesWithheldHook(venessa)("lfm3:1.7b", []chatmedia.Ref{desk, chart})
	p.imagesWithheldHook(venessa)("lfm3:1.7b", []chatmedia.Ref{desk, chart})
	p.imagesWithheldHook(nil)("", []chatmedia.Ref{desk})

	want := []string{
		"Venessa was not shown desk.png, because lfm3:1.7b cannot view images.",
		"Venessa was not shown chart.png, because lfm3:1.7b cannot view images.",
		"The model was not shown desk.png, because this provider cannot view images.",
	}
	if !slices.Equal(infos, want) {
		t.Fatalf("notices = %q", infos)
	}
}
