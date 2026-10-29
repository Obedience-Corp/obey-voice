package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type codexBatch struct {
	client codexRunner
	cfg    *config.Config
}

func newCodexBatch(cfg *config.Config) (BatchProvider, error) {
	client, err := newCodexClient()
	if err != nil {
		return nil, err
	}
	return &codexBatch{client: client, cfg: cfg}, nil
}
func (c *codexBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	var text strings.Builder
	_, err := c.client.run(ctx, codexRequest{System: req.SystemPrompt, Prompt: batchPromptBody(req), Model: c.cfg.CodexModel, Effort: c.cfg.CodexReasoningEffort, Cwd: config.WorkDirFrom(c.cfg), Ephemeral: true}, func(s string) error { text.WriteString(s); return nil }, StreamOptions{})
	if err != nil {
		return BatchResult{}, err
	}
	if strings.TrimSpace(text.String()) == "" {
		return BatchResult{}, fmt.Errorf("codex batch completed without text")
	}
	return BatchResult{Text: text.String(), Provider: "codex", Model: c.cfg.CodexModel}, nil
}
