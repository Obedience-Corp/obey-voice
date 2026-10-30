package config

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNativeSpeechPreservesEndpointAndModelIntent(t *testing.T) {
	for _, tt := range []struct {
		name, goos string
		cfg        Config
		want       bool
	}{
		{"fresh Linux", "linux", Config{OpenAISpeechRuntime: "auto"}, true},
		{"Mac keeps Ollama", "darwin", Config{OpenAISpeechRuntime: "auto"}, false},
		{"library literal keeps external", "linux", Config{}, false},
		{"explicit Mac endpoint", "linux", Config{OpenAISpeechRuntime: "auto", OpenAISpeechURL: "http://studio:11434/v1"}, false},
		{"explicit default endpoint", "linux", Config{OpenAISpeechRuntime: "auto", OpenAISpeechURL: DefaultOpenAISpeechURL, OpenAISpeechExternal: true}, false},
		{"custom model", "linux", Config{OpenAISpeechRuntime: "auto", OpenAISpeechModel: "custom:pin"}, false},
		{"external override", "linux", Config{OpenAISpeechRuntime: "external"}, false},
		{"native opt-in", "linux", Config{OpenAISpeechRuntime: "native", OpenAISpeechExternal: true}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativePhononFor(&tt.cfg, tt.goos); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestNativeSpeechReadinessIsOfflineAndActionable(t *testing.T) {
	cfg := &Config{TTSProvider: OpenAISpeechProviderName, OpenAISpeechRuntime: "native", OpenAISpeechBackend: "cpu", ModelsDir: t.TempDir()}
	target := ProbeOpenAISpeech(context.Background(), cfg)
	if !target.Native || target.ModelListed || target.Err == nil {
		t.Fatalf("unexpected target %+v", target)
	}
	rows := OpenAISpeechAssetStatus(target, cfg.ModelsDir)
	if len(rows) == 0 || rows[0].Installed || !strings.Contains(rows[0].Detail, "models ensure") {
		t.Fatalf("unexpected status %+v", rows)
	}
	d := DiagnoseOpenAISpeech(context.Background(), cfg, cfg.ModelsDir)
	if len(d) != 1 || d[0].Severity != SeverityError || !strings.Contains(d[0].Remediation, "models ensure") {
		t.Fatalf("unexpected doctor %+v", d)
	}
	cfg.OpenAISpeechModel = "custom:pin"
	if _, err := NativePhononOptions(cfg); err == nil {
		t.Fatal("native selection discarded custom model")
	}
	cfg.OpenAISpeechModel = "phonon"
	cfg.OpenAISpeechURL = "https://custom.example/v1"
	if _, err := NativePhononOptions(cfg); err == nil {
		t.Fatal("native selection discarded explicit endpoint")
	}
}

func TestLoadTracksSpeechEndpointDeclaration(t *testing.T) {
	SetConfigDirForTest(t, t.TempDir())
	t.Setenv("OPENAI_SPEECH_URL", "http://studio:11434/v1")
	cfg, err := LoadRaw()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OpenAISpeechExternal || NativePhononSelected(cfg) {
		t.Fatal("explicit endpoint lost its provenance")
	}
}

func TestMLXPullRejectionExplainsCompatibleRuntime(t *testing.T) {
	fake := &fakeOllama{version: "0.40.13", tags: []string{}, pull: pullStream(`{"error":"this model requires MLX support, but the MLX runtime is not available"}`)}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	_, hits := fakeCatalog(t, http.StatusOK, fakeVoicesJSONL)
	err := installTTS(context.Background(), cfg, &tickLog{})
	if err == nil || !strings.Contains(err.Error(), "openai_speech_runtime native") || !strings.Contains(err.Error(), "MLX") {
		t.Fatalf("got %v", err)
	}
	if *hits != 0 {
		t.Fatal("fetched catalog after runtime rejection")
	}
}
