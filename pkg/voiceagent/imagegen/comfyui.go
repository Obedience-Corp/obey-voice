// Package imagegen makes pictures for models that cannot draw. ComfyUI is
// the only backend: it runs a workflow graph and the picture is read back.
package imagegen

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Request is one picture to make.
type Request struct {
	Prompt string
	Width  int
	Height int
}

// Generator makes one PNG picture.
type Generator interface {
	Generate(ctx context.Context, req Request) ([]byte, error)
}

// defaultWorkflow is the graph used when no workflow file is configured.
//
//go:embed z_image_turbo.json
var defaultWorkflow []byte

// Placeholders a workflow graph marks its inputs with. A string value equal
// to a number placeholder becomes that number; {{prompt}} is replaced inside
// any string.
const (
	promptPlaceholder = "{{prompt}}"
	widthPlaceholder  = "{{width}}"
	heightPlaceholder = "{{height}}"
	seedPlaceholder   = "{{seed}}"
)

// pollEvery is how often a queued graph's history is read. It paces the
// reads; it never ends the wait.
const pollEvery = 500 * time.Millisecond

// maxPictureBytes bounds one picture read back from the server.
const maxPictureBytes = 32 << 20

// ComfyUI runs a workflow graph on a ComfyUI server.
type ComfyUI struct {
	base     string
	workflow string
	client   *http.Client
}

// NewComfyUI addresses the server at base. workflow is the path of a graph
// in ComfyUI's API format, read on every request so edits apply at once;
// empty uses the built-in graph.
func NewComfyUI(base, workflow string) (*ComfyUI, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("ComfyUI address %q is not an http or https URL", base)
	}
	return &ComfyUI{base: strings.TrimRight(u.String(), "/"), workflow: strings.TrimSpace(workflow), client: &http.Client{}}, nil
}

// Generate queues the graph with req filled in, waits for it however long
// it takes, and returns the first picture it produced. A cancelled ctx
// removes or interrupts this graph only, never other work on the server.
func (c *ComfyUI) Generate(ctx context.Context, req Request) ([]byte, error) {
	graph, err := c.graph(req)
	if err != nil {
		return nil, err
	}
	id, err := c.queue(ctx, graph)
	if err != nil {
		return nil, err
	}
	done := false
	defer func() {
		if !done {
			c.cancel(id)
		}
	}()
	ref, err := c.wait(ctx, id)
	if err != nil {
		return nil, err
	}
	done = true
	defer c.forget(id)
	return c.picture(ctx, ref)
}

func (c *ComfyUI) graph(req Request) (map[string]any, error) {
	raw, source := defaultWorkflow, "built-in workflow"
	if c.workflow != "" {
		data, err := os.ReadFile(c.workflow)
		if err != nil {
			return nil, fmt.Errorf("read image workflow: %w", err)
		}
		raw, source = data, c.workflow
	}
	var graph map[string]any
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("image workflow %s is not JSON: %w", source, err)
	}
	if nodes, ok := graph["prompt"].(map[string]any); ok && len(graph) <= 2 {
		graph = nodes
	}
	seed := rand.Int64N(1 << 50)
	found := false
	filled := fill(graph, func(s string) any {
		switch s {
		case widthPlaceholder:
			return req.Width
		case heightPlaceholder:
			return req.Height
		case seedPlaceholder:
			return seed
		}
		if strings.Contains(s, promptPlaceholder) {
			found = true
			return strings.ReplaceAll(s, promptPlaceholder, req.Prompt)
		}
		return s
	})
	if !found {
		return nil, fmt.Errorf("image workflow %s has no %s input", source, promptPlaceholder)
	}
	return filled.(map[string]any), nil
}

func fill(v any, str func(string) any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = fill(e, str)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = fill(e, str)
		}
		return out
	case string:
		return str(t)
	}
	return v
}

type queueReply struct {
	PromptID   string `json:"prompt_id"`
	Error      any    `json:"error"`
	NodeErrors map[string]struct {
		ClassType string `json:"class_type"`
		Errors    []struct {
			Message string `json:"message"`
			Details string `json:"details"`
		} `json:"errors"`
	} `json:"node_errors"`
}

func (c *ComfyUI) queue(ctx context.Context, graph map[string]any) (string, error) {
	body, err := json.Marshal(map[string]any{"prompt": graph})
	if err != nil {
		return "", fmt.Errorf("encode image workflow: %w", err)
	}
	data, status, err := c.do(ctx, http.MethodPost, "/prompt", body)
	if err != nil {
		return "", err
	}
	var reply queueReply
	if err := json.Unmarshal(data, &reply); err != nil && status == http.StatusOK {
		return "", fmt.Errorf("ComfyUI answered /prompt with something other than JSON: %w", err)
	}
	if status == http.StatusOK && reply.PromptID != "" {
		return reply.PromptID, nil
	}
	return "", refusal(status, reply, data)
}

