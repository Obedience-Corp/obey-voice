package brain

import (
	"context"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
)

func (b *Brain) ClassifyLiveRoute(ctx context.Context, req LiveRouteRequest) (StreamResult, error) {
	return classifyHarnessRoute(ctx, b.cfg, req, func(ctx context.Context, req BatchRequest) (string, error) {
		b.mu.Lock()
		opts := b.runOptions(claude.JSONOutput, false)
		b.mu.Unlock()
		opts.SystemPrompt, opts.ResumeID, opts.NoSessionPersistence = req.SystemPrompt, "", true
		r, err := b.client.RunPromptCtx(ctx, req.Text, opts)
		if err != nil {
			return "", claudeFailure("claude routing", err)
		}
		if r == nil {
			return "", fmt.Errorf("claude routing returned no result")
		}
		return r.Result, nil
	})
}

func (b *CodexBrain) ClassifyLiveRoute(ctx context.Context, req LiveRouteRequest) (StreamResult, error) {
	// A route answers this request without a streamed request to record it.
	b.mu.Lock()
	b.request = routedRequest(b.canonical)
	b.mu.Unlock()
	return classifyHarnessRoute(ctx, b.cfg, req, func(ctx context.Context, req BatchRequest) (string, error) {
		var text strings.Builder
		_, err := b.client.run(ctx, codexRequest{System: req.SystemPrompt, Prompt: req.Text, Model: b.cfg.CodexModel,
			Effort: b.cfg.CodexReasoningEffort, Cwd: config.WorkDirFrom(b.cfg), Ephemeral: true}, func(s string) error {
			if text.Len()+len(s) > 4096 {
				return fmt.Errorf("route decision exceeded the response size limit")
			}
			text.WriteString(s)
			return nil
		}, StreamOptions{})
		return text.String(), err
	})
}

func (b *CursorBrain) ClassifyLiveRoute(ctx context.Context, req LiveRouteRequest) (StreamResult, error) {
	return classifyHarnessRoute(ctx, b.cfg, req, func(ctx context.Context, req BatchRequest) (string, error) {
		b.mu.Lock()
		opts := b.askOptions(false)
		b.mu.Unlock()
		opts.Resume = ""
		r, err := b.client.AskCtx(ctx, AssembleBatchPrompt(req), opts)
		if err != nil {
			return "", err
		}
		if r == nil {
			return "", fmt.Errorf("cursor routing returned no result")
		}
		return r.Result, nil
	})
}

func (b *GrokBrain) ClassifyLiveRoute(ctx context.Context, req LiveRouteRequest) (StreamResult, error) {
	return classifyHarnessRoute(ctx, b.cfg, req, func(ctx context.Context, req BatchRequest) (string, error) {
		b.mu.Lock()
		opts := b.runOptions(grok.JSONOutput, false)
		b.mu.Unlock()
		opts.SystemPromptOverride, opts.ResumeID = req.SystemPrompt, ""
		r, err := b.client.RunPromptCtx(ctx, req.Text, opts)
		if err != nil {
			return "", err
		}
		if r == nil {
			return "", fmt.Errorf("grok routing returned no result")
		}
		return r.Text, nil
	})
}

func (b *MuseSparkBrain) ClassifyLiveRoute(ctx context.Context, req LiveRouteRequest) (StreamResult, error) {
	b.mu.Lock()
	b.request = routedRequest(b.canonical)
	b.mu.Unlock()
	return classifyHarnessRoute(ctx, b.cfg, req, func(ctx context.Context, req BatchRequest) (string, error) {
		r, err := b.client.RunPromptCtx(ctx, AssembleBatchPrompt(req), museRunOptions(b.cfg, false))
		if err != nil {
			return "", err
		}
		if r == nil || r.Terminal != "completed" {
			return "", fmt.Errorf("muse routing did not complete")
		}
		return r.Text, nil
	})
}

func (b *ObeyBrain) ClassifyLiveRoute(ctx context.Context, req LiveRouteRequest) (StreamResult, error) {
	b.mu.Lock()
	b.request = routedRequest(b.canonical)
	b.mu.Unlock()
	return classifyHarnessRoute(ctx, b.cfg, req, func(ctx context.Context, req BatchRequest) (string, error) {
		opts, err := b.runOptions(false)
		if err != nil {
			return "", err
		}
		r, err := b.client.run(ctx, AssembleBatchPrompt(req), opts, func(e obey.Event) error {
			if e.Approval != nil {
				return fmt.Errorf("obey routing requires approval for %s", e.Approval.ToolName)
			}
			if e.ToolCall != nil {
				return fmt.Errorf("obey routing attempted a tool call")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if r == nil {
			return "", fmt.Errorf("obey routing returned no result")
		}
		return r.Text, nil
	})
}
