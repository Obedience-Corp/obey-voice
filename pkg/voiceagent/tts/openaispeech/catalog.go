package openaispeech

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// Catalog sources, as CatalogSource reports them.
const (
	CatalogServer     = "server"
	CatalogOllama     = "ollama"
	CatalogConfigured = "configured"
)

// VoiceMetadataFile is Gradium's descriptive catalog (one JSON object per
// line: name, locale, gender, age, description), looked for under
// models_dir/phonon. Names and descriptions in it apply to every runtime that
// serves the same voice files, so it is merged into whatever catalog answered.
const VoiceMetadataFile = config.OpenAISpeechVoicesFile

// loadCatalog assembles p.voices in order of preference: the server's own
// catalog, Ollama's voice list, the configured voice alone. It never fails:
// a provider that can speak but cannot enumerate still speaks.
func (p *Provider) loadCatalog(ctx context.Context) {
	names, source, notice := p.catalogNames(ctx)
	p.catalogSource, p.notice = source, notice
	p.voices = buildCatalog(names, p.voice, loadVoiceMetadata(p.modelsDir))
}

func (p *Provider) catalogNames(ctx context.Context) (names []string, source, notice string) {
	if names, ok := p.fetchServerVoices(ctx); ok {
		return names, CatalogServer, ""
	}
	root, isOllama := ollamaRoot(p.base)
	if !isOllama {
		return nil, CatalogConfigured, ""
	}
	version, reachable := p.ollamaVersion(ctx, root)
	if !reachable {
		return nil, CatalogConfigured, ""
	}
	names, answered := p.ollamaVoices(ctx, root)
	switch {
	case len(names) > 0 && (!config.IsDefaultPhononModel(p.model) || config.HasNamedVoices(names)):
		return names, CatalogOllama, ""
	case !answered:
		// A failed or timed-out /api/show says nothing about the release;
		// blaming it would send a user on a current Ollama to upgrade.
		return nil, CatalogConfigured, ""
	}
	if !config.IsDefaultPhononModel(p.model) {
		return nil, CatalogConfigured, ""
	}
	return nil, CatalogConfigured, fmt.Sprintf(
		"Ollama %s lists no named voices for %s; %s",
		version, p.model, config.PhononVoicesRemedy(version))
}

// ollamaRoot maps an OpenAI-compatible base URL onto the Ollama API root it
// sits on, if the URL has that shape (…/v1).
func ollamaRoot(base string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	root, ok := strings.CutSuffix(trimmed, "/v1")
	return root, ok && root != ""
}

// ollamaVersion proves the root is Ollama (GET /api/version) and reports the
// version string, which the notice needs.
func (p *Provider) ollamaVersion(ctx context.Context, root string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/version", nil)
	if err != nil {
		return "", false
	}
	p.auth(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc); err != nil {
		return "", false
	}
	return doc.Version, true
}

// ollamaVoices reads the voices /api/show lists for the model. answered is
// false when the call failed or did not return 200; a 200 without the field
// is a release before the catalog change, reported as no names and answered.
func (p *Provider) ollamaVoices(ctx context.Context, root string) (names []string, answered bool) {
	body, _ := json.Marshal(map[string]string{"model": p.model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, root+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	p.auth(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var doc struct {
		Voices []string `json:"voices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&doc); err != nil {
		return nil, false
	}
	return doc.Voices, true
}

// voiceMeta is one line of voices.jsonl.
type voiceMeta struct {
	Name        string `json:"name"`
	Locale      string `json:"locale"`
	Gender      string `json:"gender"`
	Age         string `json:"age"`
	Description string `json:"description"`
}

// loadVoiceMetadata reads models_dir/phonon/voices.jsonl into a map keyed by
// folded name. A missing or unreadable file is an empty map: metadata is a
// nicety, never a gate.
func loadVoiceMetadata(modelsDir string) map[string]voiceMeta {
	out := map[string]voiceMeta{}
	if strings.TrimSpace(modelsDir) == "" {
		return out
	}
	f, err := os.Open(filepath.Join(modelsDir, filepath.FromSlash(VoiceMetadataFile)))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m voiceMeta
		if err := json.Unmarshal(line, &m); err != nil || strings.TrimSpace(m.Name) == "" {
			continue
		}
		out[foldName(m.Name)] = m
	}
	return out
}

// buildCatalog turns the names a runtime listed into voices, merging the
// descriptive metadata by folded name, keeping the configured voice listed
// even when the runtime did not name it, and ordering by locale then name so
// every surface shows the same grouping.
func buildCatalog(names []string, configured string, meta map[string]voiceMeta) []tts.Voice {
	out := make([]tts.Voice, 0, len(names)+1)
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		key := foldName(name)
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		v := tts.Voice{Name: name, FriendlyName: name}
		if m, ok := meta[key]; ok {
			v.Locale, v.Gender, v.Age, v.Description = m.Locale, m.Gender, m.Age, m.Description
		}
		out = append(out, v)
	}
	for _, n := range names {
		add(n)
	}
	add(configured)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Locale != out[j].Locale {
			// Voices without a locale (the runtime's own "default") sort first.
			if out[i].Locale == "" || out[j].Locale == "" {
				return out[i].Locale == ""
			}
			return out[i].Locale < out[j].Locale
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// foldName matches the way Ollama matches a voice: any case, with or without
// accents ("Lucía-Sol" and "lucia-sol" are the same voice).
func foldName(name string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, strings.TrimSpace(name))
	if err != nil {
		folded = name
	}
	return strings.ToLower(folded)
}
