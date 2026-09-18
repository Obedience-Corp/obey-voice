//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// TestVS0001IsolatedServeProof is an opt-in proof launcher for an actual
// fixture-backed serve. It deliberately does not call config.Load* or any
// process-wide store/path helper: every input and every write is below the
// root named by VS0001_SERVE_PROOF_ROOT.
//
// The test starts the real pipeline, selector, optional work checkpoint,
// dispatcher, and TLS server, then waits for root/stop or SIGINT/SIGTERM. It
// does not submit a turn, so an Ollama model run is outside this proof's
// scope. Both work-enabled and ordinary work-disabled fixture configs are
// supported; the latter exercises the host-owned identity path.
func TestVS0001IsolatedServeProof(t *testing.T) {
	root := os.Getenv("VS0001_SERVE_PROOF_ROOT")
	if root == "" {
		t.Skip("set VS0001_SERVE_PROOF_ROOT to run the isolated serve proof")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("VS0001_SERVE_PROOF_ROOT must be an absolute path")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("fixture root is not a directory: %s", root)
	}

	cfg := isolatedServeConfig(t, root)
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := loadIsolatedSession(t, store)

	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	personaID := sess.SelectedPersonaID
	if personaID == "" {
		personaID = persona.ActiveID(cfg)
	}
	binding, err := catalog.ResolveBinding(cfg, personaID)
	if err != nil {
		t.Fatalf("resolve fixture persona: %v", err)
	}
	cfg = binding.Config()
	confineServeFixturePaths(t, root, cfg)

	ctx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	bus := events.NewBus()
	p, fanout, _, liveTTS, cleanup, err := buildServePipeline(ctx, cfg, bus, true, false)
	if err != nil {
		t.Fatalf("build fixture serve pipeline: %v", err)
	}
	defer cleanup()

	// Restore the explicitly loaded session into the real provider before the
	// selector captures its initial identity and canonical snapshot.
	p.Brain.LoadHistory(sess.Turns)
	checkpoint, err := bindWorkSession(p, cfg, sess, store)
	if err != nil {
		t.Fatalf("bind fixture work session: %v", err)
	}
	ref := &sessionRef{sess: sess, checkpoint: checkpoint}
	var saveErr error
	p.OnTurn = func() {
		ref.mu.Lock()
		defer ref.mu.Unlock()
		ref.checkpoint.retry()
		if err := store.Save(ref.sess, p.Brain.History()); err != nil {
			saveErr = errors.Join(saveErr, err)
			if p.Events != nil {
				p.Events.Emit(events.Error{Stage: "session", Message: fmt.Sprintf("isolated proof save: %v", err)})
			}
		}
	}

	selector, err := personaruntime.NewSelector(personaruntime.SelectionOptions{
		Base:      cfg.Clone(),
		Catalog:   catalog,
		Pipeline:  p,
		Voices:    liveTTS,
		SessionID: sess.ID,
		Initial:   binding,
		Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
			ref.mu.Lock()
			defer ref.mu.Unlock()
			return store.SaveSelection(ref.sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash)
		},
	})
	if err != nil {
		t.Fatalf("construct fixture persona selector: %v", err)
	}
	defer selector.Close()
	p.AdmitInput = selector.Route

	switcher := &servePersonaSwitcher{
		base: cfg.Clone(), cfg: cfg, selector: selector, pipeline: p, tts: liveTTS, sessions: ref,
	}
	clear := func() {
		if err := switcher.clear(ctx); err != nil && p.Events != nil {
			p.Events.Emit(events.Error{Stage: "work_clear", Message: err.Error()})
		}
		if p.OnTurn != nil {
			p.OnTurn()
		}
	}
	dispatcher := netapi.NewDispatcher(p, bus, clear, nil)
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		dispatcher.Run(ctx)
	}()

	stopFile := filepath.Join(root, "stop")
	if err := os.Remove(stopFile); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	credsDir := filepath.Join(root, "serve")
	creds, err := netapi.LoadOrCreateCredentials(credsDir)
	if err != nil {
		t.Fatalf("create fixture serve credentials: %v", err)
	}
	server := netapi.New(netapi.Options{
		Bind:                   "127.0.0.1:0",
		Credentials:            creds,
		Bus:                    bus,
		Dispatcher:             dispatcher,
		CurrentSessionSnapshot: switcher.snapshotProvider(),
		SetPersonaContext:      switcher.applyContext,
		ListPersonas: func() ([]netapi.PersonaSummary, error) {
			profiles, err := catalog.List()
			if err != nil {
				return nil, err
			}
			var rows []netapi.PersonaSummary
			for _, profile := range profiles {
				binding, err := catalog.ResolveBinding(cfg, profile.ID)
				if err != nil {
					return nil, err
				}
				resolved := binding.Config()
				rows = append(rows, netapi.PersonaSummary{ID: binding.PersonaID, DisplayName: binding.DisplayName, Active: binding.PersonaID == selector.Snapshot().PersonaID,
					Brain: netapi.PersonaBrain{Provider: binding.BrainProvider, Model: binding.BrainModel}, TTS: netapi.PersonaTTS{Provider: resolved.TTSProvider, Voice: activeVoiceFor(resolved), Tier: activeTierFor(resolved)}})
			}
			return rows, nil
		},
		Audio:        fanout,
		LoopbackHTTP: false,
		WorkDir:      cfg.WorkDir,
		Providers:    netapi.Providers{Brain: cfg.BrainProvider, TTS: cfg.TTSProvider},
	})

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe(ctx) }()
	addr, err := waitForServeAddress(ctx, server, serverErr)
	if err != nil {
		t.Fatalf("start isolated TLS serve: %v", err)
	}
	readyPath := filepath.Join(credsDir, "credentials.json")
	if err := writeIsolatedReady(readyPath, addr, creds); err != nil {
		t.Fatalf("write isolated serve credentials: %v", err)
	}
	t.Logf("isolated serve ready file: %s", readyPath)

	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(stopFile); err == nil {
					stopSignal()
					return
				}
			}
		}
	}()
	err = <-serverErr
	stopSignal()
	stopWatch()
	<-watchDone
	<-dispatcherDone
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("isolated serve shutdown: %v", err)
	}

	if p.OnTurn != nil {
		p.OnTurn()
	}
	// Selection publication must be joined before the work and TTS owners
	// below are closed, matching the production shutdown ordering.
	selector.Close()
	if err := closeWorkSession(p, checkpoint); err != nil {
		t.Errorf("close fixture work session: %v", err)
	}
	if saveErr != nil {
		t.Errorf("persist fixture session: %v", saveErr)
	}
	final, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.MarshalIndent(final, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "session-final.json"), append(payload, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

type isolatedServeReady struct {
	URL         string `json:"url"`
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
	Token       string `json:"token"`
}

func isolatedServeConfig(t *testing.T, root string) *config.Config {
	t.Helper()
	for _, path := range []string{filepath.Join(root, "config.yaml"), filepath.Join(root, "personas"), filepath.Join(root, "prompts"), filepath.Join(root, "models")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fixture path is required: %s: %v", path, err)
		}
	}
	v := viper.New()
	// Match the owning config default: zero would erase provider history after
	// every turn even though the canonical transcript remains complete.
	if spec, ok := config.SpecFor("max_history"); ok {
		v.SetDefault(spec.Key, spec.Default)
	}
	v.SetConfigFile(filepath.Join(root, "config.yaml"))
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read fixture config: %v", err)
	}
	var cfg config.Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("decode fixture config: %v", err)
	}
	// These paths are deliberate fixture-owned inputs. In particular, an empty
	// SkillsDir would make the brain consult the process-wide default path.
	cfg.WorkDir = root
	cfg.PromptsDir = filepath.Join(root, "prompts")
	cfg.ModelsDir = filepath.Join(root, "models")
	cfg.SkillsDir = filepath.Join(root, "skills")
	return &cfg
}

