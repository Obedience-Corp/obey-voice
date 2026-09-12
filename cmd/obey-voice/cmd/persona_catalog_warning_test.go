//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func seedUnavailablePersona(t *testing.T) {
	t.Helper()
	path := filepath.Join(persona.Dir(), "broken", "persona.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("schema: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPersonaListRetainsJSONAndReportsUnavailableProfile(t *testing.T) {
	personaEnv(t)
	seedUnavailablePersona(t)
	var out, diagnostics bytes.Buffer
	cmd := newPersonaCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostics)
	cmd.SetArgs([]string{"list", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("JSON polluted by warning: %v: %s", err, out.String())
	}
	found := false
	for _, row := range rows {
		if row.ID == "samantha" {
			found = true
		}
		if row.ID == "broken" {
			t.Fatal("unavailable profile advertised as usable")
		}
	}
	if !found || !strings.Contains(diagnostics.String(), "broken") || !strings.Contains(diagnostics.String(), "repair") {
		t.Fatalf("rows=%v diagnostics=%q", rows, diagnostics.String())
	}
}

func TestServePersonaListRetainsHealthyRowsAndRepairIssues(t *testing.T) {
	personaEnv(t)
	seedUnavailablePersona(t)
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro", TTSVoice: "af_heart"}
	rows, err := servePersonaList(cfg)()
	var issues *netapi.PersonaListIssues
	if !errors.As(err, &issues) || len(issues.Issues) != 1 || issues.Issues[0].ID != "broken" {
		t.Fatalf("issues=%+v error=%v", issues, err)
	}
	if !strings.Contains(issues.Issues[0].Message, "Repair") {
		t.Fatalf("missing repair guidance: %+v", issues)
	}
	if !findPersonaRow(t, rows, "samantha").Active {
		t.Fatal("healthy current persona unavailable")
	}
}
