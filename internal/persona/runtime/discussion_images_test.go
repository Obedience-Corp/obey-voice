package personaruntime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// Keep continuity and discussion capabilities visible through the concrete test provider.
type imageGroupReply struct{ *groupReplyBrain }

func (b *imageGroupReply) ValidateImages(context.Context, []chatmedia.Ref) error { return nil }
func TestGroupInputImagesSurvivePersonaHandoff(t *testing.T) {
	f, _, _ := groupFixture(t)
	f.pipeline.Brain = &imageGroupReply{f.pipeline.Brain.(*groupReplyBrain)}
	makeBrain := f.selector.opts.NewBrain
	f.selector.opts.NewBrain = func(ctx context.Context, cfg *config.Config) (brain.Provider, error) {
		p, err := makeBrain(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &imageGroupReply{p.(*groupReplyBrain)}, nil
	}
	ref := chatmedia.Ref{ID: "fixture", Name: "input.png", Format: "png"}
	if err := f.pipeline.RunTurnTextMode(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}), "What does everyone think about this image?"); err != nil {
		t.Fatal(err)
	}
	inputs, replies := 0, 0
	for _, turn := range f.pipeline.Brain.History() {
		if turn.Role == "user" && turn.Content == "What does everyone think about this image?" {
			inputs++
			if len(turn.Attachments) != 1 || turn.Attachments[0] != ref {
				t.Fatalf("lost image: %+v", turn)
			}
		}
		if turn.Role == "assistant" {
			replies++
		}
	}
	if inputs != 1 || replies < 2 {
		t.Fatalf("input duplicated or handoff missing: inputs=%d replies=%d", inputs, replies)
	}
}

// textOnlyGroupReply is a persona whose model cannot view images: it refuses a
// new picture, and answers a turn that already carries one.
type textOnlyGroupReply struct{ *groupReplyBrain }

func (b *textOnlyGroupReply) ValidateImages(context.Context, []chatmedia.Ref) error {
	return errors.New("model fixture does not support images; select a vision-capable Ollama model")
}
func (b *textOnlyGroupReply) ValidateAdmittedImages(context.Context, []chatmedia.Ref) error {
	return nil
}

// Like the Ollama provider, it reports the pictures it leaves out of a request.
func (b *textOnlyGroupReply) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	var unseen []chatmedia.Ref
	for _, turn := range b.History() {
		if turn.Role == "user" {
			unseen = append(unseen, turn.Attachments...)
		}
	}
	if len(unseen) > 0 && opts.OnImagesWithheld != nil {
		opts.OnImagesWithheld("fixture", unseen)
	}
	return b.groupReplyBrain.ThinkStream(ctx, input, opts)
}

// noImageShare stands in for a Muse Spark or Obey persona in a group: no image
// input, and a working view of other personas' lines without attachments.
type noImageShare struct{ *groupReplyBrain }

func (b *noImageShare) ContextHistory() []brain.Turn {
	view := b.History()
	for i := range view {
		view[i].Attachments = nil
	}
	return view
}

func TestGroupAnswersAPictureWhenOnePersonaCannotViewIt(t *testing.T) {
	for name, tc := range map[string]struct {
		samantha func(*groupReplyBrain) brain.Provider
		notice   string
	}{
		"text-only model": {
			samantha: func(b *groupReplyBrain) brain.Provider { return &textOnlyGroupReply{b} },
			notice:   "Samantha was not shown input.png, because fixture cannot view images.",
		},
		// Its view holds no picture, so there is none to tell the user about.
		"provider without image input": {samantha: func(b *groupReplyBrain) brain.Provider { return &noImageShare{b} }},
		"provider without image input holding the whole record": {
			samantha: func(b *groupReplyBrain) brain.Provider { return b },
			notice:   "Samantha was not shown input.png, because this provider cannot view images.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, _, _ := groupFixture(t)
			f.pipeline.Brain = &imageGroupReply{f.pipeline.Brain.(*groupReplyBrain)}
			makeBrain := f.selector.opts.NewBrain
			f.selector.opts.NewBrain = func(ctx context.Context, cfg *config.Config) (brain.Provider, error) {
				p, err := makeBrain(ctx, cfg)
				if err != nil {
					return nil, err
				}
				if cfg.AgentName == "Samantha" {
					return tc.samantha(p.(*groupReplyBrain)), nil
				}
				return &imageGroupReply{p.(*groupReplyBrain)}, nil
			}
			var mu sync.Mutex
			var notices []string
			events.Subscribe(f.pipeline.Events, func(e events.Info) {
				mu.Lock()
				defer mu.Unlock()
				if strings.Contains(e.Message, "was not shown") {
					notices = append(notices, e.Message)
				}
			})
			ref := chatmedia.Ref{ID: "fixture", Name: "input.png", Format: "png"}
			for _, input := range []string{"What does everyone think about this image?", "And what stands out to each of you?"} {
				ctx := t.Context()
				if strings.HasPrefix(input, "What") {
					ctx = chatmedia.WithInput(ctx, []chatmedia.Ref{ref})
				}
				if err := f.pipeline.RunTurnTextMode(ctx, input); err != nil {
					t.Fatalf("%q: %v", input, err)
				}
			}
			pictures, replies := 0, map[string]int{}
			for _, turn := range f.pipeline.Brain.History() {
				if turn.Role == "user" {
					pictures += len(turn.Attachments)
				}
				if turn.Role == "assistant" {
					replies[turn.PersonaDisplayName]++
				}
			}
			if pictures != 1 || replies["Jarvis"] != 2 || replies["Samantha"] != 2 {
				t.Fatalf("pictures on record=%d, replies=%v", pictures, replies)
			}
			mu.Lock()
			defer mu.Unlock()
			var want []string
			if tc.notice != "" {
				want = []string{tc.notice}
			}
			if !slices.Equal(notices, want) {
				t.Fatalf("notices = %q", notices)
			}
		})
	}
}

// A persona that views images is live; the picture is addressed to one that
// cannot. The turn is already in the conversation, so it is answered.
func TestPictureAddressedToATextOnlyPersonaIsAnswered(t *testing.T) {
	f, policy, _ := groupFixture(t)
	f.pipeline.Brain = &imageGroupReply{f.pipeline.Brain.(*groupReplyBrain)}
	makeBrain := f.selector.opts.NewBrain
	f.selector.opts.NewBrain = func(ctx context.Context, cfg *config.Config) (brain.Provider, error) {
		p, err := makeBrain(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &textOnlyGroupReply{p.(*groupReplyBrain)}, nil
	}
	policy.audience = func(_ context.Context, r discussion.AudienceRequest) (discussion.Audience, error) {
		for _, p := range r.Participants {
			if p.Name == "Samantha" {
				return discussion.Audience{Kind: "direct", Members: []string{p.Key}, Coverage: "none", Reason: "explicit"}, nil
			}
		}
		return discussion.Audience{Kind: "current", Coverage: "none", Reason: "none"}, nil
	}
	ref := chatmedia.Ref{ID: "fixture", Name: "input.png", Format: "png"}
	if err := f.pipeline.RunTurnTextMode(chatmedia.WithInput(t.Context(), []chatmedia.Ref{ref}), "Samantha, what is this?"); err != nil {
		t.Fatal(err)
	}
	history := f.pipeline.Brain.History()
	if last := history[len(history)-1]; last.Role != "assistant" || last.PersonaDisplayName != "Samantha" {
		t.Fatalf("last turn = %+v", last)
	}
}
