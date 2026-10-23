package config

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenAISpeechVoicesAsset is the voice catalog file as a downloadable asset. It
// is not part of ManifestFor: a missing catalog must never block speech,
// listing or preview.
func OpenAISpeechVoicesAsset() Asset {
	return Asset{
		ID:       OpenAISpeechVoicesAssetID,
		Provider: OpenAISpeechProviderName,
		Kind:     AssetKindTTS,
		Name:     OpenAISpeechVoicesFile,
		Files:    []AssetFile{{Path: OpenAISpeechVoicesFile, URL: openAISpeechVoicesURL}},
	}
}

// ensureOpenAISpeechAssetsProgress pulls the configured model into Ollama when
// the target is Ollama and lacks it, then fetches the voice catalog file. A
// server that does not answer is an error naming it; a catalog that cannot be
// fetched is a warning, because speech does not need it.
func ensureOpenAISpeechAssetsProgress(ctx context.Context, cfg *Config, onProgress func(AssetProgress)) error {
	probeCtx, cancel := context.WithTimeout(ctx, OpenAISpeechProbeTimeout)
	target := ProbeOpenAISpeech(probeCtx, cfg)
	cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !target.Reachable {
		return fmt.Errorf("openai-speech: %s", target.Unreachable())
	}
	if target.Ollama {
		if err := ensureOllamaModel(ctx, target, openAISpeechAPIKey(cfg), onProgress); err != nil {
			return err
		}
	}
	manifest := AssetManifest{Schema: AssetSchema, Assets: []Asset{OpenAISpeechVoicesAsset()}}
	if err := ensureManifestProgress(ctx, manifest, ModelsDirFrom(cfg), onProgress); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		fmt.Fprintf(os.Stderr, "warning: Phonon voice descriptions not installed: %v\n", err)
	}
	return nil
}

func ensureOllamaModel(ctx context.Context, target OpenAISpeechTarget, apiKey string, onProgress func(AssetProgress)) error {
	name := OpenAISpeechModelAssetName(target.Model)
	emit := func(phase string, bytes, total int64) {
		if onProgress != nil {
			onProgress(newAssetProgress(name, phase, bytes, total))
		}
	}
	if target.TooOld() {
		return fmt.Errorf("openai-speech: Ollama %s at %s cannot serve %s; Phonon needs Ollama %s or later", target.Version, target.Root, target.Model, MinPhononOllama)
	}
	if target.ModelListed {
		emit(assetPhaseSkipped, 0, 0)
		return nil
	}
	emit(assetPhaseStart, 0, 0)
	if err := pullOllamaModel(ctx, target.Root, target.Model, apiKey, emit); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("openai-speech: pull %s into Ollama at %s: %w", target.Model, target.Root, err)
	}
	return nil
}

// ollamaPullLine is one NDJSON object of POST /api/pull's stream.
type ollamaPullLine struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// pullOllamaModel streams POST <root>/api/pull and maps its progress onto the
// asset phases: layer bytes summed into download, then verify, install and
// done. An error line mid-stream fails the pull.
func pullOllamaModel(ctx context.Context, root, model, apiKey string, emit func(phase string, bytes, total int64)) error {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := json.Marshal(map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, root+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setSpeechServerAuth(req, apiKey)
	resp, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	stall := &stallGuard{rc: resp.Body, timer: time.AfterFunc(downloadStallTimeout, cancel), cancel: cancel}
	defer stall.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(stall, 64<<10))
		return fmt.Errorf("HTTP %s: %s", resp.Status, ollamaErrorText(data))
	}

	layers := map[string][2]int64{}
	var done, total int64
	sc := bufio.NewScanner(stall)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var line ollamaPullLine
		if err := json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &line); err != nil {
			continue
		}
		switch {
		case line.Error != "":
			return errors.New(line.Error)
		case line.Digest != "" && line.Total > 0:
			layers[line.Digest] = [2]int64{line.Completed, line.Total}
			done, total = 0, 0
			for _, l := range layers {
				done, total = done+l[0], total+l[1]
			}
			emit(assetPhaseDownload, done, total)
		case strings.HasPrefix(line.Status, "verifying"):
			emit(assetPhaseVerify, done, total)
		case line.Status == "writing manifest":
			emit(assetPhaseInstall, done, total)
		case line.Status == "success":
			emit(assetPhaseDone, done, total)
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the pull ended before Ollama reported success")
}

func ollamaErrorText(body []byte) string {
	var doc struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &doc); err == nil && strings.TrimSpace(doc.Error) != "" {
		return strings.TrimSpace(doc.Error)
	}
	return strings.TrimSpace(string(body))
}
