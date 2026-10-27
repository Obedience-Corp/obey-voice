package config

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDiagnoseOpenAISpeechOnAReadyOllama(t *testing.T) {
	fake := &fakeOllama{version: "0.41.2", tags: []string{"phonon:latest"}, voices: []string{"Zoey", "Easton"}}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	writeVoicesFile(t, cfg)

	got := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))
	for _, name := range []string{"openai-speech-server", "openai-speech-model", "openai-speech-voices", "openai-speech-catalog-file"} {
		if got[name].Severity != SeverityOK {
			t.Errorf("%s = %+v, want ok", name, got[name])
		}
	}
	if !strings.Contains(got["openai-speech-server"].Detail, "Ollama 0.41.2") || !strings.Contains(got["openai-speech-voices"].Detail, "2 voices") {
		t.Fatalf("details = %+v", got)
	}
}

func TestDiagnoseOpenAISpeechNamesEachRemedy(t *testing.T) {
	t.Run("a release without the voice catalog", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest"}}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		got := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))
		voices := got["openai-speech-voices"]
		if voices.Severity != SeverityWarn || !strings.Contains(voices.Remediation, MinPhononVoicesOllama) {
			t.Fatalf("voices = %+v", voices)
		}
		file := got["openai-speech-catalog-file"]
		if file.Severity != SeverityWarn || !strings.Contains(file.Remediation, "models ensure --tts") {
			t.Fatalf("catalog file = %+v", file)
		}
	})
	t.Run("a failed /api/show is not an upgrade note", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{"phonon:latest"}, showStatus: http.StatusInternalServerError}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		voices := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))["openai-speech-voices"]
		if voices.Severity != SeverityWarn || voices.Remediation != "" || !strings.Contains(voices.Detail, "could not read") {
			t.Fatalf("voices = %+v", voices)
		}
	})
	t.Run("a model not pulled", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.9", tags: []string{}}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		got := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))
		model := got["openai-speech-model"]
		if model.Severity != SeverityError || !strings.Contains(model.Remediation, "models ensure --tts") || !strings.Contains(model.Remediation, "ollama pull phonon") {
			t.Fatalf("model = %+v", model)
		}
		if _, ok := got["openai-speech-voices"]; ok {
			t.Fatal("asked for the voices of a model that is not there")
		}
	})
	t.Run("an Ollama too old for Phonon", func(t *testing.T) {
		fake := &fakeOllama{version: "0.40.8", tags: []string{}}
		cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
		server := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))["openai-speech-server"]
		if server.Severity != SeverityError || !strings.Contains(server.Detail, MinPhononOllama) {
			t.Fatalf("server = %+v", server)
		}
	})
	t.Run("a server that is down", func(t *testing.T) {
		cfg := openAISpeechCfg(t, closedURL(t)+"/v1")
		got := diagByName(DiagnoseOpenAISpeech(context.Background(), cfg, ModelsDirFrom(cfg)))
		if server := got["openai-speech-server"]; server.Severity != SeverityError || !strings.Contains(server.Remediation, "ollama serve") {
			t.Fatalf("server = %+v", server)
		}
		if _, ok := got["openai-speech-model"]; ok {
			t.Fatal("model row for a server that did not answer")
		}
	})
}

func TestDiagnoseOpenAISpeechIsSilentForOtherEngines(t *testing.T) {
	if got := DiagnoseOpenAISpeech(context.Background(), &Config{TTSProvider: "kokoro"}, t.TempDir()); got != nil {
		t.Fatalf("kokoro got openai-speech rows: %+v", got)
	}
}
