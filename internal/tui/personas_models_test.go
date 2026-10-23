package tui

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/Obedience-Corp/obey-voice/internal/discovery"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/internal/persona/campdirs"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// newPickerPersonas builds a Personas screen whose persona store and provider
// discovery are both fakes: these tests must never read the user's personas or
// reach an ollama socket.
func newPickerPersonas(t *testing.T, cfg *config.Config, p *persona.Profile) personasModel {
	t.Helper()
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		if p == nil {
			return nil, nil
		}
		return []*persona.Profile{p}, nil
	}
	m.loadPromptForProfile = func(*persona.Profile) (string, error) { return "You are a test.", nil }
	m.starterPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.brainModels.discover = func(context.Context, *config.Config) []discovery.ProviderInfo {
		t.Error("discovery ran without the test asking for it")
		return nil
	}
	m.directory.discover = func(context.Context) campdirs.Choices {
		t.Error("camp discovery ran without the test asking for it")
		return campdirs.Choices{}
	}
	m.reload()
	m.width, m.height = 100, 30
	return m
}

// personaScanCmd digs the model-scan command out of the batch beginEditAt /
// beginCreate return (cursor blink first, camp scan, model scan last).
func personaScanCmd(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("opening the form returned no command; the scan must run async")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("opening the form did not batch a scan command (%T)", cmd())
	}
	return batch[len(batch)-1]
}

// fakeScan is the discovery result a test wants the form to see.
func fakeScan(models map[string][]string) []discovery.ProviderInfo {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]discovery.ProviderInfo, 0, len(names))
	for _, name := range names {
		list := models[name]
		out = append(out, discovery.ProviderInfo{Name: name, Available: len(list) > 0, Models: list})
	}
	return out
}

