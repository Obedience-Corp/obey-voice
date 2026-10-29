package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/ollama/ollama/api"
)

// DiscussionProvider pins a private inference transport independently of speaker changes.
type DiscussionProvider interface {
	DiscussionDecider() (discussion.Decider, error)
}

type ollamaDiscussion struct {
	client    *api.Client
	model     string
	options   map[string]any
	keepAlive *api.Duration
	timeout   time.Duration
	numCtx    int
}

func (o *OllamaBrain) DiscussionDecider() (discussion.Decider, error) {
	u, err := url.Parse(o.cfg.OllamaHost)
	if err != nil || (u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback()) {
		return nil, errors.New("group participation requires a local Ollama host")
	}
	o.stateMu.Lock()
	defer o.stateMu.Unlock()
	opts := o.requestOptionsFor(o.contextHighWater)
	if opts == nil {
		opts = map[string]any{}
	}
	return &ollamaDiscussion{client: o.client, model: o.model, options: opts, keepAlive: o.keepAlive, timeout: config.PersonaRoutingTimeoutOf(o.cfg), numCtx: o.numCtxFor(o.contextHighWater)}, nil
}
func (d *ollamaDiscussion) call(ctx context.Context, point string, input any, persona *discussion.Persona, out any) error {
	limit := 10 * time.Second
	tokens := 128
	if point == "audience" {
		limit = d.timeout
		tokens = 256
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if err := discussion.ReserveRequest(ctx); err != nil {
		return err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode discussion %s: %w", point, err)
	}
	instruction := discussionInstructions[point]
	model := d.model
	if persona != nil {
		instruction = persona.Prompt + "\n\n" + instruction
		if persona.Model != "" {
			model = persona.Model
		}
	}
	if len(payload) > 16384 {
		return errors.New("discussion input exceeds 16 KiB")
	}
	if d.numCtx > 0 && (len(payload)+len(instruction)+3)/4 > d.numCtx*9/10 {
		return errors.New("discussion prompt exceeds safe context budget")
	}
	options := map[string]any{}
	for k, v := range d.options {
		options[k] = v
	}
	options["temperature"], options["seed"], options["num_predict"] = 0, 42, tokens
	stream := false
	req := &api.ChatRequest{Model: model, Messages: []api.Message{{Role: "system", Content: instruction}, {Role: "user", Content: string(payload)}}, Format: discussionSchemas[point], Stream: &stream, Think: &api.ThinkValue{Value: false}, Options: options, KeepAlive: d.keepAlive}
	var raw strings.Builder
	err = d.client.Chat(ctx, req, func(r api.ChatResponse) error {
		if len(r.Message.ToolCalls) > 0 {
			return errors.New("tools are disabled for private discussion decisions")
		}
		if r.DoneReason == "length" {
			return errors.New("private discussion decision was truncated")
		}
		if raw.Len()+len(r.Message.Content) > 4096 {
			return errors.New("private discussion decision too large")
		}
		raw.WriteString(r.Message.Content)
		return nil
	})
	if err != nil {
		return fmt.Errorf("discussion %s: %w", point, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw.String()), &fields); err != nil {
		return fmt.Errorf("invalid discussion %s: %w", point, err)
	}
	var shape struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(discussionSchemas[point], &shape)
	for _, key := range shape.Required {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("discussion %s requires nonnull field %s", point, key)
		}
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid discussion %s: %w", point, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("discussion decision contains trailing data")
	}
	return ctx.Err()
}
func (d *ollamaDiscussion) Audience(ctx context.Context, r discussion.AudienceRequest) (discussion.Audience, error) {
	var a discussion.Audience
	err := d.call(ctx, "audience", r, nil, &a)
	if err == nil {
		err = a.Validate(r.Participants)
	}
	return a, err
}
func (d *ollamaDiscussion) Next(ctx context.Context, r discussion.Context) (discussion.Next, error) {
	var n discussion.Next
	err := d.call(ctx, "next", r, nil, &n)
	if err == nil {
		err = n.Validate(r)
	}
	return n, err
}
func (d *ollamaDiscussion) Check(ctx context.Context, r discussion.Context, p discussion.Persona) (discussion.Contribution, error) {
	payload := struct {
		discussion.Context
		Candidate string               `json:"candidate"`
		Working   []discussion.Message `json:"working_context"`
	}{r, p.Key, p.Messages}
	var c discussion.Contribution
	err := d.call(ctx, "participant", payload, &p, &c)
	return c, err
}

// DiscussionPersona resolves the same persona document as a speaking brain, without
// constructing a worker, starting embeddings, changing selection or mutating history.
func DiscussionPersona(cfg *config.Config, key string, working []Turn) (discussion.Persona, error) {
	prompt, err := personaSystemPrompt(cfg)
	if err != nil {
		return discussion.Persona{}, err
	}
	p := discussion.Persona{Key: key, Prompt: assembleSystemPrompt(prompt, config.WorkDirFrom(cfg), nil, "", cfg), Model: cfg.OllamaModel}
	for _, t := range working {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		text := []rune(CleanHistoricalAssistantText(t.Content))
		if len(text) > 1000 {
			text = text[:1000]
		}
		account, name := "user", "Human"
		if t.Role == "assistant" {
			account, name = "agent", t.PersonaDisplayName
		}
		p.Messages = append(p.Messages, discussion.Message{Name: name, Account: account, ResponseID: t.ResponseID, Text: string(text)})
	}
	if len(p.Messages) > 8 {
		p.Messages = p.Messages[len(p.Messages)-8:]
	}
	return p, nil
}
