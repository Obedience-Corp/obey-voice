package config

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPhononSetupRefreshesAnInstalledModelWithoutVoices(t *testing.T) {
	for _, voices := range [][]string{nil, {"default"}, {"", " DEFAULT "}} {
		t.Run(fmt.Sprint(voices), func(t *testing.T) {
			fake := &fakeOllama{version: "0.40.11", tags: []string{"phonon:latest"}, voices: voices}
			fake.pull = func(w http.ResponseWriter, r *http.Request) {
				fake.mu.Lock()
				fake.voices = []string{"default", "Zoey", "Easton"}
				fake.mu.Unlock()
				fmt.Fprintln(w, `{"status":"success"}`)
			}
			cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
			writeVoicesFile(t, cfg)
			if row := statusRows(t, cfg)[OpenAISpeechModelAssetID]; row.Installed || !strings.Contains(row.Detail, "refresh") {
				t.Fatalf("stale model status = %+v", row)
			}
			if err := installTTS(context.Background(), cfg, &tickLog{}); err != nil {
				t.Fatal(err)
			}
			if row := statusRows(t, cfg)[OpenAISpeechModelAssetID]; !row.Installed {
				t.Fatalf("refreshed model status = %+v", row)
			}
			if err := installTTS(context.Background(), cfg, &tickLog{}); err != nil {
				t.Fatal(err)
			}
			if n := len(fake.pullBodies()); n != 1 {
				t.Fatalf("pulled %d times; second setup should skip the ready model", n)
			}
		})
	}
}

func TestPhononSetupRefusesFalseSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, version, want string
		status              int
		pulls               int
	}{
		{"old runtime", "0.40.9", "0.40.11", 0, 0},
		{"show failed", "0.40.11", "could not check", http.StatusInternalServerError, 0},
		{"auth rejected", "0.40.11", "403", http.StatusForbidden, 0},
		{"still empty after refresh", "0.40.11", "still lists no named voices", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeOllama{version: tc.version, tags: []string{"phonon:latest"}, showStatus: tc.status}
			cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
			log := &tickLog{}
			err := installTTS(context.Background(), cfg, log)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(fake.pullBodies()) != tc.pulls {
				t.Fatalf("error = %v, pulls = %v", err, fake.pullBodies())
			}
			for _, tick := range log.ticks {
				if tick.Phase == assetPhaseDone || tick.Phase == assetPhaseSkipped {
					t.Fatalf("reported completion before verifying voices: %+v", tick)
				}
			}
		})
	}
}

func TestPhononRefreshDoesNotAffectConversationOrCustomModels(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		req         AssetRequest
	}{
		{"conversation", "phonon", AssetRequest{NeedTTS: true}},
		{"custom", "my-phonon", installRequest},
		{"pinned", "phonon:custom", installRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeOllama{version: "0.40.9", tags: []string{tc.model}}
			cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
			cfg.OpenAISpeechModel = tc.model
			writeVoicesFile(t, cfg)
			if err := EnsureRuntimeAssetsProgress(context.Background(), cfg, tc.req, nil); err != nil {
				t.Fatal(err)
			}
			if n := len(fake.pullBodies()); n != 0 {
				t.Fatalf("unexpected pull: %d", n)
			}
		})
	}
}

func TestPhononRefreshVerificationFailureDoesNotRepull(t *testing.T) {
	fake := &fakeOllama{version: "0.40.11", tags: []string{"phonon:latest"}}
	fake.pull = func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		fake.showStatus = http.StatusServiceUnavailable
		fake.mu.Unlock()
		fmt.Fprintln(w, `{"status":"success"}`)
	}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	err := installTTS(context.Background(), cfg, &tickLog{})
	if err == nil || !strings.Contains(err.Error(), "verify Phonon's voices") || len(fake.pullBodies()) != 1 {
		t.Fatalf("error = %v, pulls = %v", err, fake.pullBodies())
	}
}

func TestPhononRefreshCancellationBeforeVerification(t *testing.T) {
	fake := &fakeOllama{version: "0.40.11", tags: []string{"phonon:latest"}, pull: pullStream(
		`{"status":"writing manifest"}`, `{"status":"success"}`,
	)}
	cfg := openAISpeechCfg(t, fake.start(t).URL+"/v1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &tickLog{}
	err := EnsureRuntimeAssetsProgress(ctx, cfg, installRequest, func(p AssetProgress) {
		log.add(p)
		if p.Phase == assetPhaseInstall {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	for _, tick := range log.ticks {
		if tick.Phase == assetPhaseDone {
			t.Fatalf("canceled setup announced success: %+v", tick)
		}
	}
}
