package nativeaudio

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/openaispeech"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

//go:embed assets/setup.wav
var setupAudio []byte

type Requirements struct {
	ASR   bool `json:"asr"`
	Tools bool `json:"tools"`
}
type SetupResult struct {
	Metadata         Metadata              `json:"metadata"`
	Voice            string                `json:"voice"`
	Requirements     Requirements          `json:"requirements"`
	ProtocolReady    bool                  `json:"protocol_ready"`
	QualityQualified bool                  `json:"quality_qualified"`
	KnownCombination bool                  `json:"known_combination"`
	CheckedAt        time.Time             `json:"checked_at"`
	ChatText         string                `json:"quiet_chat_text"`
	SpeechSamples    int                   `json:"quiet_speech_samples"`
	Transcript       *utterance.Transcript `json:"quiet_transcript,omitempty"`
}
type SupportCache struct {
	mu      sync.Mutex
	success map[string]SetupResult
}

func NewSupportCache() *SupportCache { return &SupportCache{success: make(map[string]SetupResult)} }
func setupKey(metadata Metadata, voice string, requirements Requirements) string {
	encoded, _ := json.Marshal(struct {
		Metadata     Metadata
		Voice        string
		Requirements Requirements
	}{metadata, voice, requirements})
	return string(encoded)
}

// Prepare records successful protocol checks for setup/status reuse. Activation
// still revalidates routes: a vanished ASR/speech route can otherwise remain
// invisible behind unchanged tags/version metadata. Repeated unchanged response
// boundaries reuse the already prepared binding instead of preparing again.
func (cache *SupportCache) Prepare(ctx context.Context, client *Client, cfg *config.Config, requirements Requirements) (*openaispeech.Provider, func(), SetupResult, error) {
	if cache == nil || client == nil || cfg == nil {
		return nil, nil, SetupResult{}, errors.New("native setup requires cache, client and isolated candidate config")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, SetupResult{}, err
	}
	if config.NativePhononSelected(cfg) || cfg.OpenAISpeechRuntime != "external" || cfg.OpenAISpeechAPIKey != "" || config.OpenAISpeechModel(cfg) != client.model || strings.TrimRight(config.OpenAISpeechURL(cfg), "/") != client.endpoint("/v1") {
		return nil, nil, SetupResult{}, errors.New("native speech must use the selected model/endpoint without another runtime or credentials")
	}
	metadata, err := client.Metadata(ctx, requirements.Tools)
	if err != nil {
		return nil, nil, SetupResult{}, err
	}
	voice := config.TTSVoiceFor(cfg, config.OpenAISpeechProviderName)
	key := setupKey(metadata, voice, requirements)
	cache.mu.Lock()
	_, known := cache.success[key]
	cache.mu.Unlock()
	result := SetupResult{Metadata: metadata, Voice: voice, Requirements: requirements, KnownCombination: known}
	fail := func(err error) (*openaispeech.Provider, func(), SetupResult, error) {
		cache.mu.Lock()
		delete(cache.success, key)
		cache.mu.Unlock()
		return nil, nil, result, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, SetupCheckTimeout)
	speech, closeSpeech, err := openaispeech.NewWithContext(probeCtx, cfg, client.HTTPClient())
	cancel()
	if err != nil {
		return fail(fmt.Errorf("native speech setup: %w", err))
	}
	success := false
	defer func() {
		if !success {
			closeSpeech()
		}
	}()
	media := chatmedia.NewAudioSession()
	defer media.Close()
	ref, err := media.Import(ctx, setupAudio)
	if err != nil {
		return fail(err)
	}
	if requirements.ASR {
		probeCtx, cancel := context.WithTimeout(ctx, SetupCheckTimeout)
		transcript, err := client.Transcribe(probeCtx, ref, media)
		cancel()
		if err != nil {
			return fail(fmt.Errorf("native ASR setup: %w", err))
		}
		result.Transcript = &transcript
	}
	var reply struct {
		Model   string `json:"model"`
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []any  `json:"tool_calls"`
		} `json:"message"`
		Done bool `json:"done"`
	}
	request := map[string]any{"model": client.model, "stream": false, "think": false, "truncate": false, "shift": false, "messages": []map[string]any{
		{"role": "system", "content": "Answer the spoken question. Return only the answer."},
		{"role": "user", "content": "", "images": [][]byte{setupAudio}},
	}, "options": map[string]any{"temperature": 0}}
	if err := client.metadataRequest(ctx, "POST", "/api/chat", request, &reply); err != nil {
		return fail(fmt.Errorf("native audio chat setup: %w", err))
	}
	if !reply.Done || reply.Message.Role != "assistant" || strings.TrimSpace(reply.Message.Content) == "" || len(reply.Message.Content) > MaxTranscriptBytes || len(reply.Message.ToolCalls) > 0 {
		return fail(errors.New("native audio chat setup returned no completed plain answer"))
	}
	if reply.Model != "" && reply.Model != client.model && strings.TrimSuffix(reply.Model, ":latest") != client.model {
		return fail(errors.New("native audio chat setup answered with another model"))
	}
	result.ChatText = reply.Message.Content
	probeCtx, cancel = context.WithTimeout(ctx, SetupCheckTimeout)
	stream, err := speech.Synthesize(probeCtx, "Ready.")
	if err != nil {
		cancel()
		return fail(fmt.Errorf("native quiet speech setup: %w", err))
	}
	const sampleLimit = 15 * openaispeech.SampleRate
	overLimit := false
	for samples := range stream.Frames() {
		if len(samples) > sampleLimit-result.SpeechSamples {
			overLimit = true
			cancel()
		} else if !overLimit {
			result.SpeechSamples += len(samples)
		}
	}
	if overLimit {
		cancel()
		return fail(errors.New("native quiet speech setup exceeds sample limit"))
	}
	err = stream.Err()
	cancel()
	if err != nil {
		return fail(fmt.Errorf("native quiet speech setup: %w", err))
	}
	if result.SpeechSamples == 0 {
		return fail(errors.New("native quiet speech setup returned no PCM"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	result.ProtocolReady = true
	result.CheckedAt = time.Now().UTC()
	// Protocol support is not listener/meaning/voice/task quality qualification.
	cache.mu.Lock()
	if cache.success == nil {
		cache.success = make(map[string]SetupResult)
	}
	stored := result
	stored.Metadata.Capabilities = append([]string(nil), result.Metadata.Capabilities...)
	if result.Transcript != nil {
		transcript := *result.Transcript
		stored.Transcript = &transcript
	}
	cache.success[key] = stored
	cache.mu.Unlock()
	success = true
	return speech, closeSpeech, result, nil
}

// Lookup returns detached historical support evidence for setup presentation.
// Prepare always refreshes the required routes before activating a binding.
func (cache *SupportCache) Lookup(metadata Metadata, voice string, requirements Requirements) (SetupResult, bool) {
	if cache == nil {
		return SetupResult{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	result, ok := cache.success[setupKey(metadata, voice, requirements)]
	result.Metadata.Capabilities = append([]string(nil), result.Metadata.Capabilities...)
	if result.Transcript != nil {
		copyTranscript := *result.Transcript
		result.Transcript = &copyTranscript
	}
	return result, ok
}