func TestPersonaBrainModelList(t *testing.T) {
	// The Model row is a picker over discovered models. Degraded scans come
	// first: they are the states that must never lose a value or lie about
	// what a provider offers.
	tests := []struct {
		name      string
		cfg       *config.Config
		provider  string // brain provider row; "" = "(default)"
		saved     string // model already on the persona
		scan      map[string][]string
		pending   bool  // scan still in flight
		scanErr   error // scan cancelled/timed out
		wantList  []string
		wantNote  string
		wantLabel string
	}{
		{
			name:      "scan still running keeps the row usable",
			cfg:       &config.Config{BrainProvider: "ollama"},
			pending:   true,
			wantList:  []string{stackDefaultLabel, stackCustomLabel},
			wantNote:  "scanning models…",
			wantLabel: stackDefaultLabel,
		},
		{
			name:      "scan error never claims an empty catalog",
			cfg:       &config.Config{BrainProvider: "ollama"},
			scanErr:   context.DeadlineExceeded,
			wantList:  []string{stackDefaultLabel, stackCustomLabel},
			wantNote:  "model scan failed",
			wantLabel: stackDefaultLabel,
		},
		{
			name:      "ollama down leaves default and custom",
			cfg:       &config.Config{BrainProvider: "ollama"},
			scan:      map[string][]string{"ollama": nil, "claude": {"default"}},
			wantList:  []string{stackDefaultLabel, stackCustomLabel},
			wantNote:  "no ollama models discovered",
			wantLabel: stackDefaultLabel,
		},
		{
			name:      "claude offers default and custom only",
			cfg:       &config.Config{BrainProvider: "ollama"},
			provider:  "claude",
			scan:      map[string][]string{"claude": {"default"}, "ollama": {"llama3"}},
			wantList:  []string{stackDefaultLabel, stackCustomLabel},
			wantNote:  "claude reports no model list",
			wantLabel: stackDefaultLabel,
		},
		{
			name:      "discovered ollama models are selectable",
			cfg:       &config.Config{BrainProvider: "ollama"},
			provider:  "ollama",
			scan:      map[string][]string{"ollama": {"llama3", "qwen3:8b"}},
			wantList:  []string{stackDefaultLabel, "llama3", "qwen3:8b", stackCustomLabel},
			wantNote:  "",
			wantLabel: stackDefaultLabel,
		},
		{
			name:      "(default) provider row scopes to the app provider",
			cfg:       &config.Config{BrainProvider: "grok"},
			scan:      map[string][]string{"grok": {"grok-4"}, "ollama": {"llama3"}},
			wantList:  []string{stackDefaultLabel, "grok-4", stackCustomLabel},
			wantNote:  "",
			wantLabel: stackDefaultLabel,
		},
		{
			name:      "a saved model the scan did not list survives",
			cfg:       &config.Config{BrainProvider: "ollama"},
			provider:  "ollama",
			saved:     "gpt-oss:120b",
			scan:      map[string][]string{"ollama": {"llama3"}},
			wantList:  []string{stackDefaultLabel, "llama3", "gpt-oss:120b", stackCustomLabel},
			wantNote:  "",
			wantLabel: "gpt-oss:120b",
		},
		{
			name:      "a saved model is preselected in catalog spelling",
			cfg:       &config.Config{BrainProvider: "ollama"},
			provider:  "ollama",
			saved:     "LLAMA3",
			scan:      map[string][]string{"ollama": {"llama3", "qwen3:8b"}},
			wantList:  []string{stackDefaultLabel, "llama3", "qwen3:8b", stackCustomLabel},
			wantNote:  "",
			wantLabel: "llama3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newPickerPersonas(t, tc.cfg, &persona.Profile{
				ID: "test", DisplayName: "Test",
				Brain: persona.Brain{Provider: tc.provider, Model: tc.saved},
			})
			m.brainModels.discover = func(context.Context, *config.Config) []discovery.ProviderInfo {
				return fakeScan(tc.scan)
			}
			m.cursor = 0
			cmd := m.beginEditAt(formRowName)
			if cmd == nil {
				t.Fatal("beginEditAt returned no command; the model scan must run async")
			}
			if !tc.pending {
				m.applyBrainModels(personaModelsMsg{
					seq:       m.brainModels.seq,
					providers: fakeScan(tc.scan),
					err:       tc.scanErr,
				})
			}

			if got := m.stackModelList(); !reflect.DeepEqual(got, tc.wantList) {
				t.Errorf("stackModelList() = %v, want %v", got, tc.wantList)
			}
			if got := m.stackModelLabel(); got != tc.wantLabel {
				t.Errorf("stackModelLabel() = %q, want %q", got, tc.wantLabel)
			}
			if got := m.stackModelNote(); !strings.Contains(got, tc.wantNote) {
				t.Errorf("stackModelNote() = %q, want it to contain %q", got, tc.wantNote)
			}
		})
	}
}

