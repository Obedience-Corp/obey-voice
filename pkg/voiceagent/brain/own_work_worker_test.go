//go:build darwin || linux

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/cursor-agent-go/pkg/cursor"
	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"
	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const (
	ownWorkLiveFirst  = "LIVE-MARKER-alpha"
	ownWorkLiveSecond = "LIVE-MARKER-beta"
	ownWorkBrief      = "OWN-BRIEF tidy the notes folder"
	ownWorkReply      = "Own work done."
	ownWorkLogEnv     = "OBEY_OWN_WORK_FAKE_LOG"
)

// ownWorkCall is one start of a fake backend: its argv (without the binary),
// working directory, stdin, and for Codex the JSON-RPC requests it received.
type ownWorkCall struct {
	Backend  string           `json:"backend"`
	Args     []string         `json:"args"`
	Cwd      string           `json:"cwd"`
	Stdin    string           `json:"stdin,omitempty"`
	Session  string           `json:"session,omitempty"`
	Socket   string           `json:"-"`
	Requests []ownWorkRequest `json:"-"`
}

type ownWorkRequest struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func (c ownWorkCall) request(method string) (map[string]any, bool) {
	for _, r := range c.Requests {
		if r.Method == method {
			return r.Params, true
		}
	}
	return nil, false
}

// text is everything the process was given, for searching.
func (c ownWorkCall) text() string {
	parts := append(append([]string(nil), c.Args...), c.Stdin)
	for _, r := range c.Requests {
		data, _ := json.Marshal(r)
		parts = append(parts, string(data))
	}
	return strings.Join(parts, "\n")
}

// TestOwnWorkFakeCLIHelper stands in for claude, cursor-agent, grok, muse and
// codex when re-executed by a fixture shim; otherwise it does nothing.
func TestOwnWorkFakeCLIHelper(t *testing.T) {
	logPath := os.Getenv(ownWorkLogEnv)
	if logPath == "" {
		return
	}
	i := slices.Index(os.Args, "--")
	if i < 0 || i+1 >= len(os.Args) {
		os.Exit(80)
	}
	cwd, _ := os.Getwd()
	call := ownWorkCall{Backend: os.Args[i+1], Args: os.Args[i+2:], Cwd: cwd}
	if call.Backend != "codex" {
		data, _ := io.ReadAll(os.Stdin)
		call.Stdin = string(data)
		call.Session = fmt.Sprintf("%s-session-%d-%d", call.Backend, os.Getpid(), time.Now().UnixNano())
	}
	line, _ := json.Marshal(call)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(81)
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		os.Exit(82)
	}
	if f.Close() != nil {
		os.Exit(83)
	}
	var reply any
	switch call.Backend {
	case "codex":
		TestCodexAppServerHelper(t)
		os.Exit(84)
	case "claude", "cursor-agent":
		reply = map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": ownWorkReply, "session_id": call.Session}
	case "grok":
		reply = map[string]any{"text": ownWorkReply, "sessionId": call.Session, "stopReason": "end_turn"}
	case "muse":
		reply = map[string]any{"payload_type": "run.terminal.completed", "payload": map[string]any{"terminal": "completed", "text": ownWorkReply}, "stream": map[string]any{"id": call.Session}}
	default:
		os.Exit(85)
	}
	if json.NewEncoder(os.Stdout).Encode(reply) != nil {
		os.Exit(86)
	}
	os.Exit(0)
}

// ownWorkFixture is one backend's fake on a PATH that holds nothing else.
type ownWorkFixture struct {
	dir     string
	binary  string
	phase   int
	capture func(t *testing.T, fx *ownWorkFixture, run func()) ownWorkCall
}

func (fx *ownWorkFixture) run(t *testing.T, run func()) ownWorkCall {
	t.Helper()
	fx.phase++
	return fx.capture(t, fx, run)
}

func ownWorkIsolate(t *testing.T, dir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CURSOR_AGENT_INSTALL_DIR", t.TempDir())
	config.SetConfigDirForTest(t, t.TempDir())
}

