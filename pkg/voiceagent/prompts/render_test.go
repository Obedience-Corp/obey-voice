package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemYAMLOverridesReloadAndSurviveSeeding(t *testing.T) {
	dir := t.TempDir()
	if _, err := Seed(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "system", "application.yaml")
	for _, marker := range []string{"First edit", "Second edit"} {
		body := "schema: obey-voice.prompt.v1\nprompt:\n  name: application\n  kind: system\n  system_prompt: '" + marker + ": {calibre_enabled}'\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Seed(dir); err != nil {
			t.Fatal(err)
		}
		got, err := RenderSystem(dir, "application", map[string]string{"calibre_enabled": "false"})
		if err != nil || got != marker+": false" {
			t.Fatalf("edit lost: %q, %v", got, err)
		}
	}
}

func TestBrokenSystemYAMLUsesPackagedDocumentWithError(t *testing.T) {
	for _, body := range []string{
		"prompt: [unfinished",
		"schema: obey-voice.prompt.v1\nprompt:\n  name: application\n  kind: system\n  system_prompt: '{misspelled_variable}'\n",
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "system"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "system", "application.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		values := map[string]string{"calibre_enabled": "false"}
		want, err := RenderSystem("", "application", values)
		if err != nil {
			t.Fatal(err)
		}
		got, err := RenderSystem(dir, "application", values)
		if err == nil || !strings.Contains(err.Error(), "using packaged YAML") || got != want {
			t.Fatalf("broken override: got=%q err=%v", got, err)
		}
	}
}

func TestSystemRuntimeDataIsNotInterpretedAsAnotherTemplate(t *testing.T) {
	name := "A name containing {other_token}"
	got, err := RenderSystem("", "group-reply", map[string]string{"agent_name": name})
	if err != nil || !strings.Contains(got, name) {
		t.Fatalf("runtime data was reinterpreted: %q, %v", got, err)
	}
}

func TestWorkerContinuationPromptOverrides(t *testing.T) {
	for _, name := range []string{"worker-continuation", "worker-truncated-tools"} {
		t.Run(name, func(t *testing.T) {
			packaged, err := RenderSystem("", name, nil)
			if err != nil || packaged == "" {
				t.Fatalf("packaged prompt=%q err=%v", packaged, err)
			}
			dir := t.TempDir()
			if _, err := Seed(dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "system", name+".yaml")
			for _, marker := range []string{"First continuation edit", "Second continuation edit"} {
				body := "schema: obey-voice.prompt.v1\nprompt:\n  name: " + name + "\n  kind: system\n  system_prompt: '" + marker + "'\n"
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := Seed(dir); err != nil {
					t.Fatal(err)
				}
				if got, err := RenderSystem(dir, name, nil); err != nil || got != marker {
					t.Fatalf("override did not reload: %q %v", got, err)
				}
			}
			if err := os.WriteFile(path, []byte("prompt: [unfinished"), 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := RenderSystem(dir, name, nil); err == nil || got != packaged {
				t.Fatalf("malformed override did not fall back: %q %v", got, err)
			}
		})
	}
}