func TestPersonaBrainModelRowIsAPicker(t *testing.T) {
	// Lance's complaint: the Model row was a bare text box. It must cycle like
	// the neighbouring rows, and only "(custom)" may take typing.
	cfg := &config.Config{BrainProvider: "ollama"}
	m := newPickerPersonas(t, cfg, &persona.Profile{ID: "test", DisplayName: "Test"})
	m.brainModels.discover = func(context.Context, *config.Config) []discovery.ProviderInfo {
		return fakeScan(map[string][]string{"ollama": {"llama3", "qwen3:8b"}})
	}
	m.cursor = 0
	m.beginEditAt(formRowName)
	m.applyBrainModels(personaModelsMsg{
		seq:       m.brainModels.seq,
		providers: fakeScan(map[string][]string{"ollama": {"llama3", "qwen3:8b"}}),
	})
	m.setFormRow(formRowBrainProvider)
	m.setFormRow(formRowBrainModel)

	view := stripANSI(m.View())
	if !strings.Contains(view, "‹ "+stackDefaultLabel+" ›") {
		t.Fatalf("model row should render as a picker, got:\n%s", view)
	}

	// Typing on a listed entry must not edit anything (select-only row).
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if m.brainModelInput.Value() != "" {
		t.Fatalf("typing on a listed entry changed the model to %q", m.brainModelInput.Value())
	}

	// →  walks the discovered models.
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if got := m.formBrain().Model; got != "llama3" {
		t.Fatalf("after → model = %q, want llama3", got)
	}
	if !strings.Contains(stripANSI(m.View()), "‹ llama3 ›") {
		t.Fatalf("model row did not render the selection:\n%s", stripANSI(m.View()))
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if got := m.formBrain().Model; got != "qwen3:8b" {
		t.Fatalf("after →→ model = %q, want qwen3:8b", got)
	}

	// One more → lands on (custom): the free text field appears, focused, and
	// unlisted names stay reachable.
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if !m.brainModels.custom {
		t.Fatal("→ past the last model did not reach (custom)")
	}
	if !m.brainModelInput.Focused() {
		t.Fatal("(custom) must focus the model text field")
	}
	if !strings.Contains(stripANSI(m.View()), "‹ "+stackCustomLabel+" ›") {
		t.Fatalf("(custom) not rendered:\n%s", stripANSI(m.View()))
	}
	for _, r := range "-remote" {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.formBrain().Model; got != "qwen3:8b-remote" {
		t.Fatalf("custom text = %q, want qwen3:8b-remote", got)
	}

	// → wraps back to (default) = inherit the app config.
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if m.brainModels.custom || m.formBrain().Model != "" {
		t.Fatalf("wrap to (default) left custom=%v model=%q", m.brainModels.custom, m.formBrain().Model)
	}
	if m.brainModelInput.Focused() {
		t.Fatal("(default) must blur the model text field")
	}
}

func TestPersonaBrainModelClampsOnProviderChange(t *testing.T) {
	scan := map[string][]string{"ollama": {"llama3"}, "grok": {"grok-4"}, "claude": {"default"}}
	tests := []struct {
		name      string
		saved     string
		custom    bool
		applyScan bool
		want      string
	}{
		{
			name:      "unknown catalog keeps the model",
			saved:     "llama3",
			applyScan: false, // scan never landed: unknown is not invalid
			want:      "llama3",
		},
		{
			name:      "custom text survives a provider change",
			saved:     "some-remote-model",
			custom:    true,
			applyScan: true,
			want:      "some-remote-model",
		},
		{
			name:      "a model the new provider lacks is dropped",
			saved:     "llama3",
			applyScan: true,
			want:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{BrainProvider: "ollama"}
			m := newPickerPersonas(t, cfg, &persona.Profile{
				ID: "test", DisplayName: "Test",
				Brain: persona.Brain{Provider: "ollama", Model: tc.saved},
			})
			m.brainModels.discover = func(context.Context, *config.Config) []discovery.ProviderInfo {
				return fakeScan(scan)
			}
			m.cursor = 0
			m.beginEditAt(formRowName)
			if tc.applyScan {
				m.applyBrainModels(personaModelsMsg{seq: m.brainModels.seq, providers: fakeScan(scan)})
			}
			m.brainModels.custom = tc.custom
			m.setFormRow(formRowBrainProvider)

			// Move the Brain row from ollama to grok.
			m.setFormRow(formRowBrainProvider)
			for i := 0; i < len(stackBrainProviders()); i++ {
				if providerAt(stackBrainProviders(), m.brainProviderIdx) == "grok" {
					break
				}
				m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
			}
			if providerAt(stackBrainProviders(), m.brainProviderIdx) != "grok" {
				t.Fatal("failed to select the grok provider")
			}
			if got := m.formBrain().Model; got != tc.want {
				t.Fatalf("model after provider change = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPersonaBrainModelScanIsAsyncAndOrdered(t *testing.T) {
	// Opening the form must not wait on discovery, and a result from a
	// superseded scan must not overwrite the current one.
	cfg := &config.Config{BrainProvider: "ollama"}
	m := newPickerPersonas(t, cfg, &persona.Profile{ID: "test", DisplayName: "Test"})

	release := make(chan struct{})
	m.brainModels.discover = func(ctx context.Context, _ *config.Config) []discovery.ProviderInfo {
		<-release
		return fakeScan(map[string][]string{"ollama": {"llama3"}})
	}
	m.cursor = 0
	cmd := personaScanCmd(t, m.beginEditAt(formRowName))
	m.setFormRow(formRowBrainProvider)
	m.setFormRow(formRowBrainModel)
	if !strings.Contains(stripANSI(m.View()), "scanning models…") {
		t.Fatalf("pending scan is not visible:\n%s", stripANSI(m.View()))
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	close(release)
	msg, ok := (<-done).(personaModelsMsg)
	if !ok {
		t.Fatal("scan command did not return a personaModelsMsg")
	}

	// A stale result (previous form open) is ignored.
	stale := msg
	stale.seq--
	stale.providers = fakeScan(map[string][]string{"ollama": {"stale-model"}})
	m, _ = m.Update(stale)
	if m.brainModels.loaded {
		t.Fatal("stale scan result was applied")
	}

	m, _ = m.Update(msg)
	if !m.brainModels.loaded || m.brainModels.loading {
		t.Fatalf("scan not folded in: loaded=%v loading=%v", m.brainModels.loaded, m.brainModels.loading)
	}
	view := stripANSI(m.View())
	if strings.Contains(view, "scanning models…") {
		t.Fatalf("scanning note outlived the scan:\n%s", view)
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if got := m.formBrain().Model; got != "llama3" {
		t.Fatalf("discovered model not selectable after the scan: %q", got)
	}
}

func TestPersonaBrainModelScanIsCancelledWithTheForm(t *testing.T) {
	cfg := &config.Config{BrainProvider: "ollama"}
	m := newPickerPersonas(t, cfg, &persona.Profile{ID: "test", DisplayName: "Test"})

	started := make(chan struct{})
	var mu sync.Mutex
	var seen error
	m.brainModels.discover = func(ctx context.Context, _ *config.Config) []discovery.ProviderInfo {
		close(started)
		<-ctx.Done()
		mu.Lock()
		seen = ctx.Err()
		mu.Unlock()
		return nil
	}
	m.cursor = 0
	cmd := personaScanCmd(t, m.beginEditAt(formRowName))
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	// Guarded: if the scan command ever stops reaching discovery, fail here
	// instead of blocking the whole package until the test binary times out.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the scan command never called discovery")
	}

	// Closing the form abandons the scan instead of leaking it.
	m.cancelForm()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not observe cancellation")
	}
	mu.Lock()
	defer mu.Unlock()
	if seen == nil {
		t.Fatal("discovery context was never cancelled")
	}
	if m.brainModels.loading {
		t.Fatal("cancelled scan left the row loading")
	}
}

func TestPersonaBrainModelScanRace(t *testing.T) {
	// The scan runs on a bubbletea command goroutine while the user keeps
	// typing: the command must not touch model state. Run with -race.
	cfg := &config.Config{BrainProvider: "ollama"}
	m := newPickerPersonas(t, cfg, &persona.Profile{ID: "test", DisplayName: "Test"})
	m.brainModels.discover = func(context.Context, *config.Config) []discovery.ProviderInfo {
		return fakeScan(map[string][]string{"ollama": {"llama3", "qwen3:8b"}})
	}
	m.cursor = 0
	cmd := personaScanCmd(t, m.beginEditAt(formRowName))

	msgs := make(chan tea.Msg, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs <- cmd()
		}()
	}

	m.setFormRow(formRowBrainProvider)
	for i := 0; i < 20; i++ {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyDown})
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
		_ = m.View()
	}
	wg.Wait()
	close(msgs)

	for msg := range msgs {
		m, _ = m.Update(msg)
	}
	if !m.brainModels.loaded {
		t.Fatal("scan results never landed")
	}
}

// modelRowLine returns the rendered Model row as the user sees it (box
// truncation included), so layout assertions test the real output.
func modelRowLine(t *testing.T, m personasModel) string {
	t.Helper()
	view := stripANSI(m.View())
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Brain model") && strings.Contains(line, "‹") {
			return line
		}
	}
	t.Fatalf("no Brain model row in view:\n%s", view)
	return ""
}

// openPickerForm opens the edit form with a scan already folded in.
func openPickerForm(t *testing.T, cfg *config.Config, p *persona.Profile, scan map[string][]string) personasModel {
	t.Helper()
	m := newPickerPersonas(t, cfg, p)
	m.brainModels.discover = func(context.Context, *config.Config) []discovery.ProviderInfo {
		return fakeScan(scan)
	}
	m.cursor = 0
	m.beginEditAt(formRowName)
	m.applyBrainModels(personaModelsMsg{seq: m.brainModels.seq, providers: fakeScan(scan)})
	m.setFormRow(formRowBrainProvider)
	m.setFormRow(formRowBrainModel)
	return m
}

func TestPersonaBrainModelCustomTextIsNotDestroyedByCycling(t *testing.T) {
	// Leaving (custom) must cost nothing in either direction: → used to wrap
	// onto (default) and blank the field, while ← preserved it.
	scan := map[string][]string{"ollama": {"llama3", "qwen3:8b"}}
	cfg := &config.Config{BrainProvider: "ollama"}

	typeCustom := func(t *testing.T, m personasModel) personasModel {
		t.Helper()
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyLeft}) // (default) → (custom)
		if !m.brainModels.custom {
			t.Fatal("← from (default) did not reach (custom)")
		}
		for _, r := range "my-model" {
			m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		if got := m.formBrain().Model; got != "my-model" {
			t.Fatalf("typed model = %q, want my-model", got)
		}
		return m
	}

	for _, tc := range []struct {
		name     string
		leave    tea.KeyType
		wantLeft string // model value after leaving (custom)
	}{
		{name: "leaving forward wraps to (default)", leave: tea.KeyRight, wantLeft: ""},
		{name: "leaving backward keeps it as an entry", leave: tea.KeyLeft, wantLeft: "my-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := typeCustom(t, openPickerForm(t, cfg, &persona.Profile{ID: "test", DisplayName: "Test"}, scan))

			m, _ = m.updateForm(tea.KeyMsg{Type: tc.leave})
			if m.brainModels.custom {
				t.Fatal("still in custom after cycling away")
			}
			if got := m.formBrain().Model; got != tc.wantLeft {
				t.Fatalf("model after leaving custom = %q, want %q", got, tc.wantLeft)
			}

			// Cycling back restores what was typed, whichever way we left.
			back := tea.KeyLeft
			if tc.leave == tea.KeyLeft {
				back = tea.KeyRight
			}
			m, _ = m.updateForm(tea.KeyMsg{Type: back})
			if !m.brainModels.custom {
				t.Fatalf("cycling back did not return to (custom)")
			}
			if got := m.formBrain().Model; got != "my-model" {
				t.Fatalf("restored custom text = %q, want my-model", got)
			}
		})
	}

	t.Run("closing the form clears it", func(t *testing.T) {
		m := typeCustom(t, openPickerForm(t, cfg, &persona.Profile{ID: "test", DisplayName: "Test"}, scan))
		m.cancelForm()
		if m.brainModels.custom || m.brainModels.customText != "" {
			t.Fatalf("form close left custom=%v text=%q", m.brainModels.custom, m.brainModels.customText)
		}
		m.cursor = 0
		m.beginEditAt(formRowName)
		m.setFormRow(formRowBrainProvider)
		m.setFormRow(formRowBrainModel)
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyLeft})
		if got := m.formBrain().Model; got != "" {
			t.Fatalf("reopened form restored stale custom text %q", got)
		}
	})
}

