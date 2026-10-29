package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type codexRequest struct {
	Images                                       []any
	ThreadID, Prompt, System, Model, Effort, Cwd string
	Tools, Ephemeral                             bool
}
type codexResult struct {
	ThreadID                  string
	InputTokens, OutputTokens *int
}
type codexRunner interface {
	run(context.Context, codexRequest, func(string) error, StreamOptions) (codexResult, error)
}
type codexClient struct{ binary string }
type codexTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}
type codexNotice struct {
	ThreadID   string          `json:"threadId"`
	TurnID     string          `json:"turnId"`
	Delta      string          `json:"delta"`
	Turn       codexTurn       `json:"turn"`
	Item       codexToolNotice `json:"item"`
	TokenUsage struct {
		Total struct {
			InputTokens  *int `json:"inputTokens"`
			OutputTokens *int `json:"outputTokens"`
		} `json:"total"`
	} `json:"tokenUsage"`
}

func (client codexClient) run(ctx context.Context, req codexRequest, emit func(string) error, opts StreamOptions) (result codexResult, err error) {
	started := time.Now()
	c, err := startCodexRPC(ctx, client.binary, req.Cwd)
	if err != nil {
		return result, err
	}
	defer c.close()
	var turnID string
	defer func() {
		if err != nil && turnID != "" {
			interruptCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			// An acknowledgement is sufficient; close still reaps a hung child.
			_ = c.call(interruptCtx, "turn/interrupt", map[string]string{"threadId": result.ThreadID, "turnId": turnID}, nil, nil)
		}
		slog.Info("codex turn ended", "thread_id", result.ThreadID, "turn_id", turnID, "elapsed", time.Since(started), "failed", err != nil)
	}()
	setupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = c.initialize(setupCtx); err != nil {
		return result, err
	}
	policy, err := codexPolicy(setupCtx, c, req.Cwd, req.Tools)
	if err != nil {
		return result, err
	}
	sandbox := "read-only"
	if req.Tools {
		sandbox = "workspace-write"
	}
	params := map[string]any{"cwd": req.Cwd, "developerInstructions": req.System, "approvalPolicy": "never", "approvalsReviewer": "user", "sandbox": sandbox, "config": policy}
	if req.Model != "" && req.Model != "default" {
		params["model"] = req.Model
	}
	method := "thread/start"
	if req.ThreadID != "" {
		method = "thread/resume"
		params["threadId"] = req.ThreadID
		params["excludeTurns"] = true
	} else {
		params["ephemeral"] = req.Ephemeral
	}
	var thread struct {
		Sandbox struct {
			Type string `json:"type"`
		} `json:"sandbox"`
		ApprovalPolicy string `json:"approvalPolicy"`
		Thread         struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = c.call(setupCtx, method, params, &thread, nil); err != nil {
		return result, err
	}
	expectedSandbox := "readOnly"
	if req.Tools {
		expectedSandbox = "workspaceWrite"
	}
	if thread.Sandbox.Type != expectedSandbox || thread.ApprovalPolicy != "never" {
		return result, fmt.Errorf("codex did not apply the requested sandbox/approval policy")
	}
	result.ThreadID = thread.Thread.ID
	if result.ThreadID == "" {
		return result, errors.New("codex returned an empty thread ID")
	}
	slog.Info("codex thread ready", "thread_id", result.ThreadID, "resumed", req.ThreadID != "", "tools", req.Tools)
	params = map[string]any{"threadId": result.ThreadID, "input": append([]any{map[string]any{"type": "text", "text": req.Prompt}}, req.Images...)}
	if req.Effort != "" {
		params["effort"] = req.Effort
	}
	complete := false
	first := true
	toolStarts := map[string]time.Time{}
	var baseInput, baseOutput *int
	notice := func(m codexMessage) error {
		var n codexNotice
		if err := json.Unmarshal(m.Params, &n); err != nil {
			return fmt.Errorf("decode Codex %s: %w", m.Method, err)
		}
		if n.ThreadID != result.ThreadID {
			return nil
		}
		if m.Method == "turn/started" && turnID == "" {
			turnID = n.Turn.ID
		}
		if n.TurnID != "" && n.TurnID != turnID {
			return nil
		}
		switch m.Method {
		case "item/agentMessage/delta":
			if first && n.Delta != "" {
				slog.Info("codex first text", "elapsed", time.Since(started))
				first = false
			}
			return emit(n.Delta)
		case "thread/tokenUsage/updated":
			// Total is cumulative within the native thread. Baseline is captured on
			// resume below; latest cumulative minus baseline is this operation's usage.
			result.InputTokens = codexTokenDelta(n.TokenUsage.Total.InputTokens, baseInput)
			result.OutputTokens = codexTokenDelta(n.TokenUsage.Total.OutputTokens, baseOutput)
		case "item/started", "item/completed":
			if !codexToolItem(n.Item.Type) {
				return nil
			}
			if !req.Tools {
				return errors.New("codex attempted a tool during a tools-disabled turn")
			}
			call := n.Item.call()
			if m.Method == "item/started" {
				toolStarts[call.ID] = time.Now()
				if opts.OnToolStart != nil {
					opts.OnToolStart(call)
				}
			} else if opts.OnToolEnd != nil {
				elapsed := time.Duration(0)
				if start, ok := toolStarts[call.ID]; ok {
					elapsed = time.Since(start)
				}
				delete(toolStarts, call.ID)
				opts.OnToolEnd(n.Item.result(elapsed))
			}
		case "turn/completed":
			if n.Turn.ID != turnID {
				return nil
			}
			complete = true
			if n.Turn.Status != "completed" {
				detail := n.Turn.Status
				if n.Turn.Error != nil {
					detail += " — " + n.Turn.Error.Message
				}
				return fmt.Errorf("codex turn %s", detail)
			}
		}
		return nil
	}
	// Native totals include previous turns. On resume no reliable pre-turn total
	// is exposed by thread/resume; leave usage unknown instead of overcharging.
	if req.ThreadID != "" {
		unknown := -1
		baseInput = &unknown
		baseOutput = &unknown
	}
	var turn struct {
		Turn codexTurn `json:"turn"`
	}
	if err = c.call(ctx, "turn/start", params, &turn, notice); err != nil {
		return result, err
	}
	if turnID == "" {
		turnID = turn.Turn.ID
	}
	if turnID == "" {
		return result, errors.New("codex returned an empty turn ID")
	}
	for !complete {
		var m codexMessage
		m, err = c.next(ctx)
		if err != nil {
			return result, err
		}
		if m.Method == "" {
			continue
		}
		if len(m.ID) > 0 {
			err = c.reject(ctx, m)
			return result, err
		}
		if err = notice(m); err != nil {
			return result, err
		}
	}
	return result, nil
}
func codexTokenDelta(total, base *int) *int {
	if total == nil || base != nil && *base < 0 {
		return nil
	}
	n := *total
	if base != nil {
		n -= *base
	}
	if n < 0 {
		return nil
	}
	return &n
}
func codexToolItem(kind string) bool {
	switch kind {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "imageView", "collabAgentToolCall":
		return true
	}
	return false
}

// CodexModels returns the CLI's catalog, not a guarantee of account entitlement.
func CodexModels(ctx context.Context) ([]string, error) {
	client, err := newCodexClient()
	if err != nil {
		return nil, err
	}
	c, err := startCodexRPC(ctx, client.binary, "")
	if err != nil {
		return nil, err
	}
	defer c.close()
	if err = c.initialize(ctx); err != nil {
		return nil, err
	}
	var models []string
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var r struct {
			Data []struct {
				Model string `json:"model"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err = c.call(ctx, "model/list", params, &r, nil); err != nil {
			return nil, err
		}
		for _, m := range r.Data {
			if m.Model != "" {
				models = append(models, m.Model)
			}
		}
		if r.NextCursor == nil || *r.NextCursor == "" {
			return models, nil
		}
		if *r.NextCursor == cursor {
			return nil, errors.New("codex model catalog cursor repeated")
		}
		cursor = *r.NextCursor
	}
	return nil, errors.New("codex model catalog exceeded pagination limit")
}
