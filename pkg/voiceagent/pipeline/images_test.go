package pipeline

import (
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
)

func TestWithheldPictureIsSaidOnceForEachPersona(t *testing.T) {
	for name, coordinator := range map[string]bool{"plain": false, "with a work coordinator": true} {
		t.Run(name, func(t *testing.T) {
			bus := events.NewBus()
			var infos []string
			events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
			p := &Pipeline{Events: bus}
			if coordinator {
				jobs := job.NewTable(t.Context())
				defer jobs.Close()
				NewAgency(p, jobs, nil, &config.Config{}, nil).Wire()
			}
			if err := p.BeginConversationIdentity("first"); err != nil {
				t.Fatal(err)
			}
			desk, chart := chatmedia.Ref{ID: "a", Name: "desk.png"}, chatmedia.Ref{ID: "b", Name: "chart.png"}
			venessa := &turnMetrics{response: events.ResponseEnvelope{PersonaID: "venessa", DisplayName: "Venessa"}}

			p.imagesWithheldHook(venessa)("lfm3:1.7b", []chatmedia.Ref{desk})
			p.imagesWithheldHook(venessa)("lfm3:1.7b", []chatmedia.Ref{desk, chart})
			p.imagesWithheldHook(venessa)("lfm3:1.7b", []chatmedia.Ref{desk, chart})
			p.imagesWithheldHook(nil)("", []chatmedia.Ref{desk})
			// A new conversation that shares the same picture says it again.
			if err := p.BeginConversationIdentity("second"); err != nil {
				t.Fatal(err)
			}
			p.imagesWithheldHook(venessa)("lfm3:1.7b", []chatmedia.Ref{desk})

			want := []string{
				"Venessa was not shown desk.png, because lfm3:1.7b cannot view images.",
				"Venessa was not shown chart.png, because lfm3:1.7b cannot view images.",
				"The model was not shown desk.png, because this provider cannot view images.",
				"Venessa was not shown desk.png, because lfm3:1.7b cannot view images.",
			}
			if !slices.Equal(infos, want) {
				t.Fatalf("notices = %q", infos)
			}
		})
	}
}
