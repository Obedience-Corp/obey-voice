// Package nativeaudio owns the selected same-model native audio HTTP binding.
// It does not construct capture, conversation loops or playback devices.
package nativeaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/ollama/ollama/envconfig"
)

const MaxTranscriptBytes = 256 << 10

type Client struct {
	base   url.URL
	model  string
	http   *http.Client
	cancel context.CancelFunc
}

func NewClient(endpoint, model string) (*Client, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base == nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(endpoint, "#") || strings.TrimSpace(model) == "" {
		return nil, errors.New("native audio needs an explicit HTTP(S) endpoint and model without embedded credentials or query")
	}
	if envconfig.UseAuth() || strings.EqualFold(base.Hostname(), "ollama.com") {
		return nil, errors.New("native audio cannot resolve consistent authentication across chat, transcription and speech")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	scope, cancel := context.WithCancel(context.Background())
	selected := &selectedTransport{
		scope:  scope,
		base:   transport,
		scheme: base.Scheme,
		host:   base.Host,
		prefix: strings.TrimRight(base.Path, "/"),
	}
	return &Client{
		base:   *base,
		model:  model,
		cancel: cancel,
		http: &http.Client{
			Transport: selected,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("native audio endpoint redirects are not allowed")
			},
		},
	}, nil
}
func (c *Client) endpoint(route string) string {
	target := c.base
	target.Path = strings.TrimRight(target.Path, "/") + route
	target.RawPath = ""
	return target.String()
}
func (c *Client) Model() string            { return c.model }
func (c *Client) HTTPClient() *http.Client { return c.http }
func (c *Client) Close()                   { c.cancel(); c.http.CloseIdleConnections() }

// Transcribe uses only the selected model's transcription route. A missing,
// empty or failed response never becomes an invented transcript or fallback.
func (c *Client) Transcribe(ctx context.Context, ref utterance.AudioRef, resolver utterance.Resolver) (utterance.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return utterance.Transcript{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if resolver == nil {
		return utterance.Transcript{}, utterance.ErrAudioUnavailable
	}
	data, err := resolver.ResolveAudio(ctx, ref)
	if err != nil {
		return utterance.Transcript{}, err
	}
	inspected, canonical, err := chatmedia.CanonicalAudio(data)
	if err != nil {
		return utterance.Transcript{}, err
	}
	if inspected != ref {
		return utterance.Transcript{}, utterance.ErrInvalidAudio
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", c.model); err != nil {
		return utterance.Transcript{}, err
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="utterance.wav"`)
	header.Set("Content-Type", "audio/wav")
	file, err := writer.CreatePart(header)
	if err != nil {
		return utterance.Transcript{}, err
	}
	if _, err := file.Write(canonical); err != nil {
		return utterance.Transcript{}, err
	}
	if err := writer.Close(); err != nil {
		return utterance.Transcript{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/audio/transcriptions"), &body)
	if err != nil {
		return utterance.Transcript{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return utterance.Transcript{}, fmt.Errorf("native transcription request: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, MaxTranscriptBytes+1))
	if err != nil {
		return utterance.Transcript{}, fmt.Errorf("read native transcription: %w", err)
	}
	if len(payload) > MaxTranscriptBytes {
		return utterance.Transcript{}, errors.New("native transcription exceeds response limit")
	}
	if response.StatusCode != http.StatusOK {
		return utterance.Transcript{}, fmt.Errorf("native transcription HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(payload)))
	}
	if !utf8.Valid(payload) {
		return utterance.Transcript{}, errors.New("native transcription is not valid UTF-8")
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return utterance.Transcript{}, fmt.Errorf("decode native transcription: %w", err)
	}
	transcript := utterance.Transcript{State: utterance.Ready, Text: strings.TrimSpace(result.Text), Provider: "ollama", Model: c.model}
	if err := transcript.Validate(); err != nil {
		return utterance.Transcript{}, fmt.Errorf("native transcription unavailable: %w", err)
	}
	return transcript, nil
}
