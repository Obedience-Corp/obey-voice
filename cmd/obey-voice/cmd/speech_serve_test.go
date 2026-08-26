//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/speechserve"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func resetSpeechServeFlags(t *testing.T) {
	t.Helper()
	bind, tokenFile := speechServeBind, speechServeTokenFile
	cert, key := speechServeTLSCert, speechServeTLSKey
	t.Cleanup(func() {
		speechServeBind, speechServeTokenFile = bind, tokenFile
		speechServeTLSCert, speechServeTLSKey = cert, key
	})
	speechServeBind = speechserve.DefaultBind
	speechServeTokenFile = ""
	speechServeTLSCert, speechServeTLSKey = "", ""
}

func TestResolveSpeechTokenPrefersFileThenEnvThenCredentials(t *testing.T) {
	resetSpeechServeFlags(t)
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)

	tokenPath := filepath.Join(dir, "explicit-token")
	if err := os.WriteFile(tokenPath, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	speechServeTokenFile = tokenPath
	t.Setenv(config.SpeechTokenEnv, "from-env")
	token, minted, err := resolveSpeechToken()
	if err != nil || token != "from-file" || minted {
		t.Fatalf("token=%q minted=%v err=%v", token, minted, err)
	}

	speechServeTokenFile = ""
	token, minted, err = resolveSpeechToken()
	if err != nil || token != "from-env" || minted {
		t.Fatalf("token=%q minted=%v err=%v", token, minted, err)
	}

	t.Setenv(config.SpeechTokenEnv, "")
	token, minted, err = resolveSpeechToken()
	if err != nil {
		t.Fatalf("resolveSpeechToken: %v", err)
	}
	if !minted || len(token) < 32 {
		t.Fatalf("token=%q minted=%v, want a freshly minted one", token, minted)
	}
	if _, err := os.Stat(filepath.Join(dir, "speech", speechserve.TokenFileName)); err != nil {
		t.Fatalf("token was not stored beside serve's: %v", err)
	}
}

func TestResolveSpeechTokenRejectsAnEmptyFile(t *testing.T) {
	resetSpeechServeFlags(t)
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	speechServeTokenFile = path

	if _, _, err := resolveSpeechToken(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err=%v want an empty-token refusal", err)
	}
}

func TestSpeechServeTLSRequiresBothHalves(t *testing.T) {
	resetSpeechServeFlags(t)

	if cfg, err := speechServeTLS(); err != nil || cfg != nil {
		t.Fatalf("no flags must mean no TLS: cfg=%v err=%v", cfg, err)
	}
	speechServeTLSCert = "/tmp/cert.pem"
	if _, err := speechServeTLS(); err == nil || !strings.Contains(err.Error(), "--tls-key") {
		t.Fatalf("err=%v want both halves required", err)
	}
}

func TestResolveSpeechChildNeedsAWorker(t *testing.T) {
	cfg := &config.Config{ModelsDir: t.TempDir()}

	if _, err := resolveSpeechChild(cfg); err == nil || !strings.Contains(err.Error(), "models ensure") {
		t.Fatalf("err=%v want the install remediation", err)
	}

	cfg.QwenTTSBinary = "/opt/qwen3-tts-worker"
	cfg.QwenTTSModel = "/opt/models"
	cfg.QwenTTSModelTier = "1.7b"
	spec, err := resolveSpeechChild(cfg)
	if err != nil {
		t.Fatalf("resolveSpeechChild: %v", err)
	}
	if spec.Binary != cfg.QwenTTSBinary || spec.ModelDir != cfg.QwenTTSModel || spec.Tier != "1.7b" {
		t.Fatalf("spec=%+v", spec)
	}
}
