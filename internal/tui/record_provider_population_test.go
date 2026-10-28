package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/capture"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	tea "github.com/charmbracelet/bubbletea"
)

type providerFixtureRelease struct {
	At     time.Time           `json:"released_at"`
	MonoMS *float64            `json:"runtime_release_ms"`
	Trace  events.TraceContext `json:"trace"`
}
type providerPopulationEntry struct {
	Pair    int  `json:"pair"`
	Warmup  bool `json:"warmup"`
	Capture bool `json:"capture"`
}
type providerPopulation struct {
	Entries          []providerPopulationEntry
	Index            int
	Observed, Valid  int
	Awaiting, Active bool
}
type providerPollMsg struct{}

func providerPollCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg { return providerPollMsg{} })
}
func newProviderPopulation(root string) (*providerPopulation, error) {
	raw := os.Getenv("OBEY_VOICE_CAPTURE_PROVIDER_PAIRS")
	if raw == "" || raw == "0" {
		return nil, nil
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 || count > 120 {
		return nil, fmt.Errorf("pairs must be1..120")
	}
	seed := int64(7007)
	if value := os.Getenv("OBEY_VOICE_CAPTURE_PROVIDER_SEED"); value != "" {
		seed, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, err
		}
	}
	entries := []providerPopulationEntry{{Pair: 0, Warmup: true, Capture: false}, {Pair: 0, Warmup: true, Capture: true}}
	rng := rand.New(rand.NewSource(seed))
	for pair := 1; pair <= count; pair++ {
		first := rng.Intn(2) == 1
		entries = append(entries, providerPopulationEntry{Pair: pair, Capture: first}, providerPopulationEntry{Pair: pair, Capture: !first})
	}
	artifact := map[string]any{"seed": seed, "entries": entries, "input_reset": "same finite WAV reset each turn", "history_reset": "production Brain.ClearHistory while gated before each utterance", "excluded_from_quantiles": "predefined warmup entries and failed/missing-boundary attempts only; retain every raw attempt", "retry_policy": "no winner-dependent reruns; missing pairs fail evidence", "capture_lifecycle": "adjacent on entries share capture; off detaches and seals before next arm", "measurement": "finalized_input to first software playback; no physical acoustic onset"}
	if err := writeProviderJSON(filepath.Join(root, "population-plan.json"), artifact); err != nil {
		return nil, err
	}
	return &providerPopulation{Entries: entries, Awaiting: true}, nil
}
func writeProviderJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0600)
}
func writeProviderProvenance(root, cfgPath, wav string, cfg *config.Config) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	binaryHash, err := providerFixtureHash(executable)
	if err != nil {
		return err
	}
	configHash, err := providerFixtureHash(cfgPath)
	if err != nil {
		return err
	}
	wavHash, err := providerFixtureHash(wav)
	if err != nil {
		return err
	}
	provider, mode, model := captureSTTEnvironment(cfg)
	return writeProviderJSON(filepath.Join(root, "provider-provenance.json"), map[string]any{"binary_sha256": binaryHash, "source_revision": os.Getenv("OBEY_VOICE_CAPTURE_SOURCE_REVISION"), "go_version": runtime.Version(), "fixture_config_sha256": configHash, "wav_path": wav, "wav_sha256": wavHash, "input_kind": "realtime finite recorded acoustic fixture; no physical mic", "gate": "first cancellable fixture ReadFrame; STT.Start returns real session promptly; total turn/STT includes pre-release wait; see fixture_release rows", "brain_provider": cfg.BrainProvider, "brain_model": config.BrainModelFor(cfg, cfg.BrainProvider), "stt_provider": provider, "stt_mode": mode, "stt_model": model, "tts_provider": cfg.TTSProvider, "tts_voice": cfg.TTSVoice, "work_enabled": cfg.VoiceWorkEnabled, "output_boundary": "software local sink acceptance/Done; not calibrated acoustic onset"})
}
func (m providerRecordTerminal) populationTick() (tea.Model, tea.Cmd) {
	for {
		select {
		case release := <-m.released:
			if err := m.log.Encode(map[string]any{"type": "fixture_release", "release": release}); err != nil {
				*m.logErr = err
			}
		default:
			goto drained
		}
	}
drained:
	commands := []tea.Cmd{providerPollCmd()}
	pop := m.population
	if pop == nil || !pop.Awaiting || m.app.runtime == nil {
		return m, tea.Batch(commands...)
	}
	conv := &m.app.conversation
	if pop.Index >= len(pop.Entries) {
		if conv.capture != nil {
			conv.configureRecord([]string{"off"})
		}
		if conv.capturePending != nil {
			return m, tea.Batch(append(commands, conv.capturePollCmd())...)
		}
		next, command := m.app.Update(quitMsg{})
		m.app = next.(App)
		return m, command
	}
	if conv.turnState != turnVoiceListening {
		return m, tea.Batch(commands...)
	}
	entry := pop.Entries[pop.Index]
	if conv.capturePending != nil {
		if conv.captureSealWarned {
			*m.logErr = fmt.Errorf("population stopped: previous capture did not seal within2s")
			next, cmd := m.app.Update(quitMsg{})
			m.app = next.(App)
			return m, cmd
		}
		return m, tea.Batch(append(commands, conv.capturePollCmd())...)
	}
	if entry.Capture && conv.capture == nil {
		conv.configureRecord([]string{"talk"})
		if conv.capture == nil {
			*m.logErr = fmt.Errorf("population stopped: requested capture failed to start")
			next, cmd := m.app.Update(quitMsg{})
			m.app = next.(App)
			return m, cmd
		}
		return m, tea.Batch(append(commands, conv.capturePollCmd())...)
	}
	if !entry.Capture && conv.capture != nil {
		conv.configureRecord([]string{"off"})
		return m, tea.Batch(append(commands, conv.capturePollCmd())...)
	}
	m.app.runtime.Pipeline.Brain.ClearHistory()
	conv.commandNotice(fmt.Sprintf("Stabilized capture benchmark · pair %d · warmup %v · capture %v", entry.Pair, entry.Warmup, entry.Capture))
	select {
	case m.permit <- struct{}{}:
		pop.Active = true
		pop.Awaiting = false
	default:
	}
	return m, tea.Batch(commands...)
}

