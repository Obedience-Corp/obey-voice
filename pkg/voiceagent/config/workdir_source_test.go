package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorkDirProvenance pins which layer gets the blame for the working
// directory. The silent cases come first: an environment variable and the two
// fallbacks are the ones nobody typed, and until they were reported a hijacked
// launch was indistinguishable from a normal one (WI-742a80).
func TestWorkDirProvenance(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, env workDirEnv) (cfg *Config, override string, want WorkDirInfo)
	}{
		{
			name: "a bare WORK_DIR decides the launch and is named",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				dir := t.TempDir()
				t.Setenv("WORK_DIR", dir)
				return loadForTest(t), "", WorkDirInfo{Dir: dir, Source: WorkDirSourceEnv, EnvVar: "WORK_DIR"}
			},
		},
		{
			name: "OBEY_VOICE_WORK_DIR beats the bare name",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				prefixed, bare := t.TempDir(), t.TempDir()
				t.Setenv("OBEY_VOICE_WORK_DIR", prefixed)
				t.Setenv("WORK_DIR", bare)
				return loadForTest(t), "", WorkDirInfo{Dir: prefixed, Source: WorkDirSourceEnv, EnvVar: "OBEY_VOICE_WORK_DIR"}
			},
		},
		{
			name: "the documented override alone",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				dir := t.TempDir()
				t.Setenv("OBEY_VOICE_WORK_DIR", dir)
				return loadForTest(t), "", WorkDirInfo{Dir: dir, Source: WorkDirSourceEnv, EnvVar: "OBEY_VOICE_WORK_DIR"}
			},
		},
		{
			name: "an empty variable decides nothing, the way viper reads it",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				dir := t.TempDir()
				t.Setenv("WORK_DIR", "")
				t.Chdir(dir)
				return loadForTest(t), "", WorkDirInfo{Dir: dir, Source: WorkDirSourceLaunchDir}
			},
		},
		{
			name: "a configured directory that is gone falls back to home",
			setup: func(t *testing.T, env workDirEnv) (*Config, string, WorkDirInfo) {
				return &Config{WorkDir: filepath.Join(t.TempDir(), "gone")}, "", WorkDirInfo{Dir: env.home, Source: WorkDirSourceHomeFallback}
			},
		},
		{
			name: "no usable home falls back to the temp directory",
			setup: func(t *testing.T, env workDirEnv) (*Config, string, WorkDirInfo) {
				orig := homeDir
				homeDir = func() string { return "." }
				t.Cleanup(func() { homeDir = orig })
				// The cwd is the binary's own folder, exactly as a spawned serve
				// sees it, so the launch directory is rejected too.
				t.Chdir(env.exeDir)
				tmp, err := filepath.Abs(os.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				return nil, "", WorkDirInfo{Dir: tmp, Source: WorkDirSourceTempFallback}
			},
		},
		{
			name: "an explicit setting outranks the environment",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				dir := t.TempDir()
				t.Setenv("WORK_DIR", t.TempDir())
				return &Config{WorkDir: dir}, "", WorkDirInfo{Dir: dir, Source: WorkDirSourceConfig}
			},
		},
		{
			name: "the config file decides",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				dir := t.TempDir()
				if err := os.WriteFile(ConfigFile(), []byte("work_dir: "+dir+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return loadForTest(t), "", WorkDirInfo{Dir: dir, Source: WorkDirSourceConfig}
			},
		},
		{
			name: "nothing configured is the launch directory",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				dir := t.TempDir()
				t.Chdir(dir)
				return nil, "", WorkDirInfo{Dir: dir, Source: WorkDirSourceLaunchDir}
			},
		},
		{
			name: "the flag is its own source",
			setup: func(t *testing.T, _ workDirEnv) (*Config, string, WorkDirInfo) {
				dir, other := t.TempDir(), t.TempDir()
				t.Setenv("WORK_DIR", other)
				return loadForTest(t), dir, WorkDirInfo{Dir: dir, Source: WorkDirSourceFlag}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newWorkDirEnv(t)
			cfg, override, want := tt.setup(t, env)

			got, err := ResolveWorkDirInfo(cfg, override)
			if err != nil {
				t.Fatalf("ResolveWorkDirInfo error = %v", err)
			}
			if got != want {
				t.Errorf("ResolveWorkDirInfo() = %+v, want %+v", got, want)
			}
			assertUsableWorkDir(t, got.Dir)
			// The plain resolver is the projection of the same call: if these
			// ever disagree, provenance is describing a directory the agent is
			// not using.
			if plain := WorkDirFrom(cfg); override == "" && plain != got.Dir {
				t.Errorf("WorkDirFrom() = %q, but provenance reports %q", plain, got.Dir)
			}
		})
	}
}

// A rejected override has no provenance to report, and must not leak a
// directory alongside the error.
func TestResolveWorkDirInfoRejectsABadOverride(t *testing.T) {
	newWorkDirEnv(t)

	got, err := ResolveWorkDirInfo(nil, filepath.Join(t.TempDir(), "projct"))
	if err == nil {
		t.Fatalf("ResolveWorkDirInfo = %+v, want an error", got)
	}
	if got != (WorkDirInfo{}) {
		t.Errorf("ResolveWorkDirInfo returned %+v alongside an error", got)
	}
}

// Describe is what the banner, paths and doctor print, so the variable that
// decided a launch has to appear in it by name.
func TestWorkDirInfoDescribe(t *testing.T) {
	tests := []struct {
		name string
		info WorkDirInfo
		want string
	}{
		{"env names the variable", WorkDirInfo{Source: WorkDirSourceEnv, EnvVar: "WORK_DIR"}, "the WORK_DIR environment variable"},
		{"env without a name stays honest", WorkDirInfo{Source: WorkDirSourceEnv}, "an environment variable"},
		{"flag", WorkDirInfo{Source: WorkDirSourceFlag}, "the --work-dir flag"},
		{"config", WorkDirInfo{Source: WorkDirSourceConfig}, "the work_dir setting"},
		{"launch dir", WorkDirInfo{Source: WorkDirSourceLaunchDir}, "the directory Obey Voice was launched from"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.Describe(); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The environment binding is only half the fix: `config get work_dir` and
// `config unset` read the same table, and a value the environment decided under
// the compatibility name must not be reported as coming from the file.
func TestWorkDirEnvNamesAndSource(t *testing.T) {
	if got, want := envNamesFor("work_dir"), []string{"OBEY_VOICE_WORK_DIR", "WORK_DIR"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("envNamesFor(work_dir) = %v, want %v (documented name first)", got, want)
	}
	if got := envNamesFor("tts_provider"); len(got) != 1 || got[0] != "TTS_PROVIDER" {
		t.Errorf("envNamesFor(tts_provider) = %v, want the single documented binding", got)
	}

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, SourceDefault},
		{"the compatibility name still counts as the environment", map[string]string{"WORK_DIR": "/w"}, SourceEnv},
		{"the documented name", map[string]string{"OBEY_VOICE_WORK_DIR": "/w"}, SourceEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newWorkDirEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := Source("work_dir"); got != tt.want {
				t.Errorf("Source(work_dir) = %q, want %q", got, tt.want)
			}
		})
	}
}

// loadForTest reads config the way the CLI does, so the env bindings under test
// are the ones the loader registers rather than a second copy.
func loadForTest(t *testing.T) *Config {
	t.Helper()
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	return cfg
}