func confineServeFixturePaths(t *testing.T, root string, cfg *config.Config) {
	t.Helper()
	for name, path := range map[string]string{
		"work_dir": cfg.WorkDir, "prompts_dir": cfg.PromptsDir, "models_dir": cfg.ModelsDir, "skills_dir": cfg.SkillsDir,
	} {
		if !pathWithin(root, path) {
			t.Fatalf("fixture %s escapes proof root: %q", name, path)
		}
	}
	for name, path := range map[string]string{
		"qwen_tts_binary":          cfg.QwenTTSBinary,
		"qwen_tts_model":           cfg.QwenTTSModel,
		"qwen_tts_reference_audio": cfg.QwenTTSReferenceAudio,
	} {
		if path == "" {
			continue
		}
		path = fixturePath(root, path)
		if !pathWithin(root, path) {
			t.Fatalf("fixture %s escapes proof root: %q", name, path)
		}
	}
	cfg.QwenTTSBinary = fixturePath(root, cfg.QwenTTSBinary)
	cfg.QwenTTSModel = fixturePath(root, cfg.QwenTTSModel)
	cfg.QwenTTSReferenceAudio = fixturePath(root, cfg.QwenTTSReferenceAudio)
}

func fixturePath(root, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func pathWithin(root, path string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && (rel == "." || (len(rel) >= 3 && rel[:3] != ".."+string(filepath.Separator)))
}

func loadIsolatedSession(t *testing.T, store *session.Store) *session.Session {
	t.Helper()
	id := os.Getenv("VS0001_SERVE_PROOF_SESSION_ID")
	if id == "" {
		entries := store.List()
		if len(entries) == 0 {
			t.Fatal("fixture store has no session; set VS0001_SERVE_PROOF_SESSION_ID or add root/sessions/*.json")
		}
		id = entries[0].ID
	}
	sess, err := store.Load(id)
	if err != nil {
		t.Fatalf("load fixture session %q: %v", id, err)
	}
	// The second explicit load proves the runtime starts from the persisted
	// representation rather than retaining Store.List's display copy.
	reloaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("reload fixture session %q: %v", sess.ID, err)
	}
	return reloaded
}

func waitForServeAddress(ctx context.Context, server *netapi.Server, serverErr <-chan error) (net.Addr, error) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if addr := server.Addr(); addr != nil {
			return addr, nil
		}
		select {
		case err := <-serverErr:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, errors.New("serve listener did not become ready")
		case <-ticker.C:
		}
	}
}

func writeIsolatedReady(path string, addr net.Addr, creds *netapi.Credentials) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("serve address is not TCP: %T", addr)
	}
	payload, err := json.MarshalIndent(isolatedServeReady{
		URL:         "https://127.0.0.1:" + fmt.Sprint(tcp.Port),
		Port:        tcp.Port,
		Fingerprint: creds.Fingerprint,
		Token:       creds.Token,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func TestIsolatedServeConfigRetainsDefaultHistory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"personas", "prompts", "models"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("brain_provider: ollama\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if cfg := isolatedServeConfig(t, root); cfg.MaxHistory != 40 {
		t.Fatalf("max_history=%d, want normal history retention", cfg.MaxHistory)
	}
}
