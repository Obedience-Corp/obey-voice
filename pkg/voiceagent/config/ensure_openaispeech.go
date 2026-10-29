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
	"path/filepath"
	"strings"
	"time"
)

// ollamaPullStallTimeout aborts a pull whose stream has gone quiet.
var ollamaPullStallTimeout = downloadStallTimeout

// ensureOpenAISpeechAssetsProgress pulls the configured model into Ollama when
// the target is Ollama and lacks it. Explicit voice setup also refreshes the
// official Phonon model when named voices are missing, then fetches metadata. A server that does not answer is an error naming it; a
// catalog that cannot be fetched is a warning, because speech does not need it.
func ensureOpenAISpeechAssetsProgress(ctx context.Context, cfg *Config, req AssetRequest, onProgress func(AssetProgress)) error {
	probeCtx, cancel := context.WithTimeout(ctx, OpenAISpeechProbeTimeout)
	target := ProbeOpenAISpeech(probeCtx, cfg)
	cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !target.Reachable {
		return fmt.Errorf("openai-speech: %s", target.Problem())
	}
	if target.Ollama {
		if err := ensureOllamaModel(ctx, target, openAISpeechAPIKey(cfg), req.NeedVoiceCatalog, onProgress); err != nil {
			return err
		}
	}
	if !req.NeedVoiceCatalog {
		return nil
	}
	return ensureOpenAISpeechCatalog(ctx, ModelsDirFrom(cfg), onProgress)
}

// ensureOpenAISpeechCatalog fetches voices.jsonl unless a valid one is in
// place. A failed fetch closes the asset as skipped with the reason, so a
// progress view does not wait on it forever.
func ensureOpenAISpeechCatalog(ctx context.Context, modelsDir string, onProgress func(AssetProgress)) error {
	name := OpenAISpeechVoicesFile
	emit := func(p AssetProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	path := filepath.Join(modelsDir, filepath.FromSlash(OpenAISpeechVoicesFile))
	if checkVoicesCatalog(path) == nil {
		emit(newAssetProgress(name, assetPhaseSkipped, 0, 0))
		return nil
	}
	emit(newAssetProgress(name, assetPhaseStart, 0, 0))
	url := openAISpeechVoicesURL
	check := func(p string) error {
		if err := checkVoicesCatalog(p); err != nil {
			return fmt.Errorf("%s did not return a voice catalog: %w", url, err)
		}
		return nil
	}
	var lastBytes, lastTotal int64
	err := downloadFileChecked(ctx, path, url, name, 0, "", check, func(bytes, total int64) {
		lastBytes, lastTotal = bytes, total
		emit(newAssetProgress(name, assetPhaseDownload, bytes, total))
	})
	if err == nil {
		emit(newAssetProgress(name, assetPhaseVerify, lastBytes, lastTotal))
		emit(newAssetProgress(name, assetPhaseDone, lastBytes, lastTotal))
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	fmt.Fprintf(os.Stderr, "warning: Phonon voice descriptions not installed: %v\n", err)
	closing := newAssetProgress(name, assetPhaseSkipped, lastBytes, lastTotal)
	closing.Detail = "not installed: " + err.Error()
	emit(closing)
	return nil
}

// checkVoicesCatalog accepts a file whose first non-empty line is a JSON
// voice entry with a name. An empty body or a captive portal's HTML is not a
// catalog, and must not count as installed.
func checkVoicesCatalog(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(line, &entry); err != nil || strings.TrimSpace(entry.Name) == "" {
			return errors.New("its first line is not a JSON voice entry with a name")
		}
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("it is empty")
}

func ensureOllamaModel(ctx context.Context, target OpenAISpeechTarget, apiKey string, requireVoices bool, onProgress func(AssetProgress)) error {
	requireVoices = requireVoices && target.usesPhononCatalog()
	name := OpenAISpeechModelAssetName(target.Model)
	var shown float64
	emit := func(phase string, bytes, total int64) {
		if onProgress == nil {
			return
		}
		p := newAssetProgress(name, phase, bytes, total)
		// A layer announced late grows the total; the bar must not run back.
		p.Pct = max(p.Pct, shown)
		shown = p.Pct
		onProgress(p)
	}
	if target.TooOld() {
		return fmt.Errorf("openai-speech: Ollama %s at %s cannot serve %s; Phonon needs Ollama %s or later", target.Version, target.Root, target.Model, MinPhononOllama)
	}
	// Only a listing that answered proves the model is absent; a failed one
	// must not turn into a pull.
	if !target.ModelsKnown {
		return fmt.Errorf("openai-speech: %s", target.ListingProblem())
	}
	if requireVoices {
		if target.VoicesErr != nil {
			return fmt.Errorf("openai-speech: could not check Phonon's voices: %w", target.VoicesErr)
		}
		if !HasNamedVoices(target.Voices) && !ollamaAtLeast(target.Version, MinPhononVoicesOllama) {
			return fmt.Errorf("openai-speech: Ollama %s at %s cannot list Phonon's named voices; %s", target.Version, target.Root, PhononVoicesRemedy(target.Version))
		}
	}
	if target.ModelListed && (!requireVoices || HasNamedVoices(target.Voices)) {
		emit(assetPhaseSkipped, 0, 0)
		return nil
	}
	emit(assetPhaseStart, 0, 0)
	// Do not announce completion until the refreshed model really lists voices.
	var doneBytes, doneTotal int64
	progress := func(phase string, bytes, total int64) {
		if phase == assetPhaseDone && requireVoices {
			doneBytes, doneTotal = bytes, total
			return
		}
		emit(phase, bytes, total)
	}
	if err := pullOllamaModel(ctx, target.Root, target.Model, apiKey, progress); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("openai-speech: pull %s into Ollama at %s: %w", target.Model, target.Root, err)
	}
	if requireVoices {
		probeCtx, cancel := context.WithTimeout(ctx, OpenAISpeechProbeTimeout)
		defer cancel()
		names, _, err := OllamaShowVoices(probeCtx, target.Root, target.Model, apiKey)
		if err != nil {
			return fmt.Errorf("openai-speech: verify Phonon's voices after download: %w", err)
		}
		if !HasNamedVoices(names) {
			return fmt.Errorf("openai-speech: refreshed %s at %s but Ollama %s still lists no named voices; check the server's model registry and retry setup", target.Model, target.Root, target.Version)
		}
		emit(assetPhaseDone, doneBytes, doneTotal)
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
	stall := &stallGuard{rc: resp.Body, timer: time.AfterFunc(ollamaPullStallTimeout, cancel), cancel: cancel, idle: ollamaPullStallTimeout}
	defer stall.Close()
	// The guard cancels only the request: the caller's ctx is still live.
	stalled := func() bool { return ctx.Err() == nil && reqCtx.Err() != nil }
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(stall, 64<<10))
		if stalled() {
			return errPullStalled()
		}
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
	if stalled() {
		return errPullStalled()
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the pull ended before Ollama reported success")
}

func errPullStalled() error {
	return fmt.Errorf("no progress from Ollama for %gs (is it online?)", ollamaPullStallTimeout.Seconds())
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
