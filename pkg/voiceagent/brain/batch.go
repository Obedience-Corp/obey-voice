package brain

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"
	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// BatchRequest describes a single-shot, history-free text transformation.
// The caller supplies the full system prompt; no persona is injected and no
// interactive session history is read or written.
type BatchRequest struct {
	SystemPrompt  string
	StylePrompt   string
	Pronunciation string
	SectionTitle  string
	Text          string
	Metadata      map[string]string
}

// BatchResult carries the transformed text and the identity of the provider
// and model that produced it.
type BatchResult struct {
	Text     string
	Provider string
	Model    string
}

// BatchProvider transforms text in a single self-contained call.
type BatchProvider interface {
	Transform(ctx context.Context, req BatchRequest) (BatchResult, error)
}

// AssembleBatchPrompt renders req into its canonical prompt string: system
// prompt, style prompt, pronunciation guide, section title, metadata (sorted
// by key), then the text, joined by blank lines with empty parts skipped.
// Downstream caching hashes this string, so the order and formatting must
// stay stable. Adapters send the same content, with the system prompt routed
// through the provider's native system slot and the rest as the user prompt.
func AssembleBatchPrompt(req BatchRequest) string {
	return joinPromptParts(req.SystemPrompt, batchPromptBody(req))
}

// batchPromptBody assembles every part after the system prompt — the portion
// adapters send as the user prompt.
func batchPromptBody(req BatchRequest) string {
	parts := []string{req.StylePrompt, req.Pronunciation}
	if req.SectionTitle != "" {
		parts = append(parts, "Section: "+req.SectionTitle)
	}
	if len(req.Metadata) > 0 {
		keys := make([]string, 0, len(req.Metadata))
		for k := range req.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines := make([]string, len(keys))
		for i, k := range keys {
			lines[i] = k + ": " + req.Metadata[k]
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	parts = append(parts, req.Text)
	return joinPromptParts(parts...)
}

func joinPromptParts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// ollamaBatch adapts the Ollama chat client to BatchProvider.
type ollamaBatch struct {
	client    *api.Client
	model     string
	options   map[string]any
	keepAlive *api.Duration
	think     *api.ThinkValue
}

// newOllamaBatch reuses the interactive constructor for its connection and
// model checks, then keeps only what a stateless call needs — batch calls never
// touch the interactive brain or its history.
//
// It carries the interactive brain's request policy on purpose. Batch runs
// against the same Ollama and usually the same model as the conversation, and
// Ollama reloads the runner whenever a request's options differ from the loaded
// ones: a batch call sent bare would evict the warm conversational runner, then
// the next spoken turn would evict it back. The think value matters for the
// same reason it does interactively — omitting it lets a reasoning model think,
// and batch output is written to a file, not spoken, so nobody would hear the
// reasoning arrive; they would only see a slow, padded transform.
func newOllamaBatch(cfg *config.Config) (*ollamaBatch, error) {
	b, err := NewOllama(cfg)
	if err != nil {
		return nil, err
	}
	think := resolveOllamaThink(cfg.OllamaThink, b.model)
	b.maybeWarnThinkOff(think)
	return &ollamaBatch{
		client:    b.client,
		model:     b.model,
		options:   b.requestOptions(),
		keepAlive: b.keepAlive,
		think:     think,
	}, nil
}

func (o *ollamaBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}

	var messages []api.Message
	if req.SystemPrompt != "" {
		messages = append(messages, api.Message{Role: "system", Content: req.SystemPrompt})
	}
	messages = append(messages, api.Message{Role: "user", Content: batchPromptBody(req)})

	stream := false
	chatReq := &api.ChatRequest{
		Model:     o.model,
		Messages:  messages,
		Stream:    &stream,
		Options:   o.options,
		KeepAlive: o.keepAlive,
		Think:     o.think,
	}

	var response api.Message
	err := o.client.Chat(ctx, chatReq, func(resp api.ChatResponse) error {
		response = resp.Message
		return nil
	})
	if err != nil {
		return BatchResult{}, fmt.Errorf("ollama batch: %w", err)
	}

	text := strings.TrimSpace(response.Content)
	if text == "" {
		return BatchResult{}, fmt.Errorf("ollama batch: empty response from model %s", o.model)
	}
	return BatchResult{Text: text, Provider: "ollama", Model: o.model}, nil
}

// grokPromptRunner is the slice of the grok client the batch adapter needs.
type grokPromptRunner interface {
	RunPromptCtx(ctx context.Context, prompt string, opts *grok.RunOptions) (*grok.GrokResult, error)
}

// grokBatch adapts the grok CLI client to BatchProvider.
type grokBatch struct {
	runner grokPromptRunner
	model  string
}

func newGrokBatch(cfg *config.Config) (*grokBatch, error) {
	g, err := NewGrok(cfg)
	if err != nil {
		return nil, err
	}
	return &grokBatch{runner: g.client, model: cfg.GrokModel}, nil
}

func (g *grokBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}

	opts := &grok.RunOptions{
		Format:               grok.PlainOutput,
		SystemPromptOverride: req.SystemPrompt,
		Model:                g.model,
	}

	result, err := g.runner.RunPromptCtx(ctx, batchPromptBody(req), opts)
	if err != nil {
		return BatchResult{}, fmt.Errorf("grok batch: %w", err)
	}

	text := strings.TrimSpace(result.Text)
	if text == "" {
		return BatchResult{}, fmt.Errorf("grok batch: empty response from model %s", modelIDOrDefault(g.model))
	}
	return BatchResult{Text: text, Provider: "grok", Model: modelIDOrDefault(g.model)}, nil
}

