package pipeline

import (
	"context"
	"slices"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
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

// reportsWithheld answers every turn and, like a model that cannot view
// images, reports the picture it was not sent.
type reportsWithheld struct {
	*streamClock
	ref chatmedia.Ref
}

func (b *reportsWithheld) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	if opts.OnImagesWithheld != nil {
		opts.OnImagesWithheld("lfm3:1.7b", []chatmedia.Ref{b.ref})
	}
	return b.streamClock.ThinkStream(ctx, input, opts)
}

// The model's report reaches the user from a typed turn and from a spoken one.
func TestWithheldPictureReportReachesTheUser(t *testing.T) {
	for _, voice := range []bool{false, true} {
		t.Run(map[bool]string{true: "voice", false: "text"}[voice], func(t *testing.T) {
			bus := events.NewBus()
			var infos []string
			events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
			p := &Pipeline{Brain: &reportsWithheld{streamClock: &streamClock{}, ref: chatmedia.Ref{ID: "a", Name: "desk.png"}}, Events: bus}
			p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
				return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "venessa", DisplayName: "Venessa"}}, nil
			}
			for range 2 {
				var err error
				if voice {
					p.STT = &fakeSTT{text: "hello"}
					_, err = p.RunTurn(t.Context())
				} else {
					err = p.RunTurnTextMode(t.Context(), "hello")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if want := []string{"Venessa was not shown desk.png, because lfm3:1.7b cannot view images."}; !slices.Equal(infos, want) {
				t.Fatalf("notices = %q", infos)
			}
		})
	}
}

// noImageInput is a provider that takes no image input and counts its requests.
type noImageInput struct {
	compactBrain
	requests int
}

func (b *noImageInput) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	b.requests++
	out := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	out <- "ok"
	close(out)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: out, Done: done}, nil
}

// A provider with no image input answers a conversation that holds a picture.
// The user is told with the request that goes out, not with a turn that sends
// the provider nothing.
func TestProviderWithoutImageInputAnswersAndSaysSoWithItsRequest(t *testing.T) {
	b := &noImageInput{}
	b.history = []brain.Turn{{Role: "user", Content: "Look.", Attachments: []chatmedia.Ref{{ID: "a", Name: "desk.png", Format: "png"}}}}
	bus := events.NewBus()
	var infos []string
	events.Subscribe(bus, func(e events.Info) { infos = append(infos, e.Message) })
	p := &Pipeline{Brain: b, Events: bus}
	p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
		return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", DisplayName: "Mira"}}, nil
	}
	const notice = "Mira was not shown desk.png, because this provider cannot view images."

	_ = p.RunTurnTextMode(t.Context(), "tell me a long story")
	if b.requests != 0 || slices.Contains(infos, notice) {
		t.Fatalf("a routed turn sent %d requests and gave notices %q", b.requests, infos)
	}
	for range 2 {
		if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
			t.Fatal(err)
		}
	}
	said := 0
	for _, info := range infos {
		if info == notice {
			said++
		}
	}
	if b.requests != 2 || said != 1 {
		t.Fatalf("requests %d, notice said %d times in %q", b.requests, said, infos)
	}
}