func installOwnWorkShim(t *testing.T, name string) *ownWorkFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := "#!/bin/sh\nexec " + quote(exe) + " -test.run='^TestOwnWorkFakeCLIHelper$' -- " + name + " \"$@\"\n"
	binary := filepath.Join(dir, name)
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ownWorkIsolate(t, dir)
	if name == "cursor-agent" {
		t.Setenv("CURSOR_AGENT_BIN", binary)
	}
	return &ownWorkFixture{dir: dir, binary: binary, capture: captureOwnWorkShim}
}

func captureOwnWorkShim(t *testing.T, fx *ownWorkFixture, run func()) ownWorkCall {
	t.Helper()
	logPath := filepath.Join(fx.dir, fmt.Sprintf("calls-%d.jsonl", fx.phase))
	codexLog := filepath.Join(fx.dir, fmt.Sprintf("codex-%d.jsonl", fx.phase))
	t.Setenv(ownWorkLogEnv, logPath)
	t.Setenv("OBEY_CODEX_TEST_HELPER", "1")
	t.Setenv("OBEY_CODEX_TEST_MODE", "")
	t.Setenv("OBEY_CODEX_TEST_LOG", codexLog)
	run()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the fake was not started: %v", err)
	}
	var calls []ownWorkCall
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var c ownWorkCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	if len(calls) != 1 {
		t.Fatalf("one turn started %d processes: %+v", len(calls), calls)
	}
	call := calls[0]
	if call.Backend == "codex" {
		data, err := os.ReadFile(codexLog)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var r ownWorkRequest
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatal(err)
			}
			if r.Method != "" {
				call.Requests = append(call.Requests, r)
			}
		}
	}
	return call
}

func installOwnWorkObey(t *testing.T) (*ownWorkFixture, *config.Config) {
	t.Helper()
	obeyCfg, root := installObeyFixture(t)
	ownWorkIsolate(t, root)
	return &ownWorkFixture{dir: root, capture: captureOwnWorkObey}, obeyCfg
}

func captureOwnWorkObey(t *testing.T, fx *ownWorkFixture, run func()) ownWorkCall {
	t.Helper()
	receipt := filepath.Join(fx.dir, "request.json")
	if err := os.Remove(receipt); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	run()
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatalf("the fake obey run was not started: %v", err)
	}
	var r struct {
		Args                []string
		Socket, Prompt, Cwd string
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return ownWorkCall{Backend: "obey", Args: r.Args, Cwd: r.Cwd, Stdin: r.Prompt, Socket: r.Socket}
}

