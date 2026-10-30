//go:build integration

package discovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	obey "github.com/Obedience-Corp/obey-go-sdk/pkg/obey"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func init() {
	if os.Getenv("VOICE_CATALOG_HELPER") != "1" {
		return
	}
	if os.Getenv("VOICE_CATALOG_CONTAINER") != "1" {
		os.Exit(90)
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		os.Exit(91)
	}
	args := os.Args[1:]
	if len(args) > 1 && args[0] == "--socket" {
		args = args[2:]
	}
	f, err := os.OpenFile(os.Getenv("VOICE_CATALOG_LEDGER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(92)
	}
	_ = json.NewEncoder(f).Encode(os.Args[1:])
	_ = f.Close()
	if os.Getenv("VOICE_CATALOG_MODE") == "blocked" {
		time.Sleep(time.Minute)
		os.Exit(93)
	}
	if len(args) > 0 && args[0] == "doctor" {
		if os.Getenv("VOICE_CATALOG_MODE") == "legacy" {
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "obey.doctor", "version": 3, "status": "unknown", "checks": []any{}, "capabilities": map[string]any{
			"provider_list": map[string]any{"protocol": "obey.providers.list", "versions": []int{1}}, "provider_models": map[string]any{"protocol": "obey.providers.models", "versions": []int{1}}, "provider_check": map[string]any{"protocol": "obey.providers.check", "versions": []int{1}},
		}})
		os.Exit(0)
	}
	header := obey.CatalogMetadata{Version: 1, ObservedAt: time.Now().UTC(), DaemonVersion: "fixture"}
	if len(args) > 1 && args[1] == "models" {
		header.Schema = "obey.providers.models"
		_ = json.NewEncoder(os.Stdout).Encode(obey.ModelCatalog{CatalogMetadata: header, ProviderID: "daemon-profile", ProviderRevision: "r1", Source: "live", State: "fresh", Models: []obey.ModelInfo{{ID: "raw-model:1"}}, Diagnostics: []obey.DiscoveryDiagnostic{}})
	} else if len(args) > 1 && args[1] == "check" {
		header.Schema = "obey.providers.check"
		checks := []obey.ReadinessCheck{}
		for _, stage := range []string{"cli", "daemon", "provider", "model"} {
			checks = append(checks, obey.ReadinessCheck{Stage: stage, Code: "checked", State: obey.CheckUnknown})
		}
		checks = append(checks, obey.ReadinessCheck{Stage: "requirements", Code: "tools_off_unsupported", State: obey.CheckUnsupported})
		_ = json.NewEncoder(os.Stdout).Encode(obey.Readiness{CatalogMetadata: header, ProviderID: "daemon-profile", ModelID: "saved-manual", Fidelity: obey.FidelityFinalOnly, Checks: checks})
	} else {
		header.Schema = "obey.providers.list"
		_ = json.NewEncoder(os.Stdout).Encode(obey.ProviderCatalog{CatalogMetadata: header, Providers: []obey.ProviderInfo{{ID: "daemon-profile", Type: "openai", Enabled: true, Revision: "r1", Health: obey.CheckUnknown}}, Diagnostics: []obey.DiscoveryDiagnostic{}})
	}
	os.Exit(0)
}

func TestObeyCatalogIntegrationScopeAndFailure(t *testing.T) {
	if os.Getenv("VOICE_CATALOG_CONTAINER") != "1" {
		t.Skip("requires explicit disposable container fixture")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("requires Docker isolation")
	}
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, filepath.Join(dir, "obey")); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "calls.jsonl")
	t.Setenv("PATH", dir)
	t.Setenv("VOICE_CATALOG_HELPER", "1")
	// Helper children have no background work; avoid the race runtime's one-second exit pause consuming the production discovery deadline.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("VOICE_CATALOG_LEDGER", ledger)
	cfg := &config.Config{BrainProvider: "obey", ObeySocket: "/selected.sock", ObeyProvider: "daemon-profile", ObeyModel: "saved-manual", ObeyToolPolicy: "strict"}
	route := DiscoverObeyRoute(t.Context(), cfg, true)
	if route.Problem != "" || route.Providers == nil || route.Models == nil || route.Models.Models[0].ID != "raw-model:1" || route.Readiness == nil || !route.Readiness.HasFailures() {
		t.Fatalf("route=%+v", route)
	}
	raw, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.TrimSpace(string(raw))
	if !strings.Contains(calls, `"--refresh"`) || !strings.Contains(calls, `"--tools","off"`) || strings.Contains(calls, `"run"`) || strings.Contains(calls, `"session"`) {
		t.Fatal(calls)
	}
	for _, line := range strings.Split(calls, "\n") {
		if !strings.HasPrefix(line, `["--socket","/selected.sock",`) {
			t.Fatal("socket scope lost: " + line)
		}
	}
	if cfg.ObeyModel != "saved-manual" {
		t.Fatal("catalog replaced manual ID")
	}
	t.Setenv("VOICE_CATALOG_MODE", "legacy")
	route = DiscoverObeyRoute(t.Context(), cfg, false)
	if route.Providers != nil || !strings.Contains(route.Problem, "unsupported_protocol") {
		t.Fatalf("legacy route=%+v", route)
	}
	t.Setenv("VOICE_CATALOG_MODE", "blocked")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	before := time.Now()
	route = DiscoverObeyRoute(ctx, cfg, false)
	if route.Problem == "" || time.Since(before) > 2*time.Second {
		t.Fatalf("cancellation not bounded: %+v", route)
	}
	beforeCalls, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	cfg.BrainProvider = "ollama"
	info := discoverObey(t.Context(), cfg)
	if !info.Available || cfg.ObeyModel != "saved-manual" {
		t.Fatal("optional backend changed native route")
	}
	afterCalls, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCalls) != string(beforeCalls) {
		t.Fatal("native backend invoked the Obey CLI")
	}
}
