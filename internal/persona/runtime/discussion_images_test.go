package personaruntime

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
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
