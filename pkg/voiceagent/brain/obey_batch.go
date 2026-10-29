package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type obeyBatch struct{ brain *ObeyBrain }

func newObeyBatch(cfg *config.Config) (*obeyBatch, error) {
	if err := checkObeyInstalled(); err != nil {
		return nil, err
	}
	if err := validateObeyConfig(cfg); err != nil {
		return nil, err
	}
	snapshot := cfg.Clone()
	b := &ObeyBrain{cfg: snapshot, client: obeySDKRunner{cfg: snapshot}}
	if _, err := b.runOptions(false); err != nil {
		return nil, err
	}
	return &obeyBatch{brain: b}, nil
}
func (b *obeyBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	opts, err := b.brain.runOptions(false)
	if err != nil {
		return BatchResult{}, err
	}
	result, err := b.brain.client.run(ctx, AssembleBatchPrompt(req), opts, func(ev obey.Event) error {
		if ev.Approval != nil {
			return fmt.Errorf("obey: batch requires approval for %s", ev.Approval.ToolName)
		}
		return nil
	})
	if err != nil {
		return BatchResult{}, fmt.Errorf("obey batch: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}
	if result == nil || strings.TrimSpace(result.Text) == "" {
		return BatchResult{}, fmt.Errorf("obey batch completed without text")
	}
	return BatchResult{Text: result.Text, Provider: "obey", Model: b.brain.cfg.ObeyModel}, nil
}
