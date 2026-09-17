package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// resetViper swaps in a fresh viper so earlier tests' Set overrides don't leak.
func resetViper(t *testing.T) {
	t.Helper()
	orig := v
	v = viper.New()
	setDefaults(v)
	t.Cleanup(func() { v = orig })
}

func TestLoadDefaults(t *testing.T) {
	// Point to a non-existent config file so defaults are used
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	// Re-init viper with fresh defaults
	setDefaults(v)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TTSProvider != "kokoro" {
		t.Errorf("TTSProvider = %q, want kokoro", cfg.TTSProvider)
	}
	if cfg.TTSFallbackProvider != "kokoro" {
		t.Errorf("TTSFallbackProvider = %q, want kokoro", cfg.TTSFallbackProvider)
	}
	if cfg.TTSVoice != "af_heart" {
		t.Errorf("TTSVoice = %q, want af_heart", cfg.TTSVoice)
	}
	if cfg.STTProvider != "sherpa-parakeet" {
		t.Errorf("STTProvider = %q, want sherpa-parakeet", cfg.STTProvider)
	}
	if cfg.SherpaStreamingModel != "en-2023-06-26" {
		t.Errorf("SherpaStreamingModel = %q, want en-2023-06-26", cfg.SherpaStreamingModel)
	}
	if cfg.WhisperModel != "small" {
		t.Errorf("WhisperModel = %q, want small", cfg.WhisperModel)
	}
	if cfg.WhisperCPPBinary != "whisper-cli" {
		t.Errorf("WhisperCPPBinary = %q, want whisper-cli", cfg.WhisperCPPBinary)
	}
	if cfg.WhisperCPPModel != "base.en" {
		t.Errorf("WhisperCPPModel = %q, want base.en", cfg.WhisperCPPModel)
	}
	if cfg.MaxHistory != 10 {
		t.Errorf("MaxHistory = %d, want 10", cfg.MaxHistory)
	}
	if cfg.ClaudeMaxSessionTokens != 0 {
		t.Errorf("ClaudeMaxSessionTokens = %d, want 0 (fuse is opt-in)", cfg.ClaudeMaxSessionTokens)
	}
	if cfg.ClaudeSessionWarnTokens != 60000 {
		t.Errorf("ClaudeSessionWarnTokens = %d, want 60000", cfg.ClaudeSessionWarnTokens)
	}
	if !cfg.VADEnabled {
		t.Error("VADEnabled = false, want true")
	}
	if !cfg.VoiceToolsEnabled {
		t.Error("VoiceToolsEnabled = false, want true (local tools default on)")
	}
	if !cfg.SkillsEnabled {
		t.Error("SkillsEnabled = false, want true (agent skills default on)")
	}
	if !cfg.Speaker.Enabled {
		t.Error("Speaker.Enabled = false, want true")
	}
	if !cfg.Speaker.Live.Enabled {
		t.Error("Speaker.Live.Enabled = false, want true (speakers in chat default on)")
	}
	if !cfg.Speaker.Meeting.Enabled {
		t.Error("Speaker.Meeting.Enabled = false, want true (meeting diarization default on)")
	}
	if cfg.BargeInEnabled {
		t.Error("BargeInEnabled = true, want false")
	}
	if cfg.VADThreshold != 0.6 {
		t.Errorf("VADThreshold = %v, want 0.6", cfg.VADThreshold)
	}
	if cfg.VADMinSpeechDuration != 0.25 {
		t.Errorf("VADMinSpeechDuration = %v, want 0.25", cfg.VADMinSpeechDuration)
	}
	if cfg.VoiceFrontendEnabled {
		t.Error("VoiceFrontendEnabled = true, want false (frontend over-suppresses normal speech)")
	}
	if got := VoiceFrontendOf(cfg); got != VoiceFrontendOff {
		t.Errorf("VoiceFrontendOf = %q, want %q", got, VoiceFrontendOff)
	}
	if got := VoiceFrontendNSLevelOf(cfg); got != DefaultVoiceFrontendNSLevel {
		t.Errorf("VoiceFrontendNSLevelOf = %d, want %d", got, DefaultVoiceFrontendNSLevel)
	}
	if got := BargeInVADThresholdOf(cfg); got != DefaultBargeInVADThreshold {
		t.Errorf("BargeInVADThresholdOf = %v, want %v", got, DefaultBargeInVADThreshold)
	}
	if got := BargeInTailGuardMSOf(cfg); got != DefaultBargeInTailGuardMS {
		t.Errorf("BargeInTailGuardMSOf = %d, want %d", got, DefaultBargeInTailGuardMS)
	}
}

func TestBargeInEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BARGE_IN_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.BargeInEnabled {
		t.Error("BargeInEnabled = false, want true from env")
	}
}

func TestBargeInVADThresholdEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BARGE_IN_VAD_THRESHOLD", "0.65")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := BargeInVADThresholdOf(cfg); got != 0.65 {
		t.Errorf("BargeInVADThresholdOf = %v, want 0.65 from env", got)
	}
}

func TestBargeInTailGuardMSEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BARGE_IN_TAIL_GUARD_MS", "350")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := BargeInTailGuardMSOf(cfg); got != 350 {
		t.Errorf("BargeInTailGuardMSOf = %d, want 350 from env", got)
	}
}

// TestBargeInVADThresholdOfClampsOutOfRange documents the D8-style posture
// (shared with VoiceFrontendNSLevelOf): a value outside 0.3-0.95 — including
// the zero value of an unloaded Config — falls back to the default rather
// than failing the whole selection.
func TestBargeInVADThresholdOfClampsOutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  float64
	}{
		{"unset (zero value)", 0, DefaultBargeInVADThreshold},
		{"below min", 0.1, DefaultBargeInVADThreshold},
		{"above max", 0.99, DefaultBargeInVADThreshold},
		{"at min", MinBargeInVADThreshold, MinBargeInVADThreshold},
		{"at max", MaxBargeInVADThreshold, MaxBargeInVADThreshold},
		{"lowered for webrtc", 0.6, 0.6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{BargeInVADThreshold: tt.value}
			if got := BargeInVADThresholdOf(cfg); got != tt.want {
				t.Errorf("BargeInVADThresholdOf(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
	if got := BargeInVADThresholdOf(nil); got != DefaultBargeInVADThreshold {
		t.Errorf("BargeInVADThresholdOf(nil) = %v, want %v", got, DefaultBargeInVADThreshold)
	}
	webrtc := &Config{VoiceFrontend: VoiceFrontendWebRTC}
	if got := BargeInVADThresholdOf(webrtc); got != WebRTCBargeInVADThreshold {
		t.Errorf("BargeInVADThresholdOf(webrtc unset) = %v, want %v", got, WebRTCBargeInVADThreshold)
	}
}

func TestWebRTCBargeInVADThresholdDefaultsUnlessExplicit(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("voice_frontend: webrtc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	t.Cleanup(func() { configFile = orig })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got := BargeInVADThresholdOf(cfg); got != WebRTCBargeInVADThreshold {
		t.Errorf("webrtc unset barge-in threshold = %v, want %v", got, WebRTCBargeInVADThreshold)
	}

	resetViper(t)
	if err := os.WriteFile(path, []byte("voice_frontend: webrtc\nbarge_in_vad_threshold: 0.85\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() explicit error: %v", err)
	}
	if got := BargeInVADThresholdOf(cfg); got != DefaultBargeInVADThreshold {
		t.Errorf("explicit webrtc barge-in threshold = %v, want %v", got, DefaultBargeInVADThreshold)
	}
}

// TestBargeInTailGuardMSOfClampsOutOfRange mirrors the threshold's posture
// for the 200-800ms tail-guard window.
func TestBargeInTailGuardMSOfClampsOutOfRange(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{"unset (zero value)", 0, DefaultBargeInTailGuardMS},
		{"below min", 50, DefaultBargeInTailGuardMS},
		{"above max", 5000, DefaultBargeInTailGuardMS},
		{"at min", MinBargeInTailGuardMS, MinBargeInTailGuardMS},
		{"at max", MaxBargeInTailGuardMS, MaxBargeInTailGuardMS},
		{"mid-range", 500, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{BargeInTailGuardMS: tt.value}
			if got := BargeInTailGuardMSOf(cfg); got != tt.want {
				t.Errorf("BargeInTailGuardMSOf(%d) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
	if got := BargeInTailGuardMSOf(nil); got != DefaultBargeInTailGuardMS {
		t.Errorf("BargeInTailGuardMSOf(nil) = %d, want %d", got, DefaultBargeInTailGuardMS)
	}
	if got := BargeInTailGuardOf(&Config{BargeInTailGuardMS: 500}); got != 500*time.Millisecond {
		t.Errorf("BargeInTailGuardOf = %v, want 500ms", got)
	}
}

// Mouse scrolling is on by default, but claiming the mouse costs unmodified
// click-drag selection — so turning it back off has to actually stick.
func TestTUIMouseDefaultsOnAndCanBeDisabled(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// Off by default: the terminal keeps click-drag selection so transcript
	// text can be copied the way it is in any other agent chat.
	if cfg.TUIMouseEnabled {
		t.Error("TUIMouseEnabled = true, want false by default")
	}

	t.Setenv("TUI_MOUSE_ENABLED", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.TUIMouseEnabled {
		t.Error("TUIMouseEnabled = false, want true from env")
	}
}

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `tts_provider: edge
tts_voice: en-US-AriaNeural
speech_speed: 1.1
max_history: 20
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := configFile
	configFile = path
	defer func() { configFile = orig }()

	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TTSProvider != "edge" {
		t.Errorf("TTSProvider = %q, want edge", cfg.TTSProvider)
	}
	if cfg.TTSVoice != "en-US-AriaNeural" {
		t.Errorf("TTSVoice = %q, want en-US-AriaNeural", cfg.TTSVoice)
	}
	if cfg.SpeechSpeed != 1.1 {
		t.Errorf("SpeechSpeed = %f, want 1.1", cfg.SpeechSpeed)
	}
	if cfg.MaxHistory != 20 {
		t.Errorf("MaxHistory = %d, want 20", cfg.MaxHistory)
	}
	// Defaults still applied for unset keys
	if cfg.STTProvider != "sherpa-parakeet" {
		t.Errorf("STTProvider = %q, want sherpa-parakeet (default)", cfg.STTProvider)
	}
}

func TestEnvOverride(t *testing.T) {
	dir := t.TempDir()
	orig := configFile
	configFile = filepath.Join(dir, "config.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BRAIN_PROVIDER", "ollama")
	t.Setenv("OLLAMA_MODEL", "test-model-xyz")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.BrainProvider != "ollama" {
		t.Errorf("BrainProvider = %q, want ollama (from env)", cfg.BrainProvider)
	}
	if cfg.OllamaModel != "test-model-xyz" {
		t.Errorf("OllamaModel = %q, want test-model-xyz (from env)", cfg.OllamaModel)
	}
}

func TestGrokModelEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BRAIN_PROVIDER", "grok")
	t.Setenv("GROK_MODEL", "grok-test-xyz")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.BrainProvider != "grok" {
		t.Errorf("BrainProvider = %q, want grok (from env)", cfg.BrainProvider)
	}
	if cfg.GrokModel != "grok-test-xyz" {
		t.Errorf("GrokModel = %q, want grok-test-xyz (from env)", cfg.GrokModel)
	}
}

func TestCursorModelEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("BRAIN_PROVIDER", "cursor")
	t.Setenv("CURSOR_MODEL", "composer-2.5")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.BrainProvider != "cursor" {
		t.Errorf("BrainProvider = %q, want cursor (from env)", cfg.BrainProvider)
	}
	if cfg.CursorModel != "composer-2.5" {
		t.Errorf("CursorModel = %q, want composer-2.5 (from env)", cfg.CursorModel)
	}
}

func TestVoiceToolsEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("VOICE_TOOLS_ENABLED", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if !cfg.VoiceToolsEnabled {
		t.Error("VoiceToolsEnabled = false, want true from env")
	}
}

func TestOllamaDefaultsVoiceToolsOnWhenUnset(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// brain_provider only — no voice_tools_enabled key.
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nollama_model: llama3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("VOICE_TOOLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.VoiceToolsEnabled {
		t.Fatal("ollama without explicit voice_tools_enabled must default tools on")
	}
}

func TestOllamaRespectsExplicitVoiceToolsFalse(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nvoice_tools_enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("VOICE_TOOLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.VoiceToolsEnabled {
		t.Fatal("explicit voice_tools_enabled: false must win")
	}
}

func TestOllamaDefaultsSkillsOnWhenUnset(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nollama_model: llama3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("SKILLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.SkillsEnabled {
		t.Fatal("ollama without explicit skills_enabled must default skills on")
	}
}

func TestOllamaRespectsExplicitSkillsFalse(t *testing.T) {
	resetViper(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: ollama\nskills_enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()
	t.Setenv("SKILLS_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.SkillsEnabled {
		t.Fatal("explicit skills_enabled: false must win")
	}
}

func TestToolCommandTimeoutEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("TOOL_COMMAND_TIMEOUT", "90")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ToolCommandTimeout != 90 {
		t.Fatalf("ToolCommandTimeout = %d, want 90 from env", cfg.ToolCommandTimeout)
	}
}

func TestClaudeSessionWarnTokensEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("CLAUDE_SESSION_WARN_TOKENS", "120000")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ClaudeSessionWarnTokens != 120000 {
		t.Fatalf("ClaudeSessionWarnTokens = %d, want 120000 from env", cfg.ClaudeSessionWarnTokens)
	}
}

func TestClampToolCommandTimeout(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 30},
		{-1, 30},
		{1, 1},
		{30, 30},
		{120, 120},
		{121, 120},
		{86400, 120},
	}
	for _, tt := range tests {
		if got := ClampToolCommandTimeout(tt.in); got != tt.want {
			t.Fatalf("ClampToolCommandTimeout(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// A batch wider than the context window puts llama.cpp's end-of-prompt
// checkpoint at or before position zero, so nothing is ever reused — which is
// precisely the state ollama_num_batch exists to get out of. The schema bounds
// the key, but a hand-edited config.yaml and OLLAMA_NUM_BATCH both skip the
// schema, so the guard has to be at load.
func TestClampOllamaNumBatch(t *testing.T) {
	tests := []struct {
		name             string
		batch, ctx, want int
	}{
		{"default under window", 128, 8192, 128},
		{"equal to window is fine", 8192, 8192, 8192},
		{"wider than window clamps", 16384, 8192, 8192},
		{"wider than a small window clamps", 1024, 512, 512},
		// 0 is the documented escape hatch: let Ollama choose. It must survive
		// the guard, or setting it would silently become a real batch size.
		{"zero stays zero", 0, 8192, 0},
		{"negative normalises to zero", -5, 8192, 0},
		// num_ctx 0 means the server sizes the window, so there is nothing to
		// clamp against and the batch is left as written.
		{"unknown window leaves batch alone", 4096, 0, 4096},
		{"zero batch and zero window", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampOllamaNumBatch(tt.batch, tt.ctx); got != tt.want {
				t.Fatalf("ClampOllamaNumBatch(%d, %d) = %d, want %d", tt.batch, tt.ctx, got, tt.want)
			}
		})
	}
}

// The clamp is only useful if Load actually applies it — the schema is not on
// the path a hand-edited config.yaml takes.
func TestLoadClampsOllamaNumBatchToWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("ollama_num_ctx: 2048\nollama_num_batch: 8192\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	orig := configFile
	configFile = path
	defer func() { configFile = orig }()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OllamaNumBatch != 2048 {
		t.Fatalf("OllamaNumBatch = %d, want 2048 (clamped to ollama_num_ctx)", cfg.OllamaNumBatch)
	}
}

func TestApplyOllamaDefaultsAfterProviderSwitch(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()
	setDefaults(v)
	// No explicit tools/skills keys in config file.
	cfg := &Config{BrainProvider: "claude"}
	ApplyOllamaDefaults(cfg)
	if cfg.VoiceToolsEnabled || cfg.SkillsEnabled {
		t.Fatal("claude provider must not auto-enable tools/skills")
	}
	cfg.BrainProvider = "ollama"
	ApplyOllamaDefaults(cfg)
	if !cfg.VoiceToolsEnabled || !cfg.SkillsEnabled {
		t.Fatalf("ollama unset keys should auto-enable tools/skills, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}
}

func TestSetAndSaveBrainProviderPersistsOllamaDefaults(t *testing.T) {
	t.Setenv("VOICE_TOOLS_ENABLED", "")
	t.Setenv("SKILLS_ENABLED", "")

	dir := t.TempDir()
	origDir, origFile := configDir, configFile
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	defer func() {
		configDir, configFile = origDir, origFile
	}()
	resetViper(t)

	cfg := &Config{BrainProvider: "claude"}
	if err := SetAndSaveBrainProvider(cfg, "ollama"); err != nil {
		t.Fatalf("SetAndSaveBrainProvider() error: %v", err)
	}
	if !cfg.VoiceToolsEnabled || !cfg.SkillsEnabled {
		t.Fatalf("live config should enable Ollama capabilities, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}

	// Recreate Viper to model the next process startup rather than relying on
	// the values still held by the current process.
	v = viper.New()
	setDefaults(v)
	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load() after provider switch: %v", err)
	}
	if !reloaded.VoiceToolsEnabled || !reloaded.SkillsEnabled {
		t.Fatalf("reloaded config should keep Ollama capabilities enabled, got tools=%v skills=%v",
			reloaded.VoiceToolsEnabled, reloaded.SkillsEnabled)
	}
}

func TestSetAndSaveBrainProviderEnablesOllamaToolsDespiteDumpedFalse(t *testing.T) {
	// WriteConfigAs often materializes voice_tools_enabled: false as a real key.
	// Switching to Ollama in Settings must re-enable tools so local models can
	// actually use list/read/write/run_command.
	t.Setenv("VOICE_TOOLS_ENABLED", "")
	t.Setenv("SKILLS_ENABLED", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: claude\nvoice_tools_enabled: false\nskills_enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origDir, origFile := configDir, configFile
	configDir, configFile = dir, path
	defer func() {
		configDir, configFile = origDir, origFile
	}()
	resetViper(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("initial Load(): %v", err)
	}
	if err := SetAndSaveBrainProvider(cfg, "ollama"); err != nil {
		t.Fatalf("SetAndSaveBrainProvider() error: %v", err)
	}
	if !cfg.VoiceToolsEnabled || !cfg.SkillsEnabled {
		t.Fatalf("switch to ollama should enable tools/skills, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}

	v = viper.New()
	setDefaults(v)
	reloaded, err := Load()
	if err != nil {
		t.Fatalf("Load() after switch: %v", err)
	}
	if !reloaded.VoiceToolsEnabled || !reloaded.SkillsEnabled {
		t.Fatalf("persisted ollama switch should keep tools/skills on, got tools=%v skills=%v",
			reloaded.VoiceToolsEnabled, reloaded.SkillsEnabled)
	}
}

func TestSetAndSaveBrainProviderHonorsEnvToolsOff(t *testing.T) {
	t.Setenv("VOICE_TOOLS_ENABLED", "false")
	t.Setenv("SKILLS_ENABLED", "false")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("brain_provider: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origDir, origFile := configDir, configFile
	configDir, configFile = dir, path
	defer func() {
		configDir, configFile = origDir, origFile
	}()
	resetViper(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if err := SetAndSaveBrainProvider(cfg, "ollama"); err != nil {
		t.Fatalf("SetAndSaveBrainProvider() error: %v", err)
	}
	if cfg.VoiceToolsEnabled || cfg.SkillsEnabled {
		t.Fatalf("env=false must keep tools/skills off, got tools=%v skills=%v",
			cfg.VoiceToolsEnabled, cfg.SkillsEnabled)
	}
}

func TestPromptConfigEnvOverrides(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("PERSONA", "work")
	t.Setenv("PROMPTS_DIR", "/tmp/samantha-prompts")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Persona != "work" {
		t.Errorf("Persona = %q, want work from env", cfg.Persona)
	}
	if cfg.PromptsDir != "/tmp/samantha-prompts" {
		t.Errorf("PromptsDir = %q, want /tmp/samantha-prompts from env", cfg.PromptsDir)
	}
	if got := PromptsDir(); got != "/tmp/samantha-prompts" {
		t.Errorf("PromptsDir() = %q, want /tmp/samantha-prompts", got)
	}
}

func TestModelsDirDefault(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	_, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	want := DefaultModelsDir()
	got := ModelsDir()
	if got != want {
		t.Errorf("ModelsDir() = %q, want %q", got, want)
	}
	if !strings.Contains(got, AppSlug) {
		t.Errorf("ModelsDir() = %q, want path containing AppSlug %q", got, AppSlug)
	}
}

func TestSkillsConfigDefaults(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("SKILLS_ENABLED", "")
	t.Setenv("SKILLS_DIR", "")
	t.Setenv("OLLAMA_EMBEDDING_MODEL", "")
	t.Setenv("SKILLS_SIMILARITY_THRESHOLD", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.SkillsEnabled {
		t.Error("SkillsEnabled default = false, want true")
	}
	if cfg.SkillsDir != "" {
		t.Errorf("SkillsDir field = %q, want empty default", cfg.SkillsDir)
	}
	if cfg.OllamaEmbeddingModel != "nomic-embed-text" {
		t.Errorf("OllamaEmbeddingModel = %q, want nomic-embed-text default", cfg.OllamaEmbeddingModel)
	}
	if cfg.SkillsSimilarityThreshold != 0.55 {
		t.Errorf("SkillsSimilarityThreshold = %v, want 0.55 default", cfg.SkillsSimilarityThreshold)
	}
	wantDir := filepath.Join(configDir, "skills")
	if got := SkillsDir(); got != wantDir {
		t.Errorf("SkillsDir() = %q, want %q", got, wantDir)
	}
}

func TestSkillsConfigEnvOverrides(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("SKILLS_ENABLED", "true")
	t.Setenv("SKILLS_DIR", "/tmp/samantha-skills")
	t.Setenv("OLLAMA_EMBEDDING_MODEL", "embeddinggemma")
	t.Setenv("SKILLS_SIMILARITY_THRESHOLD", "0.7")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.SkillsEnabled {
		t.Error("SkillsEnabled = false, want true from env")
	}
	if cfg.SkillsDir != "/tmp/samantha-skills" {
		t.Errorf("SkillsDir = %q, want /tmp/samantha-skills from env", cfg.SkillsDir)
	}
	if cfg.OllamaEmbeddingModel != "embeddinggemma" {
		t.Errorf("OllamaEmbeddingModel = %q, want embeddinggemma from env", cfg.OllamaEmbeddingModel)
	}
	if cfg.SkillsSimilarityThreshold != 0.7 {
		t.Errorf("SkillsSimilarityThreshold = %v, want 0.7 from env", cfg.SkillsSimilarityThreshold)
	}
	if got := SkillsDir(); got != "/tmp/samantha-skills" {
		t.Errorf("SkillsDir() = %q, want /tmp/samantha-skills", got)
	}
}

func TestSkillsEnabledRoundTrip(t *testing.T) {
	dir := t.TempDir()
	origFile, origDir := configFile, configDir
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	defer func() {
		configFile = origFile
		configDir = origDir
	}()

	setDefaults(v)
	Set("skills_enabled", true)
	Set("skills_dir", filepath.Join(dir, "my-skills"))
	if err := Save(); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Reset and reload from disk.
	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save: %v", err)
	}
	if !cfg.SkillsEnabled {
		t.Error("SkillsEnabled did not round-trip true")
	}
	want := filepath.Join(dir, "my-skills")
	if cfg.SkillsDir != want {
		t.Errorf("SkillsDir = %q, want %q after round-trip", cfg.SkillsDir, want)
	}
	if got := SkillsDir(); got != want {
		t.Errorf("SkillsDir() = %q, want %q after round-trip", got, want)
	}
}

func TestModelsDirEnvOverride(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()

	setDefaults(v)
	t.Setenv("MODELS_DIR", "/mnt/fast/models")

	_, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	got := ModelsDir()
	if got != "/mnt/fast/models" {
		t.Errorf("ModelsDir() = %q, want /mnt/fast/models", got)
	}
}

func TestDownloadFileCreatesNestedParentDir(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("model"))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "whispercpp", "ggml-base.en.bin")
	if err := downloadFile(t.Context(), path, server.URL, "ggml-base.en.bin", 0, "", nil); err != nil {
		t.Fatalf("downloadFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "model" {
		t.Fatalf("downloaded data = %q, want model", data)
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	origDir := configDir
	origFile := configFile
	configDir = dir
	configFile = filepath.Join(dir, "config.yaml")
	defer func() {
		configDir = origDir
		configFile = origFile
	}()

	setDefaults(v)

	if err := SetAndSave("tts_voice", "af_bella"); err != nil {
		t.Fatalf("SetAndSave() error: %v", err)
	}

	// Reload
	setDefaults(v)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.TTSVoice != "af_bella" {
		t.Errorf("TTSVoice = %q, want af_bella (saved)", cfg.TTSVoice)
	}
}

func TestValidateAndSet(t *testing.T) {
	tests := []struct {
		name string
		key  string
		raw  string
		want string
	}{
		{"string", "tts_voice", "af_nova", "af_nova"},
		{"bool", "vad_enabled", "false", "false"},
		{"int", "max_history", "25", "25"},
		{"float", "vad_threshold", "0.45", "0.45"},
		{"float barge-in vad threshold", "barge_in_vad_threshold", "0.6", "0.6"},
		{"int barge-in tail guard", "barge_in_tail_guard_ms", "350", "350"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			origDir, origFile := configDir, configFile
			configDir, configFile = dir, filepath.Join(dir, "config.yaml")
			defer func() { configDir, configFile = origDir, origFile }()
			resetViper(t)

			if err := ValidateAndSet(tt.key, tt.raw); err != nil {
				t.Fatalf("ValidateAndSet() error: %v", err)
			}

			// Reload from disk with a fresh viper: the value must persist and
			// the config must still unmarshal.
			resetViper(t)
			if _, err := Load(); err != nil {
				t.Fatalf("Load() after ValidateAndSet error: %v", err)
			}
			if got := fmt.Sprint(Get(tt.key)); got != tt.want {
				t.Errorf("Get(%s) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestValidateAndSetRejects(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		raw     string
		wantErr string
	}{
		{"unknown key", "vad_treshold", "0.5", "unknown config key"},
		{"bad bool", "vad_enabled", "high", "vad_enabled expects true or false"},
		{"bad int", "max_history", "many", "max_history expects a whole number between 1 and 100"},
		{"bad float", "vad_threshold", "high", "vad_threshold expects a number between 0 and 1"},
		{"barge-in vad threshold below min", "barge_in_vad_threshold", "0.2", "barge_in_vad_threshold expects a number between 0.3 and 0.95"},
		{"barge-in vad threshold above max", "barge_in_vad_threshold", "0.99", "barge_in_vad_threshold expects a number between 0.3 and 0.95"},
		{"barge-in tail guard below min", "barge_in_tail_guard_ms", "100", "barge_in_tail_guard_ms expects a number between 200 and 800"},
		{"barge-in tail guard above max", "barge_in_tail_guard_ms", "5000", "barge_in_tail_guard_ms expects a number between 200 and 800"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			origDir, origFile := configDir, configFile
			configDir, configFile = dir, filepath.Join(dir, "config.yaml")
			defer func() { configDir, configFile = origDir, origFile }()
			resetViper(t)

			err := ValidateAndSet(tt.key, tt.raw)
			if err == nil {
				t.Fatalf("ValidateAndSet(%s, %s) = nil, want error", tt.key, tt.raw)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			if _, statErr := os.Stat(configFile); !os.IsNotExist(statErr) {
				t.Error("config file written despite invalid input")
			}
		})
	}
}

func TestLanguageCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"en-US", "en"},
		{"en", "en"},
		{"", "en"},
		{"C", "c"},
		{"pt-BR", "pt"},
		{" en-GB ", "en"},
	}
	for _, tt := range tests {
		if got := LanguageCode(tt.in); got != tt.want {
			t.Errorf("LanguageCode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLanguageEnvDoesNotLeak(t *testing.T) {
	orig := configFile
	configFile = filepath.Join(t.TempDir(), "nonexistent.yaml")
	defer func() { configFile = orig }()
	resetViper(t)

	t.Setenv("LANGUAGE", "en_US:en") // standard locale var, not a obey-voice setting
	t.Setenv("TTS_VOICE", "af_nova") // documented binding must still apply

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Language != "en-US" {
		t.Errorf("Language = %q, want en-US default (LANGUAGE env must not bind)", cfg.Language)
	}
	if cfg.TTSVoice != "af_nova" {
		t.Errorf("TTSVoice = %q, want af_nova from TTS_VOICE env", cfg.TTSVoice)
	}
}

func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	origDir, origFile := configDir, configFile
	configDir, configFile = dir, filepath.Join(dir, "config.yaml")
	defer func() { configDir, configFile = origDir, origFile }()
	resetViper(t)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(writer bool) {
			defer wg.Done()
			for range 25 {
				if writer {
					_ = SetAndSave("tts_voice", "af_bella")
				} else {
					_ = Get("tts_voice")
					_ = ModelsDir()
					_ = AllSettings()
					_ = AllKeys()
				}
			}
		}(i%2 == 0)
	}
	wg.Wait()
}

// LoadWithoutHooks must read the same configuration as Load while skipping the
// after-load hook. The hook is registered from persona's init, so a library
// consumer gets persona application merely by importing the wrong thing —
// LoadWithoutHooks is its opt-out.
func TestLoadWithoutHooksSkipsTheAfterLoadHook(t *testing.T) {
	prev := afterLoad
	t.Cleanup(func() { afterLoad = prev })

	var calls int
	SetAfterLoad(func(*Config) error {
		calls++
		return nil
	})

	if _, err := LoadWithoutHooks(); err != nil {
		t.Fatalf("LoadWithoutHooks: %v", err)
	}
	if calls != 0 {
		t.Errorf("after-load hook ran %d times under LoadWithoutHooks, want 0", calls)
	}

	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if calls != 1 {
		t.Errorf("after-load hook ran %d times under Load, want 1 — the CLI's behaviour must not change", calls)
	}
}
