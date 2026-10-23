package openaispeech

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func names(voices []tts.Voice) string {
	out := make([]string, 0, len(voices))
	for _, v := range voices {
		out = append(out, v.Name)
	}
	return strings.Join(out, ",")
}

// Ollama's /api/show lists the voices once its release carries them; the
// provider reads them when the server publishes no catalog of its own.
func TestCatalogFromOllamaShow(t *testing.T) {
	f, srv := newFake(t)
	f.ollamaVersion = "0.40.11"
	f.ollamaVoices = []string{"Zoey", "default", "Easton"}
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if p.CatalogSource() != CatalogOllama || p.CatalogNotice() != "" {
		t.Fatalf("source %q notice %q", p.CatalogSource(), p.CatalogNotice())
	}
	if got := names(p.ListVoices("", "")); got != "default,Easton,Zoey" {
		t.Fatalf("voices = %s", got)
	}
	if len(f.showRequests) != 1 || f.showRequests[0] != "phonon" {
		t.Fatalf("show requests = %v", f.showRequests)
	}
}

// A server catalog outranks Ollama's list: the server knows what it loaded.
func TestServerCatalogOutranksOllama(t *testing.T) {
	f, srv := newFake(t)
	f.voiceAPI = []string{"Harper"}
	f.ollamaVersion = "0.40.11"
	f.ollamaVoices = []string{"Zoey"}
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if p.CatalogSource() != CatalogServer || names(p.ListVoices("", "")) != "default,Harper" {
		t.Fatalf("source %q voices %s", p.CatalogSource(), names(p.ListVoices("", "")))
	}
}

// Ollama below the catalog release: the configured voice stands alone and the
// notice names the version and the upstream change, so the picker can say
// why it shows one voice.
func TestOllamaWithoutVoicesCarriesANotice(t *testing.T) {
	f, srv := newFake(t)
	f.ollamaVersion = "0.40.9"
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if p.CatalogSource() != CatalogConfigured || names(p.ListVoices("", "")) != "default" {
		t.Fatalf("source %q voices %s", p.CatalogSource(), names(p.ListVoices("", "")))
	}
	n := p.CatalogNotice()
	if !strings.Contains(n, "0.40.9") || !strings.Contains(n, ollamaVoicesIssue) || !strings.Contains(n, "ollama pull phonon") {
		t.Fatalf("notice = %q", n)
	}
}

// A non-Ollama server with no catalog is simply a one-voice catalog; there is
// nothing to tell the user to upgrade.
func TestPlainServerWithoutCatalogHasNoNotice(t *testing.T) {
	_, srv := newFake(t)
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if p.CatalogSource() != CatalogConfigured || p.CatalogNotice() != "" {
		t.Fatalf("source %q notice %q", p.CatalogSource(), p.CatalogNotice())
	}
}

// voices.jsonl under models_dir/phonon describes the voices; it is merged by
// folded name (any case, accents or not) and orders the catalog by locale.
func TestVoiceMetadataMergesByFoldedName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "phonon"), 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := strings.Join([]string{
		`{"name":"Zoey","locale":"en-us","gender":"female","age":"Young Adult","description":"Bright and warm."}`,
		`{"name":"Lucía-Sol","locale":"es-es","gender":"female","age":"Adult","description":"Cálida."}`,
		``,
		`not json`,
		`{"name":"Captain Silas Blackwater","locale":"characters","gender":null,"age":null,"description":"Gravelly pirate."}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "phonon", "voices.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	f, srv := newFake(t)
	f.ollamaVersion = "0.40.11"
	f.ollamaVoices = []string{"captain silas blackwater", "lucia-sol", "Zoey", "default"}
	cfg := cfgFor(srv.URL)
	cfg.ModelsDir = dir
	p, _, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := p.ListVoices("", "")
	if names(got) != "default,captain silas blackwater,Zoey,lucia-sol" {
		t.Fatalf("order = %s", names(got))
	}
	byName := map[string]tts.Voice{}
	for _, v := range got {
		byName[v.Name] = v
	}
	if z := byName["Zoey"]; z.Locale != "en-us" || z.Gender != "female" || z.Age != "Young Adult" || z.Description != "Bright and warm." {
		t.Fatalf("Zoey = %+v", z)
	}
	if l := byName["lucia-sol"]; l.Locale != "es-es" || l.Description != "Cálida." {
		t.Fatalf("accent-insensitive merge failed: %+v", l)
	}
	if c := byName["captain silas blackwater"]; c.Locale != "characters" || c.Gender != "" {
		t.Fatalf("character voice = %+v", c)
	}
	// Filters apply to the merged attributes; voices that do not state the
	// attribute pass, as in every other catalog here.
	if names(p.ListVoices("es-es", "")) != "default,lucia-sol" || names(p.ListVoices("", "female")) != "default,captain silas blackwater,Zoey,lucia-sol" {
		t.Fatalf("filters: %s | %s", names(p.ListVoices("es-es", "")), names(p.ListVoices("", "female")))
	}
}

func TestOllamaRootAndFold(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:11434/v1":  "http://localhost:11434",
		"http://localhost:11434/v1/": "http://localhost:11434",
		"http://host:8880/v1":        "http://host:8880",
	} {
		if root, ok := ollamaRoot(in); !ok || root != want {
			t.Fatalf("ollamaRoot(%q) = %q,%v", in, root, ok)
		}
	}
	if _, ok := ollamaRoot("http://host:8880"); ok {
		t.Fatal("a base without /v1 is not an Ollama root")
	}
	if foldName("Lucía-Sol") != "lucia-sol" || foldName("  ZOEY ") != "zoey" || foldName("Maëlys") != "maelys" {
		t.Fatalf("fold: %q %q %q", foldName("Lucía-Sol"), foldName("  ZOEY "), foldName("Maëlys"))
	}
}

// A failing /api/show says nothing about the release: no upgrade notice.
func TestOllamaShowFailureCarriesNoNotice(t *testing.T) {
	f, srv := newFake(t)
	f.ollamaVersion = "0.42.0"
	f.showStatus = 500
	p, _, err := New(cfgFor(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if p.CatalogSource() != CatalogConfigured || p.CatalogNotice() != "" {
		t.Fatalf("source %q notice %q", p.CatalogSource(), p.CatalogNotice())
	}
}

// An Ollama behind an authenticating proxy: the catalog calls must carry the
// same bearer token as synthesis, or the picker silently shows one voice.
func TestOllamaCatalogCallsCarryTheAPIKey(t *testing.T) {
	f, srv := newFake(t)
	f.requireAuth = "secret"
	f.ollamaVersion = "0.40.11"
	f.ollamaVoices = []string{"Zoey", "default"}
	cfg := cfgFor(srv.URL)
	cfg.OpenAISpeechAPIKey = "secret"
	p, _, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.CatalogSource() != CatalogOllama || names(p.ListVoices("", "")) != "default,Zoey" {
		t.Fatalf("source %q voices %s", p.CatalogSource(), names(p.ListVoices("", "")))
	}
}
