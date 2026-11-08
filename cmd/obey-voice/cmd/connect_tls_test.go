//go:build !integration

package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/coder/websocket"
)

// Run the actual command in a separate process with disposable configuration.
// This also exercises Cobra flag wiring and runConnect's credential boundary.
func TestConnectCommandChild(t *testing.T) {
	root := os.Getenv("CONNECT_TEST_ROOT")
	if root == "" {
		return
	}
	config.SetConfigDirForTest(t, root)
	for i, arg := range os.Args {
		if arg == "--" {
			rootCmd.SetArgs(os.Args[i+1:])
			rootCmd.SilenceUsage = true
			rootCmd.SilenceErrors = true
			if err := rootCmd.Execute(); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing child command arguments")
}

func connectTestCertificate(t *testing.T, wrongHost, expired bool) (tls.Certificate, []byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if wrongHost {
		template.DNSNames = []string{"wrong.example"}
	} else {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	if expired {
		template.NotAfter = time.Now().Add(-time.Minute)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return pair, certPEM, hex.EncodeToString(sum[:])
}

func TestConnectCommandAuthenticatesBeforeSendingToken(t *testing.T) {
	for _, tc := range []struct {
		name                                                            string
		trusted, pin, badPin, malformedPin, wrongHost, expired, success bool
	}{
		{name: "untrusted"},
		{name: "pin alone does not trust", pin: true},
		{name: "explicit certificate", trusted: true, success: true},
		{name: "certificate and uppercase pin", trusted: true, pin: true, success: true},
		{name: "mismatched pin", trusted: true, badPin: true},
		{name: "malformed pin", trusted: true, malformedPin: true},
		{name: "wrong hostname", trusted: true, pin: true, wrongHost: true},
		{name: "expired", trusted: true, pin: true, expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, certPEM, fingerprint := connectTestCertificate(t, tc.wrongHost, tc.expired)
			var requests atomic.Int32
			messages := make(chan string, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-connect-token" {
					t.Error("missing synthetic credential")
				}
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = ws.CloseNow() }()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				_, raw, err := ws.Read(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				var message struct{ Type, Text string }
				if err := json.Unmarshal(raw, &message); err != nil {
					t.Error(err)
					return
				}
				messages <- message.Type + ":" + message.Text
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			root := t.TempDir()
			caFile := filepath.Join(root, "server.pem")
			if err := os.WriteFile(caFile, certPEM, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"-test.run=^TestConnectCommandChild$", "--", "connect", strings.TrimPrefix(server.URL, "https://"), "--token", "synthetic-connect-token"}
			if tc.trusted {
				args = append(args, "--ca-cert", caFile)
			}
			if tc.pin {
				args = append(args, "--fingerprint", strings.ToUpper(fingerprint))
			}
			if tc.badPin {
				args = append(args, "--fingerprint", strings.Repeat("0", 64))
			}
			if tc.malformedPin {
				args = append(args, "--fingerprint", "invalid")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], args...)
			child.Env = append(os.Environ(), "CONNECT_TEST_ROOT="+root)
			child.Stdin = strings.NewReader("hello\n/quit\n")
			output, err := child.CombinedOutput()
			if tc.success {
				if err != nil {
					t.Fatalf("command failed: %v\n%s", err, output)
				}
				select {
				case message := <-messages:
					if message != "text_input:hello" {
						t.Fatalf("message: %s", message)
					}
				case <-ctx.Done():
					t.Fatal("no text turn received")
				}
				if requests.Load() != 1 || !strings.Contains(string(output), "Connected:") {
					t.Fatalf("command did not connect: %s", output)
				}
			} else {
				if err == nil {
					t.Fatal("untrusted connection succeeded")
				}
				if requests.Load() != 0 {
					t.Fatal("credentials reached an unauthenticated server")
				}
			}
			if strings.Contains(string(output), "synthetic-connect-token") {
				t.Fatal("command output leaked token")
			}
		})
	}
}

func TestConnectTLSRejectsInvalidTrustInput(t *testing.T) {
	if _, err := connectTLSConfig(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("accepted missing CA file")
	}
	file := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(file, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := connectTLSConfig(file, ""); err == nil {
		t.Fatal("accepted malformed CA file")
	}
}