func TestCapturePopulationPlanIsPairedAndPinnedBeforeRun(t *testing.T) {
	t.Setenv("OBEY_VOICE_CAPTURE_PROVIDER_PAIRS", "30")
	t.Setenv("OBEY_VOICE_CAPTURE_PROVIDER_SEED", "7007")
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	first, err := newProviderPopulation(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newProviderPopulation(secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(firstRoot, "population-plan.json"))
	b, _ := os.ReadFile(filepath.Join(secondRoot, "population-plan.json"))
	if !bytes.Equal(a, b) || len(first.Entries) != 62 || !reflect.DeepEqual(first.Entries, second.Entries) {
		t.Fatal("seeded pre-run population changed")
	}
	seen := map[int][2]bool{}
	for _, entry := range first.Entries {
		if entry.Warmup {
			continue
		}
		arms := seen[entry.Pair]
		index := 0
		if entry.Capture {
			index = 1
		}
		if arms[index] {
			t.Fatal("duplicate population arm")
		}
		arms[index] = true
		seen[entry.Pair] = arms
	}
	if len(seen) != 30 {
		t.Fatal("missing pairs")
	}
	for _, arms := range seen {
		if !arms[0] || !arms[1] {
			t.Fatal("unmatched pair")
		}
	}
	first.Index = 61
	after, _ := os.ReadFile(filepath.Join(firstRoot, "population-plan.json"))
	if !bytes.Equal(a, after) {
		t.Fatal("runtime state changed predeclared plan")
	}
}

func validateProviderCaptureArtifacts(root string, pop *providerPopulation) error {
	expectedOn := 0
	for _, entry := range pop.Entries {
		if entry.Capture {
			expectedOn++
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "observations.jsonl"))
	if err != nil {
		return err
	}
	ids := map[string]string{}
	onRows := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var row struct {
			Type        string                   `json:"type"`
			Population  *providerPopulationEntry `json:"population"`
			CaptureID   string                   `json:"capture_id"`
			CapturePath string                   `json:"capture_path"`
		}
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		if row.Type != "turn" || row.Population == nil || !row.Population.Capture {
			continue
		}
		onRows++
		if row.CaptureID == "" || row.CapturePath == "" {
			return fmt.Errorf("capture-on attempt missing actual capture identity/path")
		}
		if prior, ok := ids[row.CaptureID]; ok && prior != row.CapturePath {
			return fmt.Errorf("capture identity maps to multiple files")
		}
		ids[row.CaptureID] = row.CapturePath
	}
	if onRows != expectedOn {
		return fmt.Errorf("planned%d capture-on rows, found%d", expectedOn, onRows)
	}
	paths, err := filepath.Glob(filepath.Join(root, "captures", "*", "manifest.json"))
	if err != nil {
		return err
	}
	if len(paths) != len(ids) {
		return fmt.Errorf("observed%d unique capture IDs, found%d manifests", len(ids), len(paths))
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var manifest capture.Manifest
		if err = json.Unmarshal(raw, &manifest); err != nil {
			return err
		}
		eventsPath, known := ids[manifest.CaptureID]
		if !known || filepath.Clean(eventsPath) != filepath.Join(filepath.Dir(path), "events.jsonl") || manifest.Status != "sealed" || manifest.Health.Dropped != 0 || manifest.Health.Skipped != 0 || manifest.Health.WriteErrors != 0 {
			return fmt.Errorf("incomplete/conflicting population capture %s", path)
		}
	}
	return nil
}

