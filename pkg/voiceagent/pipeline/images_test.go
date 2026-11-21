package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
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
	const notice = "Mira was not shown desk.png, because this provider cannot view images."
	for _, voice := range []bool{false, true} {
		t.Run(map[bool]string{true: "voice", false: "text"}[voice], func(t *testing.T) {
			b := &noImageInput{}
			b.history = []brain.Turn{{Role: "user", Content: "Look.", Attachments: []chatmedia.Ref{{ID: "a", Name: "desk.png", Format: "png"}}}}
			bus := events.NewBus()
			said := 0
			events.Subscribe(bus, func(e events.Info) {
				if e.Message == notice {
					said++
				}
			})
			p := &Pipeline{Brain: b, Events: bus}
			p.AdmitInput = func(_ context.Context, input string, _ bool) (ResponseAdmission, error) {
				return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "r1", PersonaID: "mira", DisplayName: "Mira"}}, nil
			}
			say := func(text string) error {
				if !voice {
					return p.RunTurnTextMode(t.Context(), text)
				}
				p.STT = &fakeSTT{text: text}
				_, err := p.RunTurn(t.Context())
				return err
			}

			_ = say("tell me a long story")
			if b.requests != 0 || said != 0 {
				t.Fatalf("a routed turn sent %d requests and gave the notice %d times", b.requests, said)
			}
			for range 2 {
				if err := say("hello"); err != nil {
					t.Fatal(err)
				}
			}
			if b.requests != 2 || said != 1 {
				t.Fatalf("requests %d, notice said %d times", b.requests, said)
			}
		})
	}
}

// A new picture is admitted to the conversation whatever the selected Ollama
// model can view. The checks that keep a bad picture out of the record stay,
// and a provider that would drop the picture still refuses it.
func TestNewPictureAdmission(t *testing.T) {
	root := t.TempDir()
	config.SetConfigDirForTest(t, filepath.Join(root, "config"))
	var good []chatmedia.Ref
	for i := range 5 {
		ref, err := chatmedia.DefaultStore().Import(fmt.Sprintf("picture-%d.png", i), append(slices.Clone(testmedia.PNG()), byte(i)))
		if err != nil {
			t.Fatal(err)
		}
		good = append(good, ref)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}})
		case "/api/chat":
			t.Error("an admission check reached the model")
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "fixture"}}})
		}
	}))
	t.Cleanup(server.Close)
	textOnly, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", MaxHistory: 20, WorkDir: root, PromptsDir: filepath.Join(root, "prompts"), AgentName: "Venessa"})
	if err != nil {
		t.Fatal(err)
	}
	admit := (&Pipeline{Brain: textOnly}).ImageValidator()
	if err := admit(t.Context(), good[:1]); err != nil {
		t.Fatalf("a picture for the conversation was refused: %v", err)
	}
	if err := admit(t.Context(), good); err == nil || !strings.Contains(err.Error(), "at most four") {
		t.Fatalf("five pictures: %v", err)
	}
	if err := admit(t.Context(), []chatmedia.Ref{{ID: "missing", Name: "gone.png", Format: "png"}}); err == nil {
		t.Fatal("a picture that does not load was admitted")
	}
	if err := (&Pipeline{Brain: &noImageInput{}}).ImageValidator()(t.Context(), good[:1]); err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("a provider that would drop the picture: %v", err)
	}
	// A native-audio persona's turn would fail on the picture, so neither
	// check lets one through to it.
	textOnly.SetAudioResolver(silentMedia{})
	native := &Pipeline{Brain: textOnly}
	if err := native.ImageValidator()(t.Context(), good[:1]); !errors.Is(err, brain.ErrNativeImageInput) {
		t.Fatalf("a new picture for a native-audio persona: %v", err)
	}
	if err := native.validateAdmittedImages(t.Context(), good[:1]); !errors.Is(err, brain.ErrNativeImageInput) {
		t.Fatalf("a picture addressed to a native-audio persona: %v", err)
	}
}

type silentMedia struct{}

func (silentMedia) ResolveAudio(context.Context, utterance.AudioRef) ([]byte, error) {
	return nil, errors.New("no recording")
}