// ownWorkFlag is the value after flag, looking only before a "--" separator.
func ownWorkFlag(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == flag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

func ownWorkHas(args []string, flag string) bool {
	_, ok := ownWorkFlag(args, flag)
	return ok
}

func wantOwnWorkFlag(t *testing.T, call ownWorkCall, flag, want string) {
	t.Helper()
	if got, ok := ownWorkFlag(call.Args, flag); !ok || got != want {
		t.Errorf("%s %s = %q (present=%v), want %q; args=%q", call.Backend, flag, got, ok, want, call.Args)
	}
}

func wantOwnWorkSwitch(t *testing.T, call ownWorkCall, flag string, present bool) {
	t.Helper()
	if ownWorkHas(call.Args, flag) != present {
		t.Errorf("%s %s present=%v, want %v; args=%q", call.Backend, flag, !present, present, call.Args)
	}
}

type ownWorkBackend struct {
	provider string
	model    string
	install  func(t *testing.T) (*ownWorkFixture, *config.Config)
	// persona sets the backend's own fields on a persona config; base is the
	// fixture's config where the fixture supplies one.
	persona func(cfg, base *config.Config)
	// continued reports whether next carried the live conversation of prev:
	// a resumed CLI session, or the earlier turn replayed into the prompt.
	continued func(prev, next ownWorkCall) bool
	// fresh fails when the worker's start asks to resume anything.
	fresh func(t *testing.T, call ownWorkCall)
	// policy asserts the permission, sandbox and approval surface.
	policy func(t *testing.T, call ownWorkCall, tools bool, cfg *config.Config)
}

func resumedOwnWorkSession(prev, next ownWorkCall) bool {
	id, ok := ownWorkFlag(next.Args, "--resume")
	return ok && prev.Session != "" && id == prev.Session
}

func replayedOwnWorkTurn(_, next ownWorkCall) bool {
	return strings.Contains(next.text(), ownWorkLiveFirst)
}

func noOwnWorkResume(flags ...string) func(t *testing.T, call ownWorkCall) {
	return func(t *testing.T, call ownWorkCall) {
		t.Helper()
		for _, flag := range flags {
			wantOwnWorkSwitch(t, call, flag, false)
		}
	}
}

func installNamedOwnWorkShim(name string) func(t *testing.T) (*ownWorkFixture, *config.Config) {
	return func(t *testing.T) (*ownWorkFixture, *config.Config) {
		return installOwnWorkShim(t, name), nil
	}
}

var ownWorkBackends = []ownWorkBackend{
	{
		provider:  "claude",
		model:     "persona-claude-model",
		install:   installNamedOwnWorkShim("claude"),
		persona:   func(cfg, _ *config.Config) { cfg.ClaudeModel = "persona-claude-model" },
		continued: resumedOwnWorkSession,
		fresh:     noOwnWorkResume("--resume", "--continue", "--session-id", "--fork-session"),
		policy: func(t *testing.T, call ownWorkCall, tools bool, cfg *config.Config) {
			wantOwnWorkFlag(t, call, "--model", "persona-claude-model")
			wantOwnWorkSwitch(t, call, "--no-session-persistence", true)
			wantOwnWorkFlag(t, call, "--disallowedTools", "Bash(camp list),Bash(camp list:*),Bash(camp list *)")
			wantOwnWorkSwitch(t, call, "--allow-dangerously-skip-permissions", false)
			wantOwnWorkSwitch(t, call, "--add-dir", false)
			if tools {
				wantOwnWorkFlag(t, call, "--permission-mode", "bypassPermissions")
				wantOwnWorkSwitch(t, call, "--tools", false)
			} else {
				wantOwnWorkFlag(t, call, "--permission-mode", "default")
				wantOwnWorkFlag(t, call, "--tools", "")
			}
		},
	},
	{
		provider: "codex",
		model:    "persona-codex-model",
		install:  installNamedOwnWorkShim("codex"),
		persona:  func(cfg, _ *config.Config) { cfg.CodexModel = "persona-codex-model" },
		continued: func(_, next ownWorkCall) bool {
			p, ok := next.request("thread/resume")
			return ok && p["threadId"] == "thread-1"
		},
		fresh: func(t *testing.T, call ownWorkCall) {
			t.Helper()
			if _, ok := call.request("thread/resume"); ok {
				t.Error("the own worker resumed a Codex thread")
			}
			start, ok := call.request("thread/start")
			if !ok {
				t.Fatalf("the own worker started no Codex thread: %+v", call.Requests)
			}
			if _, ok := start["threadId"]; ok {
				t.Errorf("thread/start named a thread: %v", start["threadId"])
			}
			// A fresh thread, but not an ephemeral one, as for a Codex worker.
			if start["ephemeral"] != false {
				t.Errorf("thread/start ephemeral = %v, want false", start["ephemeral"])
			}
		},
		policy: func(t *testing.T, call ownWorkCall, tools bool, cfg *config.Config) {
			want := []string{"app-server", "--listen", "stdio://", "-c", "features.codex_hooks=false", "-c", "notify=[]"}
			if !slices.Equal(call.Args, want) {
				t.Errorf("codex argv = %q, want %q", call.Args, want)
			}
			start, _ := call.request("thread/start")
			if start["approvalPolicy"] != "never" || start["model"] != "persona-codex-model" || !sameOwnWorkDir(start["cwd"], cfg.WorkDir) {
				t.Errorf("thread/start = approval %v, model %v, cwd %v", start["approvalPolicy"], start["model"], start["cwd"])
			}
			policy, _ := start["config"].(map[string]any)
			if policy["features.codex_hooks"] != false {
				t.Errorf("hooks not disabled: %v", policy["features.codex_hooks"])
			}
			_, readConfig := call.request("config/read")
			if tools {
				if start["sandbox"] != "workspace-write" {
					t.Errorf("sandbox = %v, want workspace-write", start["sandbox"])
				}
				roots, _ := policy["sandbox_workspace_write.writable_roots"].([]any)
				if policy["sandbox_workspace_write.network_access"] != false || roots == nil || len(roots) != 0 ||
					policy["sandbox_workspace_write.exclude_tmpdir_env_var"] != true || policy["sandbox_workspace_write.exclude_slash_tmp"] != true {
					t.Errorf("workspace-write policy = %v", policy)
				}
				if readConfig {
					t.Error("tools-on turn read the integration config")
				}
				return
			}
			if start["sandbox"] != "read-only" {
				t.Errorf("sandbox = %v, want read-only", start["sandbox"])
			}
			for _, key := range []string{"features.shell_tool", "features.unified_exec", "features.apply_patch_freeform", "features.apps", "features.plugins", "tools.update_plan.enabled"} {
				if policy[key] != false {
					t.Errorf("%s = %v, want false", key, policy[key])
				}
			}
			if policy["web_search"] != "disabled" {
				t.Errorf("web_search = %v", policy["web_search"])
			}
			for key, name := range map[string]string{"mcp_servers": "company.tools", "plugins": "fixture@local"} {
				entries, _ := policy[key].(map[string]any)
				entry, _ := entries[name].(map[string]any)
				if entry["enabled"] != false {
					t.Errorf("%s %s not disabled: %v", key, name, policy[key])
				}
			}
			if !readConfig {
				t.Error("tools-off turn did not read the integrations it must disable")
			}
		},
	},
	{
		provider:  "cursor",
		model:     "persona-cursor-model",
		install:   installNamedOwnWorkShim("cursor-agent"),
		persona:   func(cfg, _ *config.Config) { cfg.CursorModel = "persona-cursor-model" },
		continued: resumedOwnWorkSession,
		fresh:     noOwnWorkResume("--resume", "--continue"),
		policy: func(t *testing.T, call ownWorkCall, tools bool, cfg *config.Config) {
			wantOwnWorkFlag(t, call, "--model", "persona-cursor-model")
			wantOwnWorkFlag(t, call, "--output-format", "json")
			wantOwnWorkSwitch(t, call, "--trust", true)
			for _, flag := range []string{"--force", "--yolo", "--sandbox", "--approve-mcps", "--workspace", "--add-dir"} {
				wantOwnWorkSwitch(t, call, flag, false)
			}
			if tools {
				wantOwnWorkSwitch(t, call, "--mode", false)
			} else {
				wantOwnWorkFlag(t, call, "--mode", "ask")
			}
		},
	},
	{
		provider:  "grok",
		model:     "persona-grok-model",
		install:   installNamedOwnWorkShim("grok"),
		persona:   func(cfg, _ *config.Config) { cfg.GrokModel = "persona-grok-model" },
		continued: resumedOwnWorkSession,
		fresh:     noOwnWorkResume("--resume", "--continue", "-s", "--fork-session", "--restore-code"),
		policy: func(t *testing.T, call ownWorkCall, tools bool, cfg *config.Config) {
			wantOwnWorkFlag(t, call, "--model", "persona-grok-model")
			wantOwnWorkFlag(t, call, "--output-format", "json")
			if got, _ := ownWorkFlag(call.Args, "--cwd"); !sameOwnWorkDir(got, cfg.WorkDir) {
				t.Errorf("grok --cwd = %q, want %q", got, cfg.WorkDir)
			}
			for _, flag := range []string{"--always-approve", "--sandbox", "--deny", "--disallowed-tools", "-w"} {
				wantOwnWorkSwitch(t, call, flag, false)
			}
			if tools {
				wantOwnWorkFlag(t, call, "--permission-mode", "bypassPermissions")
				wantOwnWorkSwitch(t, call, "--tools", false)
			} else {
				wantOwnWorkFlag(t, call, "--tools", "")
				wantOwnWorkSwitch(t, call, "--permission-mode", false)
			}
		},
	},
	{
		provider:  "muse-spark",
		model:     "persona-muse-model",
		install:   installNamedOwnWorkShim("muse"),
		persona:   func(cfg, _ *config.Config) { cfg.MuseSparkModel = "persona-muse-model" },
		continued: replayedOwnWorkTurn,
		fresh:     noOwnWorkResume("--session-id"),
		policy: func(t *testing.T, call ownWorkCall, tools bool, cfg *config.Config) {
			if len(call.Args) < 2 || call.Args[0] != "exec" || call.Args[1] != "--json" {
				t.Errorf("muse argv = %q", call.Args)
			}
			wantOwnWorkFlag(t, call, "--model", "persona-muse-model")
			wantOwnWorkFlag(t, call, "--provider", "meta")
			wantOwnWorkFlag(t, call, "--approval-mode", "never")
			wantOwnWorkFlag(t, call, "--worktree", "off")
			if got, _ := ownWorkFlag(call.Args, "--workspace"); !sameOwnWorkDir(got, cfg.WorkDir) {
				t.Errorf("muse --workspace = %q, want %q", got, cfg.WorkDir)
			}
			for _, flag := range []string{"--no-session-log", "--no-foreign-personal-context", "--disable-request-user-input", "--disable-reminders"} {
				wantOwnWorkSwitch(t, call, flag, true)
			}
			for _, flag := range []string{"--disable-sandbox", "--trust-workspace", "--permission-profile", "--sandbox-network", "--allow-workspace-switch"} {
				wantOwnWorkSwitch(t, call, flag, false)
			}
			off := []string{"--disable-write", "--disable-shell", "--disable-web-tools"}
			if tools {
				wantOwnWorkSwitch(t, call, "--preset", false)
				for _, flag := range off {
					wantOwnWorkSwitch(t, call, flag, false)
				}
			} else {
				wantOwnWorkFlag(t, call, "--preset", "empty")
				for _, flag := range off {
					wantOwnWorkSwitch(t, call, flag, true)
				}
			}
		},
	},
	{
		provider: "obey",
		model:    "persona-obey-model",
		install:  installOwnWorkObey,
		persona: func(cfg, base *config.Config) {
			cfg.ObeyProvider, cfg.ObeyModel, cfg.ObeyCamp = base.ObeyProvider, "persona-obey-model", base.ObeyCamp
			cfg.ObeySocket, cfg.ObeyToolPolicy = base.ObeySocket, "daemon"
		},
		continued: replayedOwnWorkTurn,
		fresh: func(t *testing.T, call ownWorkCall) {
			t.Helper()
			if len(call.Args) == 0 || call.Args[0] != "run" {
				t.Errorf("own worker did not start a run of its own: %q", call.Args)
			}
			for _, flag := range []string{"--session", "--festival", "--task"} {
				wantOwnWorkSwitch(t, call, flag, false)
			}
		},
		policy: func(t *testing.T, call ownWorkCall, tools bool, cfg *config.Config) {
			wantOwnWorkFlag(t, call, "--provider", cfg.ObeyProvider)
			wantOwnWorkFlag(t, call, "--model", "persona-obey-model")
			wantOwnWorkFlag(t, call, "--campaign", cfg.ObeyCamp)
			wantOwnWorkFlag(t, call, "--input", "-")
			if got, _ := ownWorkFlag(call.Args, "--workdir"); !sameOwnWorkDir(got, cfg.WorkDir) {
				t.Errorf("obey --workdir = %q, want %q", got, cfg.WorkDir)
			}
			if call.Socket != cfg.ObeySocket {
				t.Errorf("obey socket = %q, want %q", call.Socket, cfg.ObeySocket)
			}
			if tools {
				wantOwnWorkFlag(t, call, "--mode", "supervised")
			} else {
				wantOwnWorkFlag(t, call, "--mode", "discussion")
			}
		},
	},
}

func sameOwnWorkDir(got any, want string) bool {
	s, _ := got.(string)
	if s == "" {
		return false
	}
	a, errA := filepath.EvalSymlinks(s)
	b, errB := filepath.EvalSymlinks(want)
	return errA == nil && errB == nil && a == b
}

func ownWorkBinary(p Provider) string {
	switch b := p.(type) {
	case *Brain:
		if c, ok := b.client.(*claude.ClaudeClient); ok {
			return c.BinPath
		}
	case *CodexBrain:
		if c, ok := b.client.(codexClient); ok {
			return c.binary
		}
	case *CursorBrain:
		if c, ok := b.client.(*cursor.Client); ok {
			return c.BinPath
		}
	case *GrokBrain:
		if c, ok := b.client.(*grok.GrokClient); ok {
			return c.BinPath
		}
	case *MuseSparkBrain:
		if c, ok := b.client.(*muse.Client); ok {
			return c.BinPath
		}
	}
	return ""
}

func ownWorkPersona(t *testing.T, b ownWorkBackend, base *config.Config, tools, tiered bool) *config.Config {
	t.Helper()
	cfg := &config.Config{
		BrainProvider: b.provider, PersonaWork: config.WorkOwn, AgentName: "Ada", MaxHistory: 10,
		WorkDir: t.TempDir(), WorkDirSource: "fixture", PromptsDir: t.TempDir(),
		VoiceToolsEnabled: tools, VoiceWorkerBoundary: true,
		VoiceWorkerProvider: "grok", VoiceWorkerModel: "configured-worker-model",
	}
	b.persona(cfg, base)
	if tiered {
		cfg.VoiceWorkers = config.DefaultVoiceWorkers()
		cfg.VoiceWorkers.Mode = config.WorkerModeTiered
	}
	return cfg
}

// ownWorkJob builds and runs the job the way the pipeline does: the persona's
// own config, a worker built from it, a fresh instance per job, and the
// worker tool surface the job's config allows.
func ownWorkJob(t *testing.T, cfg *config.Config, wantTools bool) (Provider, func() (string, error)) {
	t.Helper()
	own := config.OwnWorkConfig(cfg)
	if own.VoiceToolsEnabled != wantTools {
		t.Fatalf("OwnWorkConfig voice_tools_enabled = %v, want %v (input %v, tiers %+v)", own.VoiceToolsEnabled, wantTools, cfg.VoiceToolsEnabled, cfg.VoiceWorkers)
	}
	if own.VoiceWorkers != nil && own.VoiceWorkers.Mode != config.WorkerModeLegacy {
		t.Fatalf("own job left in %q worker mode", own.VoiceWorkers.Mode)
	}
	tools := own.VoiceToolsEnabled || own.VoiceWorkers != nil && own.VoiceWorkers.Mode == config.WorkerModeTiered
	worker, err := NewWorkerProvider(own)
	if err != nil {
		t.Fatalf("build own worker: %v", err)
	}
	if f, ok := worker.(FreshProvider); ok {
		worker = f.Fresh()
	}
	return worker, func() (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		return worker.ThinkFull(ctx, ownWorkBrief, WorkerStreamOptions(tools))
	}
}

func TestOwnWorkBuildsAFreshWorkerOnEachHostedBackend(t *testing.T) {
	modes := []struct {
		name          string
		tools, tiered bool
	}{
		{"tools_on", true, false},
		{"tools_off", false, false},
		{"tools_off_tiered", false, true},
	}
	for _, b := range ownWorkBackends {
		t.Run(b.provider, func(t *testing.T) {
			for _, mode := range modes {
				t.Run(mode.name, func(t *testing.T) {
					fx, base := b.install(t)
					cfg := ownWorkPersona(t, b, base, mode.tools, mode.tiered)

					live, err := NewProvider(cfg)
					if err != nil {
						t.Fatalf("build live brain: %v", err)
					}
					if fx.binary != "" {
						if got := ownWorkBinary(live); got != fx.binary {
							t.Fatalf("live brain resolved %q, not the fixture %q", got, fx.binary)
						}
					}
					liveTurn := func(input string) {
						ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
						defer cancel()
						if _, err := live.ThinkFull(ctx, input, StreamOptions{ToolsEnabled: cfg.VoiceToolsEnabled}); err != nil {
							t.Fatalf("live turn %q: %v", input, err)
						}
					}
					first := fx.run(t, func() { liveTurn(ownWorkLiveFirst) })
					second := fx.run(t, func() { liveTurn(ownWorkLiveSecond) })
					if !b.continued(first, second) {
						t.Fatalf("the fake cannot see the live conversation carried forward: first=%q second=%q", first.Args, second.text())
					}
					historyBefore := len(live.History())
					var sessionBefore SessionState
					if r, ok := live.(SessionReporter); ok {
						sessionBefore = r.SessionInfo()
					}

					worker, run := ownWorkJob(t, cfg, mode.tools)
					if fx.binary != "" {
						if got := ownWorkBinary(worker); got != fx.binary {
							t.Fatalf("own worker resolved %q, not the fixture %q", got, fx.binary)
						}
					}
					var reply string
					job := fx.run(t, func() {
						if reply, err = run(); err != nil {
							t.Fatalf("own job: %v", err)
						}
					})
					if reply == "" {
						t.Fatal("own job returned no reply")
					}

					if !sameOwnWorkDir(job.Cwd, cfg.WorkDir) {
						t.Errorf("own job ran in %q, want the persona directory %q", job.Cwd, cfg.WorkDir)
					}
					text := job.text()
					if !strings.Contains(text, ownWorkBrief) {
						t.Fatalf("captured start is not the job: %q", job.Args)
					}
					for _, marker := range []string{ownWorkLiveFirst, ownWorkLiveSecond} {
						if strings.Contains(text, marker) {
							t.Errorf("own job was given the live conversation (%s)", marker)
						}
					}
					for _, id := range []string{first.Session, second.Session} {
						if id != "" && strings.Contains(text, id) {
							t.Errorf("own job names the live session %s", id)
						}
					}
					if b.continued(second, job) {
						t.Error("own job continued the live conversation")
					}
					b.fresh(t, job)
					b.policy(t, job, mode.tools, cfg)

					if got := len(live.History()); got != historyBefore {
						t.Errorf("own job changed the live history: %d turns, was %d", got, historyBefore)
					}
					if r, ok := live.(SessionReporter); ok {
						if after := r.SessionInfo(); after.ID != sessionBefore.ID || after.Turns != sessionBefore.Turns {
							t.Errorf("own job touched the live session: %+v, was %+v", after, sessionBefore)
						}
					}
				})
			}
		})
	}
}

// Under obey_tool_policy strict, the default, Obey cannot hold a turn to no
// tools, so an own job with voice_tools_enabled off is refused before any run.
func TestOwnWorkOnObeyRefusesToolsOffUnderTheStrictPolicy(t *testing.T) {
	for _, tiered := range []bool{false, true} {
		t.Run(fmt.Sprintf("tiered=%v", tiered), func(t *testing.T) {
			var obey ownWorkBackend
			for _, b := range ownWorkBackends {
				if b.provider == "obey" {
					obey = b
				}
			}
			fx, base := obey.install(t)
			cfg := ownWorkPersona(t, obey, base, false, tiered)
			cfg.ObeyToolPolicy = "strict"
			_, run := ownWorkJob(t, cfg, false)
			_, err := run()
			if err == nil || !strings.Contains(err.Error(), "cannot enforce tools off") {
				t.Fatalf("own job error = %v, want the tools-off refusal", err)
			}
			if _, statErr := os.Stat(filepath.Join(fx.dir, "request.json")); !os.IsNotExist(statErr) {
				t.Fatalf("a run started despite the refusal: %v", statErr)
			}
		})
	}
}