// refusal names what ComfyUI rejected, such as a model file that is not
// installed, without the full list of the files that are.
func refusal(status int, reply queueReply, data []byte) error {
	var problems []string
	for _, node := range reply.NodeErrors {
		for _, e := range node.Errors {
			detail := e.Details
			if i := strings.Index(detail, " not in ["); i >= 0 {
				detail = detail[:i] + " is not installed"
			}
			problems = append(problems, strings.TrimSpace(node.ClassType+": "+e.Message+": "+detail))
		}
	}
	sort.Strings(problems)
	if len(problems) == 0 {
		switch e := reply.Error.(type) {
		case map[string]any:
			if m, _ := e["message"].(string); m != "" {
				problems = append(problems, m)
			}
		case string:
			problems = append(problems, e)
		}
	}
	if len(problems) == 0 {
		problems = append(problems, strings.TrimSpace(clip(string(data), 300)))
	}
	return fmt.Errorf("ComfyUI refused the image workflow (HTTP %d): %s", status, strings.Join(problems, "; "))
}

type pictureRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

type historyEntry struct {
	Status struct {
		StatusStr string `json:"status_str"`
		Completed bool   `json:"completed"`
		Messages  []json.RawMessage
	} `json:"status"`
	Outputs map[string]struct {
		Images []pictureRef `json:"images"`
	} `json:"outputs"`
}

func (c *ComfyUI) wait(ctx context.Context, id string) (pictureRef, error) {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		data, status, err := c.do(ctx, http.MethodGet, "/history/"+url.PathEscape(id), nil)
		if err != nil {
			return pictureRef{}, err
		}
		if status != http.StatusOK {
			return pictureRef{}, fmt.Errorf("ComfyUI history: HTTP %d: %s", status, clip(string(data), 200))
		}
		var history map[string]historyEntry
		if err := json.Unmarshal(data, &history); err != nil {
			return pictureRef{}, fmt.Errorf("ComfyUI history is not JSON: %w", err)
		}
		if entry, ok := history[id]; ok {
			if entry.Status.StatusStr == "error" {
				return pictureRef{}, fmt.Errorf("ComfyUI failed to run the image workflow: %s", executionError(entry.Status.Messages))
			}
			if entry.Status.Completed {
				keys := make([]string, 0, len(entry.Outputs))
				for k := range entry.Outputs {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					if images := entry.Outputs[k].Images; len(images) > 0 {
						return images[0], nil
					}
				}
				return pictureRef{}, errors.New("the image workflow finished without producing a picture")
			}
		}
		select {
		case <-ctx.Done():
			return pictureRef{}, ctx.Err()
		case <-tick.C:
		}
	}
}

// executionError is the exception message of an execution_error status
// message, which ComfyUI sends as ["execution_error", {...}].
func executionError(messages []json.RawMessage) string {
	for _, raw := range messages {
		var pair []json.RawMessage
		if json.Unmarshal(raw, &pair) != nil || len(pair) != 2 {
			continue
		}
		var kind string
		var detail struct {
			NodeType string `json:"node_type"`
			Message  string `json:"exception_message"`
		}
		if json.Unmarshal(pair[0], &kind) != nil || kind != "execution_error" || json.Unmarshal(pair[1], &detail) != nil {
			continue
		}
		return strings.TrimSpace(detail.NodeType + ": " + clip(strings.TrimSpace(detail.Message), 300))
	}
	return "no error message"
}

func (c *ComfyUI) picture(ctx context.Context, ref pictureRef) ([]byte, error) {
	q := url.Values{"filename": {ref.Filename}, "subfolder": {ref.Subfolder}, "type": {ref.Type}}
	data, status, err := c.do(ctx, http.MethodGet, "/view?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("read picture from ComfyUI: HTTP %d", status)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || format != "png" {
		return nil, errors.New("ComfyUI returned a file that is not a PNG picture")
	}
	return data, nil
}

// cancel removes this graph from the queue and interrupts it only if it is
// the one running. It runs after ctx ended, so it has its own short limit.
func (c *ComfyUI) cancel(id string) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_, _, _ = c.do(ctx, http.MethodPost, "/queue", mustJSON(map[string]any{"delete": []string{id}}))
	_, _, _ = c.do(ctx, http.MethodPost, "/interrupt", mustJSON(map[string]any{"prompt_id": id}))
}

// forget drops this graph's history entry once its picture is read.
func (c *ComfyUI) forget(id string) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_, _, _ = c.do(ctx, http.MethodPost, "/history", mustJSON(map[string]any{"delete": []string{id}}))
}

func (c *ComfyUI) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("ComfyUI request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, fmt.Errorf("ComfyUI at %s is not answering: %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPictureBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read ComfyUI reply: %w", err)
	}
	if len(data) > maxPictureBytes {
		return nil, resp.StatusCode, fmt.Errorf("ComfyUI reply is larger than %d MiB", maxPictureBytes>>20)
	}
	return data, resp.StatusCode, nil
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
