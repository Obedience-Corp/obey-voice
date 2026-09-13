package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

func TestValidateWorkerPlanUsesReadOnlyLocalCatalog(t *testing.T) {
	var chatCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"}]}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/show" {
			_, _ = w.Write([]byte(`{"details":{"family":"qwen"},"capabilities":["completion","tools"],"model_info":{"general.architecture":"qwen"}}`))
			return
		}
		if r.URL.Path == "/api/chat" {
			chatCalls.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	plan := localPlan(srv.URL, "qwen3:8b")
	if err := ValidateWorkerPlan(context.Background(), plan); err != nil {
		t.Fatalf("ValidateWorkerPlan: %v", err)
	}
	if got := chatCalls.Load(); got != 0 {
		t.Fatalf("probe made %d inference calls", got)
	}
}

func TestValidateWorkerPlanRejectsRemoteCloudAlias(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"gpt-oss:cloud"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"details":{"family":"qwen"},"capabilities":["completion","tools"],"remote_model":"gpt-oss","remote_host":"https://ollama.com"}`))
	}))
	t.Cleanup(srv.Close)
	err := ValidateWorkerPlan(context.Background(), localPlan(srv.URL, "gpt-oss:cloud"))
	if err == nil || !strings.Contains(err.Error(), "remote or cloud") {
		t.Fatalf("error = %v, want cloud alias rejection", err)
	}
}

func TestValidateWorkerPlanRejectsStaticSpoofsBeforeNetwork(t *testing.T) {
	tests := []config.WorkerAssignment{
		{Provider: "grok", Model: "m", Billing: config.WorkerBillingLocal},
		{Provider: "ollama", Model: "m", Billing: config.WorkerBillingSubscription},
		{Provider: "ollama", Model: "m", Billing: config.WorkerBillingLocal, AccountRef: strptr("acct")},
		{Provider: "ollama", Model: "m", Billing: config.WorkerBillingLocal, EndpointRef: strptr("http://192.168.1.2:11434")},
	}
	for _, assignment := range tests {
		if err := ValidateWorkerPlan(context.Background(), &config.WorkerPlan{Assignment: assignment}); err == nil {
			t.Errorf("assignment %+v was accepted", assignment)
		}
	}
}

func TestValidateWorkerPlanCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ValidateWorkerPlan(ctx, localPlan("http://127.0.0.1:1", "m"))
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestNewPlannedWorkerRequiresPinnedEndpointAndModel(t *testing.T) {
	plan := &config.WorkerPlan{Assignment: config.WorkerAssignment{Provider: "ollama", Model: "m", Billing: config.WorkerBillingLocal}}
	_, err := NewPlannedWorker(context.Background(), &config.Config{}, plan)
	if err == nil || !strings.Contains(err.Error(), "endpoint_ref is required") {
		t.Fatalf("error = %v, want pinned endpoint rejection", err)
	}
}

func TestNewPlannedWorkerUsesPinnedAssignment(t *testing.T) {
	var shownModel string
	var forbiddenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"pinned:7b"}]}`))
			return
		}
		if r.URL.Path == "/api/show" {
			var request struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			shownModel = request.Model
			_, _ = w.Write([]byte(`{"details":{"family":"qwen"},"capabilities":["completion","tools"],"model_info":{"general.architecture":"qwen"}}`))
			return
		}
		if r.URL.Path == "/api/embed" || r.URL.Path == "/api/generate" || r.URL.Path == "/api/chat" {
			forbiddenCalls.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	skillsDir := t.TempDir()
	if err := os.WriteFile(skillsDir+"/SKILL.md", []byte("---\nname: fixture\ndescription: fixture skill\n---\nUse fixture skill.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := &config.Config{BrainProvider: "grok", VoiceWorkerProvider: "grok", VoiceWorkerModel: "mutable", OllamaHost: "http://127.0.0.1:9", OllamaModel: "mutable-live", OllamaEmbeddingModel: "cloud-embedding", SkillsEnabled: true, SkillsDir: skillsDir, PromptsDir: t.TempDir(), WorkDir: t.TempDir()}
	worker, err := NewPlannedWorker(context.Background(), base, localPlan(srv.URL, "pinned:7b"))
	if err != nil {
		t.Fatalf("NewPlannedWorker: %v", err)
	}
	if _, ok := worker.(*OllamaBrain); !ok {
		t.Fatalf("worker type = %T, want *OllamaBrain", worker)
	}
	if shownModel != "pinned:7b" {
		t.Fatalf("show model = %q, want pinned:7b", shownModel)
	}
	if got := forbiddenCalls.Load(); got != 0 {
		t.Fatalf("constructor made %d generation/embedding calls", got)
	}
}

func TestNewOllamaContextCancellationStopsProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newOllama(ctx, &config.Config{OllamaHost: "http://127.0.0.1:1", OllamaModel: "m"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func localPlan(endpoint, model string) *config.WorkerPlan {
	return &config.WorkerPlan{Assignment: config.WorkerAssignment{Provider: "ollama", Model: model, Billing: config.WorkerBillingLocal, EndpointRef: strptr(endpoint)}}
}

func strptr(s string) *string { return &s }

var _ config.WorkerCapabilityResolver = LocalWorkerCapabilities{}

func ExampleLocalWorkerCapabilities() {
	err := (LocalWorkerCapabilities{}).ValidateWorkerAssignment(config.WorkerAssignment{Provider: "grok", Model: "m", Billing: config.WorkerBillingLocal})
	fmt.Println(err != nil)
	// Output: true
}
