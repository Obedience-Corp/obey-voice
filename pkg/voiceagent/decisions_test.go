package voiceagent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

func TestDecisionOffDoesNotProvision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent-models")
	cfg := &config.Config{ModelsDir: dir, DecisionProvider: "openai", DecisionModel: "not-used", DecisionVoiceWork: true, DecisionTextWork: true}
	t.Setenv("DECISION_API_KEY", "unused-fixture-secret")
	p := &pipeline.Pipeline{}
	closeRuntime := bindDecisions(p, cfg)
	defer closeRuntime()
	if p.DecisionStatus().Phase != "off" || p.Decisions.Enabled(decision.VoiceWork) {
		t.Fatal("off activated")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("off touched model directory: %v", err)
	}
}

func TestDecisionSettingsDisableCancelsWithoutCancellingConversation(t *testing.T) {
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("Authorization") != "" {
			t.Error("local request received a credential")
		}
		close(entered)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	cfg := &config.Config{DecisionEnabled: true, DecisionVoiceWork: true, DecisionProvider: "local", DecisionRuntime: "external", DecisionEndpoint: server.URL + "/v1/systemone", DecisionAPIKey: "must-not-send", DecisionModel: "hosted-model-must-not-apply"}
	p := &pipeline.Pipeline{}
	closeRuntime := bindDecisions(p, cfg)
	defer closeRuntime()
	cfg.DecisionEndpoint = "http://192.0.2.1/v1/systemone" // cannot mutate the runtime snapshot
	deadline := time.Now().Add(time.Second)
	for p.DecisionStatus().Phase != "ready" {
		if time.Now().After(deadline) {
			t.Fatal(p.DecisionStatus())
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempt := p.Decisions.Start(ctx, decision.VoiceWork, decision.WorkInput{Text: "List the files.", EnabledTiers: []decision.Tier{decision.Simple}})
	defer attempt.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	p.ConfigureDecisions(&config.Config{})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disable did not cancel request")
	}
	if ctx.Err() != nil {
		t.Fatal("conversation was cancelled")
	}
	if _, ok := attempt.Take(); ok {
		t.Fatal("disabled result usable")
	}
}

func TestManagedDecisionStartupRequiresExplicitSetup(t *testing.T) {
	for _, runtimeMode := range []string{"", "managed"} {
		t.Run("runtime="+runtimeMode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-installed")
			cfg := &config.Config{ModelsDir: dir, DecisionEnabled: true, DecisionVoiceWork: true, DecisionTextWork: true, DecisionProvider: "local", DecisionRuntime: runtimeMode, DecisionDevice: "cpu", DecisionLocalModel: "2b"}
			before := *cfg
			p := &pipeline.Pipeline{}
			closeRuntime := bindDecisions(p, cfg)
			defer closeRuntime()
			deadline := time.After(time.Second)
			for p.DecisionStatus().Phase == "preparing" {
				select {
				case <-deadline:
					t.Fatal("missing assets did not become unavailable promptly")
				case <-time.After(time.Millisecond):
				}
			}
			status := p.DecisionStatus()
			if status.Phase != "unavailable" || status.Err == nil || !strings.Contains(status.Err.Error(), "Settings > Brain") || !strings.Contains(status.Err.Error(), "models decision install") {
				t.Fatalf("missing setup guidance: %+v", status)
			}
			if p.Decisions.Enabled(decision.VoiceWork) || p.Decisions.Enabled(decision.TextWork) {
				t.Fatal("unprepared decisions activated")
			}
			if attempt := p.Decisions.Start(t.Context(), decision.TextWork, decision.WorkInput{Text: "List the files.", EnabledTiers: []decision.Tier{decision.Simple}}); attempt != nil {
				attempt.Close()
				t.Fatal("unavailable decisions intercepted ordinary routing")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("startup provisioned model directory: %v", err)
			}
			if !reflect.DeepEqual(*cfg, before) {
				t.Fatal("startup changed saved selection")
			}
		})
	}
}