// cursorPromptRunner is the slice of the cursor-agent client the batch adapter needs.
type cursorPromptRunner interface {
	AskCtx(ctx context.Context, prompt string, opts *cursor.AskOptions) (*cursor.AskResult, error)
}

// cursorBatch adapts the cursor-agent CLI client to BatchProvider.
type cursorBatch struct {
	runner cursorPromptRunner
	model  string
}

func newCursorBatch(cfg *config.Config) (*cursorBatch, error) {
	b, err := NewCursor(cfg)
	if err != nil {
		return nil, err
	}
	return &cursorBatch{runner: b.client, model: cfg.CursorModel}, nil
}

func (c *cursorBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}

	opts := &cursor.AskOptions{
		// Batch is a pure text transform: ask mode refuses edits, and Trust
		// avoids a workspace-trust prompt stalling a headless call.
		Mode:  cursor.ModeAsk,
		Trust: true,
		Model: c.model,
	}

	result, err := c.runner.AskCtx(ctx, AssembleBatchPrompt(req), opts)
	if err != nil {
		return BatchResult{}, fmt.Errorf("cursor batch: %w", err)
	}
	if result == nil {
		return BatchResult{}, fmt.Errorf("cursor batch: empty result from model %s", modelIDOrDefault(c.model))
	}

	text := strings.TrimSpace(result.Result)
	if text == "" {
		return BatchResult{}, fmt.Errorf("cursor batch: empty response from model %s", modelIDOrDefault(c.model))
	}
	return BatchResult{Text: text, Provider: "cursor", Model: modelIDOrDefault(c.model)}, nil
}

// claudePromptRunner is the slice of the claude client the batch adapter needs.
type claudePromptRunner interface {
	RunPromptCtx(ctx context.Context, prompt string, opts *claude.RunOptions) (*claude.ClaudeResult, error)
}

// claudeBatch adapts the claude CLI client to BatchProvider.
type claudeBatch struct {
	runner claudePromptRunner
	model  string
}

// newClaudeBatch reuses the interactive constructor for its claude-CLI lookup
// (and its "claude CLI not found" error shape), then keeps only the client —
// batch calls inject no persona and touch no history.
func newClaudeBatch(cfg *config.Config) (*claudeBatch, error) {
	b, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &claudeBatch{runner: b.client, model: claudeModel(cfg)}, nil
}

func (c *claudeBatch) Transform(ctx context.Context, req BatchRequest) (BatchResult, error) {
	if err := ctx.Err(); err != nil {
		return BatchResult{}, err
	}

	opts := &claude.RunOptions{
		Format: claude.TextOutput,
		// Caller-supplied system prompt only — no GetSystemPrompt persona.
		SystemPrompt: req.SystemPrompt,
		// Batch is a pure text transform. Unlike the interactive provider it
		// must not grant tool execution, so it uses the default permission mode
		// (headless print mode never auto-runs tools) rather than
		// bypassPermissions, and wires no tools.
		PermissionMode: claude.PermissionModeDefault,
		Model:          c.model,
	}

	result, err := c.runner.RunPromptCtx(ctx, batchPromptBody(req), opts)
	if err != nil {
		return BatchResult{}, fmt.Errorf("claude batch: %w", err)
	}

	text := strings.TrimSpace(result.Result)
	if text == "" {
		return BatchResult{}, fmt.Errorf("claude batch: empty response from model %s", modelIDOrDefault(c.model))
	}
	return BatchResult{Text: text, Provider: "claude", Model: modelIDOrDefault(c.model)}, nil
}

// modelIDOrDefault reports model, or "default" when the provider CLI selects
// the model itself — neither the grok nor claude CLI returns the resolved name.
func modelIDOrDefault(model string) string {
	if model == "" {
		return "default"
	}
	return model
}
