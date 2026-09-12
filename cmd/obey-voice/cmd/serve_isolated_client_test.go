//go:build !integration

package cmd

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestVS0001IsolatedClient records the real TLS stream. With no control it
// watches until observer-stop appears. With one explicit text_input or
// set_persona control it returns after the matching response/ack. No token or
// credential payload is copied to the retained stream artifact.
func TestVS0001IsolatedClient(t *testing.T) {
	root := os.Getenv("VS0001_SERVE_CLIENT_ROOT")
	if root == "" {
		t.Skip("set VS0001_SERVE_CLIENT_ROOT for a live fixture")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("client root must be absolute")
	}
	raw, err := os.ReadFile(filepath.Join(root, "serve", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var creds isolatedServeReady
	if err := json.Unmarshal(raw, &creds); err != nil {
		t.Fatal(err)
	}
	cert, err := os.ReadFile(filepath.Join(root, "serve", "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cert) {
		t.Fatal("fixture certificate is invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool,
		VerifyConnection: func(state tls.ConnectionState) error {
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != creds.Fingerprint {
				return fmt.Errorf("fixture certificate fingerprint mismatch")
			}
			return nil
		}}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(creds.URL, "https://", "wss://", 1)+"/v1/stream", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + creds.Token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	name := os.Getenv("VS0001_SERVE_CLIENT_NAME")
	if name == "" {
		name = "observer"
	}
	if filepath.Base(name) != name || name == "." {
		t.Fatal("client name must be one filename component")
	}
	log, err := os.OpenFile(filepath.Join(root, name+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	control := os.Getenv("VS0001_SERVE_CLIENT_CONTROL")
	expectedError := os.Getenv("VS0001_SERVE_CLIENT_EXPECT_ERROR")
	var request struct {
		Type string `json:"type"`
	}
	if control != "" {
		if err := json.Unmarshal([]byte(control), &request); err != nil {
			t.Fatal(err)
		}
		if request.Type != "set_persona" && request.Type != "text_input" {
			t.Fatal("unsupported fixture control")
		}
	}
	if control == "" {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := os.Stat(filepath.Join(root, "observer-stop")); err == nil {
						cancel()
						return
					}
				}
			}
		}()
	}
	first := true
	for {
		kind, data, err := ws.Read(ctx)
		if err != nil {
			if ctx.Err() != nil && control == "" {
				return
			}
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Stage   string `json:"stage"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		if _, err := log.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
		if err := log.Sync(); err != nil {
			t.Fatal(err)
		}
		if first {
			if event.Type != "session_snapshot" {
				t.Fatalf("first message = %s", event.Type)
			}
			first = false
			if control != "" {
				if err := ws.Write(ctx, websocket.MessageText, []byte(control)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if event.Type == "error" {
			t.Logf("stream error stage=%s: %s", event.Stage, event.Message)
			if expectedError != "" && strings.Contains(event.Message, expectedError) {
				return
			}
		}
		if control != "" && ((request.Type == "set_persona" && event.Type == "set_persona_ack") || (request.Type == "text_input" && event.Type == "response_ready")) {
			if expectedError != "" {
				t.Fatal("response succeeded instead of expected refusal")
			}
			return
		}
	}
}