func TestPersonaBrainModelRowHelpIsScopedToTheRow(t *testing.T) {
	// The (custom) hint used to stick to the status line after moving away.
	scan := map[string][]string{"ollama": {"llama3"}}
	m := openPickerForm(t, &config.Config{BrainProvider: "ollama"},
		&persona.Profile{ID: "test", DisplayName: "Test"}, scan)

	if !strings.Contains(m.message, "Model") || strings.Contains(m.message, "Type a model name") {
		t.Fatalf("row help = %q, want the Model row's own help", m.message)
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyLeft}) // → (custom)
	if !strings.Contains(m.message, "Type a model name") {
		t.Fatalf("custom row help = %q", m.message)
	}

	// Moving to another row restores the step help while custom stays selected.
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyDown})
	if m.formRow == formRowBrainModel {
		t.Fatal("↓ did not leave the Model row")
	}
	if strings.Contains(m.message, "Type a model name") {
		t.Fatalf("help after leaving the row = %q, want the new row's help", m.message)
	}

	// Coming back re-scopes it to the row that owns the typing keys.
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyUp})
	if m.formRow != formRowBrainModel {
		t.Fatal("↑ did not return to the Model row")
	}
	if !strings.Contains(m.message, "Type a model name") {
		t.Fatalf("help back on the custom row = %q", m.message)
	}

	// Cycling off (custom) on the row drops the row help too.
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if m.brainModels.custom {
		t.Fatal("→ did not leave custom")
	}
	if strings.Contains(m.message, "Type a model name") {
		t.Fatalf("help after leaving custom = %q, want the row help", m.message)
	}
}

