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
	// Models is what the server serves (Ollama's /api/tags, else /models);
	// nil when it lists nothing.
	Models      []string
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
				t.Models = names
			}
		}
	}
	if !t.Ollama || t.Models == nil {
		ids, err := speechServerModels(ctx, t.Base, key)
		switch {
		case err == nil:
			t.Reachable = true
			if t.Models == nil {
				t.Models = ids
			}
		case !t.Reachable:
			t.Err = err
		}
	}
	t.ModelListed = servesModel(t.Models, t.Model)
	return t
}

// Unreachable describes a server that did not answer, with the remedy.
func (t OpenAISpeechTarget) Unreachable() string {
	return fmt.Sprintf("%s is not answering (%v); start Ollama with `ollama serve`, or point openai_speech_url at a running speech server", t.Base, t.Err)
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
		switch {
		case t.TooOld():
			row.Installed = false
			row.Detail = fmt.Sprintf("Ollama %s at %s is older than %s, the first release that serves Phonon; update Ollama", t.Version, t.Root, MinPhononOllama)
		case !t.ModelListed:
			row.Detail = fmt.Sprintf("not pulled into Ollama %s at %s; run `obey-voice models ensure --tts`", t.Version, t.Root)
		}
		rows = append(rows, row)
	case t.Root != "" && !t.Reachable:
		rows = append(rows, AssetStatus{
			ID: OpenAISpeechModelAssetID, Name: OpenAISpeechModelAssetName(t.Model),
			Provider: OpenAISpeechProviderName, Mode: "ollama", Kind: AssetKindTTS,
			Detail: t.Unreachable(),
		})
	default:
		row := AssetStatus{
			ID: OpenAISpeechServerAssetID, Name: "speech server " + t.Base,
			Provider: OpenAISpeechProviderName, Kind: AssetKindTTS, Installed: t.Reachable,
		}
		if !t.Reachable {
			row.Detail = t.Unreachable()
		}
		rows = append(rows, row)
	}
	return append(rows, openAISpeechVoicesStatus(modelsDir))
}

func openAISpeechVoicesStatus(modelsDir string) AssetStatus {
	path := filepath.Join(modelsDir, filepath.FromSlash(OpenAISpeechVoicesFile))
	row := AssetStatus{
		ID: OpenAISpeechVoicesAssetID, Name: OpenAISpeechVoicesFile,
		Provider: OpenAISpeechProviderName, Kind: AssetKindTTS, Installed: pathExists(path),
	}
	if !row.Installed {
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

// speechServerModels reads the ids GET <base>/models lists; nil when the body
// is not a list.
func speechServerModels(ctx context.Context, base, apiKey string) ([]string, error) {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := speechServerJSON(ctx, http.MethodGet, base+"/models", apiKey, nil, 4<<20, &doc); err != nil {
		var syntax *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &typeErr) {
			return nil, nil
		}
		return nil, err
	}
	if len(doc.Data) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
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
		return fmt.Errorf("%s %s returned %s", method, url, resp.Status)
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
