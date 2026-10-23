package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// OpenAISpeechProbeTimeout bounds one read-only look at the speech server, so
// status, doctor and the voices gate stay quick when it is down.
const OpenAISpeechProbeTimeout = 3 * time.Second

// MinPhononOllama is the first Ollama release that serves Phonon.
const MinPhononOllama = "0.40.9"

// OllamaVoicesChange is the upstream change that makes Ollama list Phonon's
// voices in /api/show.
const OllamaVoicesChange = "ollama/ollama#19242"

// Status row ids for openai-speech. They live outside the manifest: the model
// belongs to the server and voices.jsonl is metadata that never gates speech.
const (
	OpenAISpeechModelAssetID  = "tts.openai-speech.model"
	OpenAISpeechServerAssetID = "tts.openai-speech.server"
	OpenAISpeechVoicesAssetID = "tts.openai-speech.voices"
)

var speechServerClient = &http.Client{}

// OpenAISpeechTarget is what the configured speech server answered.
type OpenAISpeechTarget struct {
	Base  string
	Model string
	// Root is the Ollama API root under Base when Base has Ollama's /v1 shape.
	Root string
	// Ollama is true when GET <Root>/api/version answered.
	Ollama  bool
	Version string
	// Reachable is true when Ollama or GET <Base>/models answered; Err says
	// why not.
	Reachable bool
	Err       error
	// Models is what the server serves (Ollama's /api/tags, else /models).
	// ModelsKnown is true only when a listing answered, so ModelListed is
	// evidence either way; ListErr says why no listing did.
	Models      []string
	ModelsKnown bool
	ListErr     error
	ModelListed bool
}

// ProbeOpenAISpeech asks the configured server what it is and what it serves.
// It never fails: an unreachable server is a result, not an error.
func ProbeOpenAISpeech(ctx context.Context, cfg *Config) OpenAISpeechTarget {
	t := OpenAISpeechTarget{Base: OpenAISpeechURL(cfg), Model: OpenAISpeechModel(cfg)}
	key := openAISpeechAPIKey(cfg)
	if root, ok := OllamaRootOf(t.Base); ok {
		t.Root = root
		if version, err := OllamaVersion(ctx, root, key); err == nil {
			t.Ollama, t.Version, t.Reachable = true, version, true
			if names, err := ollamaTags(ctx, root, key); err == nil {
				t.Models, t.ModelsKnown = names, true
			} else {
				t.ListErr = err
			}
		}
	}
	if !t.ModelsKnown {
		ids, listed, err := speechServerModels(ctx, t.Base, key)
		switch {
		case err == nil && listed:
			t.Reachable, t.Models, t.ModelsKnown, t.ListErr = true, ids, true, nil
		case err == nil:
			t.Reachable = true
			if t.ListErr == nil {
				t.ListErr = fmt.Errorf("GET %s/models did not return a model list", t.Base)
			}
		case !t.Reachable:
			t.Err = err
		case t.ListErr == nil:
			t.ListErr = err
		default:
			t.ListErr = fmt.Errorf("%w; %w", t.ListErr, err)
		}
	}
	t.ModelListed = t.ModelsKnown && servesModel(t.Models, t.Model)
	return t
}

// ListingProblem says why the model could not be checked.
func (t OpenAISpeechTarget) ListingProblem() string {
	where := t.Base
	if t.Ollama {
		where = "Ollama " + t.Version + " at " + t.Root
	}
	return fmt.Sprintf("could not check whether %s is installed in %s: %v", t.Model, where, t.ListErr)
}

// Problem says why an unreachable target cannot be used, with the remedy.
func (t OpenAISpeechTarget) Problem() string {
	if code := t.RejectedCode(); code != 0 {
		return fmt.Sprintf("%s rejected the request (%d): check openai_speech_api_key", t.Base, code)
	}
	return fmt.Sprintf("%s is not answering (%v); start Ollama with `ollama serve`, or point openai_speech_url at a running speech server", t.Base, t.Err)
}

// RejectedCode is 401 or 403 when the server answered but refused the
// credentials, else 0.
func (t OpenAISpeechTarget) RejectedCode() int {
	var status *speechServerStatusError
	if errors.As(t.Err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) {
		return status.Code
	}
	return 0
}

// speechServerStatusError is a server that answered with something other
// than 200.
type speechServerStatusError struct {
	Method, URL, Status string
	Code                int
}

func (e *speechServerStatusError) Error() string {
	return fmt.Sprintf("%s %s returned %s", e.Method, e.URL, e.Status)
}

// TooOld reports an Ollama release that cannot serve Phonon.
func (t OpenAISpeechTarget) TooOld() bool {
	return t.Ollama && !ollamaAtLeast(t.Version, MinPhononOllama)
}

// OpenAISpeechModelAssetName labels the model in status rows and progress
// ticks, so a client can match the two.
func OpenAISpeechModelAssetName(model string) string {
	return model + " (Ollama)"
}

