package config

import (
	"context"
	"strings"
	"testing"
)

func statusRows(t *testing.T, cfg *Config) map[string]AssetStatus {
	t.Helper()
	rows := OpenAISpeechAssetStatus(ProbeOpenAISpeech(context.Background(), cfg), ModelsDirFrom(cfg))
	out := map[string]AssetStatus{}
	for _, r := range rows {
		out[r.ID] = r
	}
	if len(out) != len(rows) {
		t.Fatalf("duplicate row ids: %+v", rows)
	}
	return out
}

func TestOpenAISpeechStatusOnOllama(t *testing.T) {
	fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest", "qwen3.8:27b"}}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")

	rows := statusRows(t, cfg)
	model, ok := rows[OpenAISpeechModelAssetID]
	if !ok || !model.Installed || model.Name != "phonon (Ollama)" || model.Mode != "ollama" {
		t.Fatalf("model row = %+v", model)
	}
	if _, ok := rows[OpenAISpeechServerAssetID]; ok {
		t.Fatalf("Ollama target also reported a server row: %+v", rows)
	}
	catalog := rows[OpenAISpeechVoicesAssetID]
	if catalog.Installed || !catalog.Optional || len(catalog.Missing) != 1 || catalog.Missing[0] != voicesPath(cfg) {
		t.Fatalf("catalog row before install = %+v", catalog)
	}

	writeVoicesFile(t, cfg)
	if catalog := statusRows(t, cfg)[OpenAISpeechVoicesAssetID]; !catalog.Installed || !catalog.Optional || catalog.Missing != nil {
		t.Fatalf("catalog row after install = %+v", catalog)
	}

	fake.tags = []string{"phonon:31m"}
	model = statusRows(t, cfg)[OpenAISpeechModelAssetID]
	if model.Installed || !strings.Contains(model.Detail, "models ensure --tts") {
		t.Fatalf("a different phonon tag is not the configured model: %+v", model)
	}
}

func TestOpenAISpeechStatusWithoutOllama(t *testing.T) {
	ptts := &fakeOllama{tags: []string{"phonon"}, notOllama: true}
	rows := statusRows(t, openAISpeechCfg(t, ptts.start(t).URL+"/v1"))
	if _, ok := rows[OpenAISpeechModelAssetID]; ok {
		t.Fatalf("a non-Ollama server got a model row: %+v", rows)
	}
	if server := rows[OpenAISpeechServerAssetID]; !server.Installed {
		t.Fatalf("server row = %+v", server)
	}
	if _, ok := rows[OpenAISpeechVoicesAssetID]; !ok {
		t.Fatal("catalog file row missing for a non-Ollama server")
	}

	// Down is a row, never an error: an Ollama-shaped URL keeps its model row.
	down := statusRows(t, openAISpeechCfg(t, closedURL(t)+"/v1"))
	model := down[OpenAISpeechModelAssetID]
	if model.Installed || !strings.Contains(model.Detail, "ollama serve") {
		t.Fatalf("model row with Ollama down = %+v", model)
	}
	server := statusRows(t, openAISpeechCfg(t, closedURL(t)))[OpenAISpeechServerAssetID]
	if server.Installed || !strings.Contains(server.Detail, "not answering") {
		t.Fatalf("server row with the server down = %+v", server)
	}
}

func TestOllamaReleaseComparisonIsNumeric(t *testing.T) {
	for v, want := range map[string]bool{
		"0.40.9": true, "0.40.10": true, "0.41.0-rc1": true, "1.0.0": true,
		"0.40.8": false, "0.9.99": false,
		"0.0.0": true, "": true, "dev": true,
	} {
		if got := ollamaAtLeast(v, MinPhononOllama); got != want {
			t.Errorf("ollamaAtLeast(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestARejectedKeyIsNotAServerThatIsDown(t *testing.T) {
	cfg := openAISpeechCfg(t, rejectingServer(t)+"/v1")
	model := statusRows(t, cfg)[OpenAISpeechModelAssetID]
	if model.Installed || !strings.Contains(model.Detail, "rejected the request (401): check openai_speech_api_key") || strings.Contains(model.Detail, "ollama serve") {
		t.Fatalf("model row = %+v", model)
	}
	server := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))["openai-speech-server"]
	if server.Severity != SeverityError || server.Remediation != "check openai_speech_api_key" {
		t.Fatalf("doctor row = %+v", server)
	}
}
