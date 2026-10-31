package audiojob

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

func (p *ManagedRuntime) Analyze(ctx context.Context, wav []byte, task, instruction string) (string, TextUsage, error) {
	var usage TextUsage
	if !p.audioInput || !p.textOutput {
		return "", usage, fmt.Errorf("the selected runtime does not report audio input and text output")
	}
	if len(wav) > 44+int(WindowSamples)*2 {
		return "", usage, fmt.Errorf("audio window exceeds its bounded input")
	}
	content := []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": base64.StdEncoding.EncodeToString(wav), "format": "wav"}}}
	if task == "understanding" {
		content = append(content, map[string]any{"type": "text", "text": instruction})
	}
	body := map[string]any{"model": ManagedModel, "messages": []any{map[string]any{"role": "system", "content": textPrompt(task)}, map[string]any{"role": "user", "content": content}}, "stream": false, "max_tokens": MaxTextTokens, "temperature": 0, "seed": 42, "cache_prompt": false, "n_keep": -1, "modalities": []string{"text"}, "chat_template_kwargs": map[string]any{"enable_thinking": false}}
	data, err := json.Marshal(body)
	if err != nil {
		return "", usage, err
	}
	var tokens struct {
		Input  int    `json:"input_tokens"`
		Object string `json:"object"`
	}
	if err = p.textJSON(ctx, "/v1/chat/completions/input_tokens", data, &tokens); err != nil {
		return "", usage, err
	}
	if tokens.Object != "response.input_tokens" || tokens.Input <= 0 || tokens.Input > ManagedContextTokens-MaxTextTokens {
		return "", usage, fmt.Errorf("complete audio window does not fit the verified runtime context")
	}
	var result struct {
		Model       string `json:"model"`
		Fingerprint string `json:"system_fingerprint"`
		Choices     []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Role     string          `json:"role"`
				Content  string          `json:"content"`
				Tools    json.RawMessage `json:"tool_calls"`
				Function json.RawMessage `json:"function_call"`
				Refusal  string          `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Usage TextUsage `json:"usage"`
	}
	if err = p.textJSON(ctx, "/v1/chat/completions", data, &result); err != nil {
		return "", usage, err
	}
	if result.Model != ManagedModel || result.Fingerprint != ManagedVersion {
		return "", usage, fmt.Errorf("audio text response has unexpected model/runtime provenance")
	}
	if len(result.Choices) != 1 || result.Choices[0].Finish != "stop" {
		return "", usage, fmt.Errorf("audio text response did not complete without truncation")
	}
	message := result.Choices[0].Message
	if message.Role != "assistant" || hasToolResult(message.Tools) || hasToolResult(message.Function) || message.Refusal != "" {
		return "", usage, fmt.Errorf("audio text response is not a plain assistant finding")
	}
	if result.Usage.PromptTokens != tokens.Input || result.Usage.CompletionTokens < 1 || result.Usage.CompletionTokens > MaxTextTokens {
		return "", usage, fmt.Errorf("audio text context usage does not match the complete input")
	}
	if strings.TrimSpace(message.Content) == "" || !utf8.ValidString(message.Content) || len(message.Content) > 64<<10 {
		return "", usage, fmt.Errorf("audio text response is empty, invalid or oversized")
	}
	return message.Content, result.Usage, nil
}

func hasToolResult(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != "[]"
}

func (p *ManagedRuntime) textJSON(ctx context.Context, path string, data []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("audio text request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("owned audio text runtime returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (128<<10)+1))
	if err != nil {
		return fmt.Errorf("audio text response: %w", err)
	}
	if len(body) > 128<<10 {
		return fmt.Errorf("audio text response exceeds bound")
	}
	if !utf8.Valid(body) {
		return fmt.Errorf("audio text response is not UTF-8")
	}
	if err = json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("audio text response: %w", err)
	}
	return nil
}
