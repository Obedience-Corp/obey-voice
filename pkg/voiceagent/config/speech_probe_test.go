package config

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSpeechListener answers one hello per connection with reply, or holds the
// connection open when reply is empty.
func fakeSpeechListener(t *testing.T, token, reply string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				var hello SpeechHello
				if err := json.Unmarshal([]byte(line), &hello); err != nil ||
					hello.Type != "hello" || hello.Protocol != SpeechProtocol || hello.Token != token {
					fmt.Fprint(conn, `{"type":"error","message":"unauthorized"}`+"\n")
					return
				}
				if reply == "" {
					time.Sleep(5 * time.Second)
					return
				}
				fmt.Fprint(conn, reply+"\n")
			}()
		}
	}()
	return "http://" + listener.Addr().String()
}

const readySpeechLine = `{"type":"ready","protocol":"obey-speech/v1","sample_rate":24000,"pcm_format":"f32le","streaming":false,"tier":"0.6b"}`

func probeConfig(url, token string) *Config {
	return &Config{
		TTSProvider:     "qwen3-tts",
		TTSPlace:        TTSPlaceLAN,
		TTSWorkerURL:    url,
		TTSWorkerToken:  token,
		TTSLanTimeoutMS: 500,
	}
}

func TestSpeechHelloLineIsOneFrame(t *testing.T) {
	line, err := SpeechHelloLine("s3cret")
	if err != nil {
		t.Fatalf("SpeechHelloLine: %v", err)
	}
	want := `{"type":"hello","protocol":"obey-speech/v1","token":"s3cret"}` + "\n"
	if string(line) != want {
		t.Fatalf("hello=%q want %q", line, want)
	}
}

func TestSpeechDialTargetOf(t *testing.T) {
	for _, tc := range []struct {
		name    string
		url     string
		wantErr bool
		want    SpeechDialTarget
	}{
		{name: "plaintext off box", url: "http://192.168.1.1:7263", wantErr: true},
		{name: "no url", url: "", wantErr: true},
		{name: "default port", url: "https://studio", want: SpeechDialTarget{Address: "studio:7263", Host: "studio", ServerTLS: true}},
		{name: "explicit port", url: "https://studio:9000", want: SpeechDialTarget{Address: "studio:9000", Host: "studio", ServerTLS: true}},
		{name: "loopback plaintext", url: "http://127.0.0.1:7263", want: SpeechDialTarget{Address: "127.0.0.1:7263", Host: "127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SpeechDialTargetOf(probeConfig(tc.url, ""))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("target=%+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SpeechDialTargetOf: %v", err)
			}
			if got != tc.want {
				t.Fatalf("target=%+v want %+v", got, tc.want)
			}
		})
	}
}

func TestTTSWorkerTokenOfPrefersConfigThenEnv(t *testing.T) {
	t.Setenv(SpeechTokenEnv, "from-env")
	if got := TTSWorkerTokenOf(&Config{TTSWorkerToken: "from-config"}); got != "from-config" {
		t.Fatalf("token=%q", got)
	}
	if got := TTSWorkerTokenOf(&Config{}); got != "from-env" {
		t.Fatalf("token=%q want the environment override", got)
	}
}

func TestProbeSpeechWorker(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) *Config
		want  string
	}{
		{
			name: "unreachable",
			setup: func(t *testing.T) *Config {
				// Bound then closed: the dial is refused immediately.
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				addr := listener.Addr().String()
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
				return probeConfig("http://"+addr, "s3cret")
			},
			want: "dial speech worker",
		},
		{
			name: "bad token",
			setup: func(t *testing.T) *Config {
				return probeConfig(fakeSpeechListener(t, "s3cret", readySpeechLine), "wrong")
			},
			want: "unauthorized",
		},
		{
			name: "wrong protocol",
			setup: func(t *testing.T) *Config {
				reply := `{"type":"ready","protocol":"qwen3-tts-worker/v1","sample_rate":24000,"pcm_format":"f32le"}`
				return probeConfig(fakeSpeechListener(t, "s3cret", reply), "s3cret")
			},
			want: "protocol",
		},
		{
			name: "timeout",
			setup: func(t *testing.T) *Config {
				cfg := probeConfig(fakeSpeechListener(t, "s3cret", ""), "s3cret")
				cfg.TTSLanTimeoutMS = 100
				return cfg
			},
			want: "ready",
		},
		{
			name: "ok",
			setup: func(t *testing.T) *Config {
				return probeConfig(fakeSpeechListener(t, "s3cret", readySpeechLine), "s3cret")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.setup(t)
			result, err := ProbeSpeechWorker(context.Background(), cfg)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err=%v want %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("ProbeSpeechWorker: %v", err)
			}
			if result.Tier != "0.6b" || result.Streaming || result.RTT <= 0 {
				t.Fatalf("result=%+v", result)
			}
			if !strings.HasPrefix(result.Host, "127.0.0.1:") {
				t.Fatalf("host=%q", result.Host)
			}
		})
	}
}

func TestProbeSpeechWorkerHonorsCancellation(t *testing.T) {
	cfg := probeConfig(fakeSpeechListener(t, "s3cret", ""), "s3cret")
	cfg.TTSLanTimeoutMS = 5000

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeSpeechWorker(ctx, cfg); err == nil {
		t.Fatal("expected a cancelled probe to fail")
	} else if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err=%v want a cancellation", err)
	}
}