func TestPersonaBrainModelRowFitsTheBox(t *testing.T) {
	const longName = "hf.co/lordx64/Qwen3.6-35B-A3B-Kimi-K2.6-Reasoning-Distilled-GGUF:IQ4_XS"
	tests := []struct {
		name     string
		width    int
		custom   bool
		pending  bool
		scan     map[string][]string
		selected string
		wantNote string
	}{
		{
			name:     "ollama down under (custom) still shows why",
			width:    100,
			custom:   true,
			scan:     map[string][]string{"ollama": nil},
			wantNote: "no ollama models discovered",
		},
		{
			name:     "a pending scan is visible under (custom)",
			width:    100,
			custom:   true,
			pending:  true,
			scan:     map[string][]string{"ollama": {"llama3"}},
			wantNote: "scanning models…",
		},
		{
			name:     "a narrow box still shows the note",
			width:    62,
			custom:   true,
			scan:     map[string][]string{"ollama": nil},
			wantNote: "no ollama",
		},
		{
			name:     "a long model name keeps its closing chevron",
			width:    80,
			scan:     map[string][]string{"ollama": {longName}},
			selected: longName,
		},
		{
			name:     "a long model name in a narrow box keeps it too",
			width:    40,
			scan:     map[string][]string{"ollama": {longName}},
			selected: longName,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{BrainProvider: "ollama"}
			m := newPickerPersonas(t, cfg, &persona.Profile{
				ID: "test", DisplayName: "Test",
				Brain: persona.Brain{Provider: "ollama", Model: tc.selected},
			})
			m.width = tc.width
			m.brainModels.discover = func(context.Context, *config.Config) []discovery.ProviderInfo {
				return fakeScan(tc.scan)
			}
			m.cursor = 0
			m.beginEditAt(formRowName)
			if !tc.pending {
				m.applyBrainModels(personaModelsMsg{seq: m.brainModels.seq, providers: fakeScan(tc.scan)})
			}
			m.setFormRow(formRowBrainProvider)
			m.setFormRow(formRowBrainModel)
			if tc.custom {
				m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyLeft})
				if !m.brainModels.custom {
					t.Fatal("could not reach (custom)")
				}
			}

			line := modelRowLine(t, m)
			// The chip always closes: the value is never cut mid-name.
			if !strings.Contains(line, "›") {
				t.Errorf("model row lost its closing chevron:\n%s", line)
			}
			if tc.wantNote != "" && !strings.Contains(line, tc.wantNote) {
				t.Errorf("model row hid the scan note %q:\n%s", tc.wantNote, line)
			}
			// The row must fit its share of the box, so nothing on it is ever
			// truncated away by formBox.
			budget := m.formBoxWidth() - 12
			if w := ansi.StringWidth(stripANSI(m.stackModelRowValue())); w > budget {
				t.Errorf("model row is %d columns wide, over its %d budget:\n%s", w, budget, line)
			}
		})
	}
}
