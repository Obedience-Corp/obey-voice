package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// toolRowNamed focuses the Tools row called name and returns it.
func toolRowNamed(t *testing.T, m *settingsModel, name string) settingRow {
	t.Helper()
	for i, row := range m.toolRows() {
		if row.name == name {
			m.cursor = i
			return row
		}
	}
	t.Fatalf("no Tools row %q", name)
	return settingRow{}
}

func enter(m settingsModel) settingsModel {
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m
}

func TestPictureMakerRowsTurnPictureMakingOn(t *testing.T) {
	saved := map[string]any{}
	m := talkSettings(t, &config.Config{}, saved)

	maker := toolRowNamed(t, &m, "Picture maker")
	if maker.value != "off" || maker.key != "image_generation" {
		t.Fatalf("picture maker row = %+v", maker)
	}
	address := toolRowNamed(t, &m, "ComfyUI address")
	if !address.disabled || address.value != config.DefaultComfyUIURL {
		t.Fatalf("address row with picture making off = %+v", address)
	}
	m = enter(m)
	if m.edit.kind != editNone || !strings.Contains(m.message, "picture maker is off") {
		t.Fatalf("a disabled address row opened %v, message %q", m.edit.kind, m.message)
	}

	toolRowNamed(t, &m, "Picture maker")
	m = enter(m)
	if saved["image_generation"] != config.ImageGenerationComfyUI || m.cfg.ImageGeneration != config.ImageGenerationComfyUI {
		t.Fatalf("saved %v cfg %q, want comfyui", saved["image_generation"], m.cfg.ImageGeneration)
	}
	if !strings.Contains(m.message, "must be running at "+config.DefaultComfyUIURL) {
		t.Fatalf("message = %q", m.message)
	}
	if row := toolRowNamed(t, &m, "Picture maker"); row.value != "ComfyUI" {
		t.Fatalf("row after choosing = %q", row.value)
	}

	// Enter again goes back to off.
	m = enter(m)
	if saved["image_generation"] != config.ImageGenerationOff {
		t.Fatalf("saved %v, want off", saved["image_generation"])
	}
}

func TestComfyUIAddressEditor(t *testing.T) {
	saved := map[string]any{}
	m := talkSettings(t, &config.Config{ImageGeneration: config.ImageGenerationComfyUI}, saved)

	toolRowNamed(t, &m, "ComfyUI address")
	m = enter(m)
	if m.edit.kind != editImage || m.edit.input.Value() != config.DefaultComfyUIURL {
		t.Fatalf("editor %v holding %q", m.edit.kind, m.edit.input.Value())
	}
	m.edit.input.SetValue("localhost:9000")
	m = enter(m)
	if m.edit.kind != editImage || !strings.Contains(m.message, "not an http or https URL") {
		t.Fatalf("an unusable address closed the editor or gave %q", m.message)
	}
	if _, wrote := saved["comfyui_url"]; wrote {
		t.Fatal("an unusable address was saved")
	}
	m.edit.input.SetValue(" http://studio.local:8188 ")
	m = enter(m)
	if m.edit.kind != editNone || saved["comfyui_url"] != "http://studio.local:8188" || m.cfg.ComfyUIURL != "http://studio.local:8188" {
		t.Fatalf("saved %v cfg %q editor %v", saved["comfyui_url"], m.cfg.ComfyUIURL, m.edit.kind)
	}

	// Clearing the field restores the default address.
	toolRowNamed(t, &m, "ComfyUI address")
	m = enter(m)
	m.edit.input.SetValue("")
	m = enter(m)
	if saved["comfyui_url"] != config.DefaultComfyUIURL {
		t.Fatalf("saved %v, want the default", saved["comfyui_url"])
	}
}

func TestPictureWorkflowEditor(t *testing.T) {
	saved := map[string]any{}
	m := talkSettings(t, &config.Config{ImageGeneration: config.ImageGenerationComfyUI}, saved)
	if row := toolRowNamed(t, &m, "Picture workflow"); !strings.Contains(row.value, "built-in") {
		t.Fatalf("workflow row = %q", row.value)
	}

	m = enter(m)
	m.edit.input.SetValue(filepath.Join(t.TempDir(), "missing.json"))
	m = enter(m)
	if m.edit.kind != editImage || !strings.Contains(m.message, "is not a workflow file") {
		t.Fatalf("a missing file closed the editor or gave %q", m.message)
	}

	file := filepath.Join(t.TempDir(), "graph.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m.edit.input.SetValue(file)
	m = enter(m)
	if saved["image_workflow"] != file || m.cfg.ImageWorkflow != file {
		t.Fatalf("saved %v cfg %q", saved["image_workflow"], m.cfg.ImageWorkflow)
	}
	if row := toolRowNamed(t, &m, "Picture workflow"); row.value != file {
		t.Fatalf("row after saving = %q", row.value)
	}

	m = enter(m)
	m.edit.input.SetValue("")
	m = enter(m)
	if saved["image_workflow"] != "" || !strings.Contains(m.message, "built-in") {
		t.Fatalf("saved %v message %q, want the built-in workflow", saved["image_workflow"], m.message)
	}
}
