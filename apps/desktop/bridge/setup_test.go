package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestSetupBlankAndExistingConfigRemainUntouched(t *testing.T) {
	dir := t.TempDir()
	config.SetConfigDirForTest(t, dir)
	v, err := setupStatus()
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["existing"] != false {
		t.Fatal("blank install was not detected")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("discovery wrote files: %v %v", entries, err)
	}
	original := []byte("brain_provider: ollama\nollama_model: preserve-me\n")
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	v, err = setupStatus()
	if err != nil || v.(map[string]any)["existing"] != true {
		t.Fatalf("existing detection: %v %v", v, err)
	}
	p := setupProgress{Version: 1, Step: 3, TextOnly: true, Silent: true}
	data, _ := json.Marshal(p)
	if _, err := saveSetup(setupPath(), data); err != nil {
		t.Fatal(err)
	}
	saved, warning := readSetup(setupPath())
	if saved != p || warning != "" {
		t.Fatalf("resume lost progress: %+v %s", saved, warning)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("setup altered voice configuration")
	}
	info, _ := os.Stat(setupPath())
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
}

func TestSetupCorruptionAndInvalidWritesFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.json")
	for _, data := range []string{`{`, `{"version":2}`, `{"version":1,"step":8}`, `{"version":1,"completed":true,"deferred":true}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		p, warning := readSetup(path)
		if p.Completed || p.Step != 0 || warning == "" {
			t.Fatalf("corrupt state accepted: %+v", p)
		}
		if _, err := saveSetup(path, []byte(data)); err == nil {
			t.Fatal("invalid save accepted")
		}
	}
}

func TestSetupOllamaReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, body, model string
		code              int
		ok                bool
	}{
		{"present", `{"models":[{"name":"test:latest"}]}`, "test", 200, true},
		{"missing", `{"models":[{"name":"other"}]}`, "test", 200, false},
		{"invalid", `not json`, "test", 200, false},
		{"unauthorized", `{}`, "test", 401, false},
		{"redirect", `{}`, "test", 302, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" || r.Method != "GET" {
					t.Errorf("unexpected provider request: %v", r)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			d := probeOllama(context.Background(), server.URL, tc.model)
			if (d.Severity == config.SeverityOK) != tc.ok {
				t.Fatalf("unexpected check: %+v", d)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if probeOllama(ctx, "http://127.0.0.1:1", "test").Severity == config.SeverityOK {
		t.Fatal("offline provider passed")
	}
}

func TestObeyOptionalDesktopSetup(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	t.Setenv("PATH", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"fixture:latest"}]}`))
	}))
	defer server.Close()
	for key, value := range map[string]string{"brain_provider": "ollama", "ollama_host": server.URL, "ollama_model": "fixture", "active_persona": ""} {
		if err := config.SetAndSave(key, value); err != nil {
			t.Fatal(err)
		}
	}
	result, err := setupProvider(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.(providerCheck).Severity != config.SeverityOK {
		t.Fatalf("Obey absence broke Ollama: %+v", result)
	}
	if err := config.SetAndSave("brain_provider", "obey"); err != nil {
		t.Fatal(err)
	}
	result, err = setupProvider(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check := result.(providerCheck)
	if check.Severity != config.SeverityError || !strings.Contains(check.Detail, "obey CLI not found") || !strings.Contains(check.Remediation, "select another") {
		t.Fatalf("missing optional CLI guidance: %+v", check)
	}
}