type fixtureGateContractProvider struct{ source *gatedFixtureSource }
type fixtureGateContractSession struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	events chan stt.Event
}

func (*fixtureGateContractProvider) Available() bool { return true }
func (p *fixtureGateContractProvider) Start(ctx context.Context) (stt.Session, error) {
	child, cancel := context.WithCancel(ctx)
	s := &fixtureGateContractSession{cancel: cancel, done: make(chan struct{}), events: make(chan stt.Event)}
	go func() { defer close(s.done); _, s.err = p.source.ReadFrame(child) }()
	return s, nil
}
func (s *fixtureGateContractSession) Events() <-chan stt.Event { return s.events }
func (s *fixtureGateContractSession) Close() error             { s.cancel(); <-s.done; return nil }
func testGatedFixtureSource(t *testing.T) *gatedFixtureSource {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.wav")
	if err := audio.WriteWAVFloat32(path, audio.SampleRate, make([]float32, audio.ChunkSize*4)); err != nil {
		t.Fatal(err)
	}
	source, err := audio.NewFixtureSourceFromWAV(path, audio.ChunkSize, true)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &gatedFixtureSource{FixtureSource: source, permit: make(chan struct{}, 1), released: make(chan providerFixtureRelease, 1), dropped: &atomic.Int64{}, closed: make(chan struct{})}
	t.Cleanup(func() { _ = wrapped.Close() })
	return wrapped
}
func TestCaptureFixtureGatePreservesSessionCancellation(t *testing.T) {
	source := testGatedFixtureSource(t)
	provider := &gatedFixtureSTT{source: source, provider: &fixtureGateContractProvider{source: source}}
	outer, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan stt.Session, 1)
	go func() {
		session, err := provider.Start(context.WithoutCancel(outer))
		if err == nil {
			returned <- session
		}
	}()
	var session stt.Session
	select {
	case session = <-returned:
	case <-time.After(time.Second):
		t.Fatal("Start waited for fixture permit instead of returning native session")
	}
	cancel() // work capture deliberately detached its Start context
	native, ok := session.(*fixtureGateContractSession)
	if !ok {
		t.Fatal("Start replaced native session/control capability")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(native.err, context.Canceled) {
		t.Fatal("Close did not cancel gated ReadFrame", native.err)
	}
}
func TestCaptureFixtureGateRestartsRealtimeAtRelease(t *testing.T) {
	source := testGatedFixtureSource(t)
	permit := make(chan struct{}, 1)
	released := make(chan providerFixtureRelease, 1)
	source.permit = permit
	source.released = released
	source.waiting = true
	first := make(chan struct{})
	go func() { _, _ = source.ReadFrame(context.Background()); close(first) }()
	chunk := audio.SamplesDuration(audio.ChunkSize)
	time.Sleep(chunk*3 + time.Millisecond)
	permit <- struct{}{}
	<-first
	stamp := <-released
	if _, err := source.ReadFrame(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadFrame(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(stamp.At); elapsed < chunk*3/2 {
		t.Fatal("old pacing clock caused catch-up burst after gate", elapsed, chunk)
	}
}
