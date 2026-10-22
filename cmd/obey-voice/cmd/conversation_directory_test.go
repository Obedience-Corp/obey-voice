//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	appTUI "github.com/Obedience-Corp/obey-voice/internal/tui"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func TestConversationDirectoryStartResumeAndDefaults(t *testing.T) {
	personaEnv(t)
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	cwd, _ := os.Getwd()
	a, _ := filepath.EvalSymlinks(t.TempDir())
	b, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := persona.Load("samantha")
	profile.StartingDirectory = a
	writePersona(t, profile)
	cfg := &config.Config{ActivePersona: "samantha", WorkDir: b}
	if err := resolveConversationDirectory(cfg, "samantha", "", nil); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != a || cfg.WorkDirSource != "persona" {
		t.Fatal(cfg.WorkDir, cfg.WorkDirSource)
	}
	sess := session.New("ollama", "test")
	sess.WorkDir = cfg.WorkDir
	sess.WorkDirSource = cfg.WorkDirSource
	if err := sess.Save(nil); err != nil {
		t.Fatal(err)
	}
	saved, err := session.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile.StartingDirectory = b
	writePersona(t, profile)
	cfg.WorkDir = b
	cfg.WorkDirSource = ""
	if err := resolveConversationDirectory(cfg, "samantha", b, saved); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != a {
		t.Fatal("resume redirected", cfg.WorkDir)
	}
	cfg.WorkDir = b
	cfg.WorkDirSource = ""
	if err := resolveConversationDirectory(cfg, appTUI.StartDefaultsID, "", nil); err != nil {
		t.Fatal(err)
	}
	if cfg.WorkDir != b {
		t.Fatal("defaults used persona directory")
	}
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	if err := resolveConversationDirectory(cfg, "samantha", "", saved); err == nil {
		t.Fatal("missing saved directory accepted")
	}
	if after, _ := os.Getwd(); after != cwd {
		t.Fatal("session resolution changed cwd")
	}
}

func TestServeResumeRestoresDirectoryBeforeResponderAndRejectsMissing(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	t.Setenv("OBEY_VOICE_WORK_DIR", "")
	t.Setenv("WORK_DIR", "")
	a, _ := filepath.EvalSymlinks(t.TempDir())
	b, _ := filepath.EvalSymlinks(t.TempDir())
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro", WorkDir: a, WorkDirSource: "persona"}
	s := newSwitcherForTest(t, cfg)
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	saved := session.New("ollama", "test")
	saved.WorkDir = b
	saved.WorkDirSource = "persona"
	if err := session.DefaultStore().SaveSelection(saved, nil, "samantha", "Samantha", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(t.Context(), saved.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.selector.Config(); got.WorkDir != b {
		t.Fatal("resumed responder rooted in old folder", got.WorkDir)
	}
	if got := s.selector.Snapshot(); got.WorkDir != b {
		t.Fatal("snapshot directory mismatch", got)
	}
	disk, err := session.Load(saved.ID)
	if err != nil || disk.WorkDir != b {
		t.Fatal(disk, err)
	}
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	priorID := s.sessions.id()
	if err := s.resume(t.Context(), saved.ID); err == nil {
		t.Fatal("resumed missing directory")
	}
	if s.sessions.id() != priorID {
		t.Fatal("failed resume changed conversation")
	}
}