// OpenAISpeechAssetStatus turns a probe into status rows: the model when the
// target is Ollama, else the server's reachability, and always the voice
// catalog file. A server that is down is reported in a row, never as an error.
func OpenAISpeechAssetStatus(t OpenAISpeechTarget, modelsDir string) []AssetStatus {
	var rows []AssetStatus
	switch {
	case t.Ollama:
		row := AssetStatus{
			ID: OpenAISpeechModelAssetID, Name: OpenAISpeechModelAssetName(t.Model),
			Provider: OpenAISpeechProviderName, Mode: "ollama", Kind: AssetKindTTS,
			Installed: t.ModelListed,
		}
		if ollamaTagged(t.Model) == ollamaTagged(DefaultOpenAISpeechModel) {
			row.License, row.Source = "Apache-2.0", "https://ollama.com/library/phonon"
		}
		switch {
		case t.TooOld():
			row.Installed = false
			row.Detail = fmt.Sprintf("Ollama %s at %s is older than %s, the first release that serves Phonon; update Ollama", t.Version, t.Root, MinPhononOllama)
		case !t.ModelsKnown:
			row.Detail = "unknown: " + t.ListingProblem()
		case !t.ModelListed:
			row.Detail = fmt.Sprintf("not pulled into Ollama %s at %s; run `obey-voice models ensure --tts`", t.Version, t.Root)
		}
		rows = append(rows, row)
	case t.Root != "" && !t.Reachable:
		rows = append(rows, AssetStatus{
			ID: OpenAISpeechModelAssetID, Name: OpenAISpeechModelAssetName(t.Model),
			Provider: OpenAISpeechProviderName, Mode: "ollama", Kind: AssetKindTTS,
			Detail: t.Problem(),
		})
	default:
		row := AssetStatus{
			ID: OpenAISpeechServerAssetID, Name: "speech server " + t.Base,
			Provider: OpenAISpeechProviderName, Kind: AssetKindTTS, Installed: t.Reachable,
		}
		if !t.Reachable {
			row.Detail = t.Problem()
		}
		rows = append(rows, row)
	}
	return append(rows, openAISpeechVoicesStatus(modelsDir))
}

func openAISpeechVoicesStatus(modelsDir string) AssetStatus {
	path := filepath.Join(modelsDir, filepath.FromSlash(OpenAISpeechVoicesFile))
	row := AssetStatus{
		ID: OpenAISpeechVoicesAssetID, Name: OpenAISpeechVoicesFile,
		Provider: OpenAISpeechProviderName, Kind: AssetKindTTS,
		License: "Apache-2.0", Source: "https://huggingface.co/gradium/phonon-voices",
	}
	err := checkVoicesCatalog(path)
	row.Installed = err == nil
	switch {
	case row.Installed:
	case pathExists(path):
		row.Missing = []string{path}
		row.Detail = fmt.Sprintf("not a voice catalog (%v); run `obey-voice models ensure --tts`", err)
	default:
		row.Missing = []string{path}
		row.Detail = "voice descriptions only; speech works without it"
	}
	return row
}

// OllamaRootOf maps an OpenAI-compatible base URL onto the Ollama API root it
// sits on, when the URL has that shape (…/v1).
func OllamaRootOf(base string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	root, ok := strings.CutSuffix(trimmed, "/v1")
	return root, ok && root != ""
}

// OllamaVersion reads GET <root>/api/version, which also proves root is Ollama.
func OllamaVersion(ctx context.Context, root, apiKey string) (string, error) {
	var doc struct {
		Version string `json:"version"`
	}
	if err := speechServerJSON(ctx, http.MethodGet, root+"/api/version", apiKey, nil, 64<<10, &doc); err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.Version) == "" {
		return "", errors.New("/api/version carried no version")
	}
	return doc.Version, nil
}

// OllamaShowVoices reads the voices /api/show lists for model. answered is
// false when Ollama did not answer 200; true with no names is a release that
// predates the voice catalog.
func OllamaShowVoices(ctx context.Context, root, model, apiKey string) (names []string, answered bool, err error) {
	var doc struct {
		Voices []string `json:"voices"`
	}
	if err := speechServerJSON(ctx, http.MethodPost, root+"/api/show", apiKey, map[string]string{"model": model}, 4<<20, &doc); err != nil {
		return nil, false, err
	}
	return doc.Voices, true, nil
}

func ollamaTags(ctx context.Context, root, apiKey string) ([]string, error) {
	var doc struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := speechServerJSON(ctx, http.MethodGet, root+"/api/tags", apiKey, nil, 4<<20, &doc); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(doc.Models))
	for _, m := range doc.Models {
		if n := strings.TrimSpace(m.Name); n != "" {
			names = append(names, n)
		} else if n := strings.TrimSpace(m.Model); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// speechServerModels reads the ids GET <base>/models lists. listed is false
// when the server answered with something that is not a model list.
func speechServerModels(ctx context.Context, base, apiKey string) (ids []string, listed bool, err error) {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := speechServerJSON(ctx, http.MethodGet, base+"/models", apiKey, nil, 4<<20, &doc); err != nil {
		var syntax *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &typeErr) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if doc.Data == nil {
		return nil, false, nil
	}
	ids = make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, true, nil
}

func speechServerJSON(ctx context.Context, method, url, apiKey string, body any, limit int64, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setSpeechServerAuth(req, apiKey)
	resp, err := speechServerClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &speechServerStatusError{Method: method, URL: url, Status: resp.Status, Code: resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(out)
}

func setSpeechServerAuth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func openAISpeechAPIKey(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.OpenAISpeechAPIKey)
}

// servesModel matches the way Ollama resolves a name: "phonon" is
// "phonon:latest", any case.
func servesModel(names []string, model string) bool {
	want := ollamaTagged(model)
	if want == "" {
		return false
	}
	for _, n := range names {
		if ollamaTagged(n) == want {
			return true
		}
	}
	return false
}

func ollamaTagged(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	if i := strings.LastIndex(name, "/"); !strings.Contains(name[i+1:], ":") {
		name += ":latest"
	}
	return name
}

// ollamaAtLeast compares dotted release numbers. A version it cannot read (a
// source build reports 0.0.0) is given the benefit of the doubt.
func ollamaAtLeast(version, min string) bool {
	have, ok := releaseNumbers(version)
	if !ok || (have[0] == 0 && have[1] == 0 && have[2] == 0) {
		return true
	}
	want, _ := releaseNumbers(min)
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

func releaseNumbers(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
