package config

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// The obey-speech/v1 wire contract (design spec/30). config owns these because
// both the host's LAN client and the doctor probe speak them, and config sits
// below tts in the import graph.
const (
	// SpeechProtocol is the protocol string a TCP speech session must see in
	// hello and in the server's ready. The local child worker never sees it.
	SpeechProtocol = "obey-speech/v1"
	// SpeechDefaultPort is speech-serve's port when the URL omits one.
	SpeechDefaultPort = "7263"
	// SpeechTokenEnv overrides tts_worker_token.
	SpeechTokenEnv = "OBEY_SPEECH_TOKEN"
)

// SpeechHello is the first line a client writes. Extra fields are rejected by
// the server as an unknown handshake, so this struct is the whole frame.
type SpeechHello struct {
	Type     string `json:"type"`
	Protocol string `json:"protocol"`
	Token    string `json:"token"`
}

// SpeechHelloLine renders the hello frame, newline included. One definition
// serves the client and the probe so they cannot drift.
func SpeechHelloLine(token string) ([]byte, error) {
	data, err := json.Marshal(SpeechHello{Type: "hello", Protocol: SpeechProtocol, Token: token})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// SpeechDialTarget is where and how to reach the configured speech worker.
type SpeechDialTarget struct {
	Address   string // host:port
	Host      string
	ServerTLS bool
}

// SpeechDialTargetOf resolves tts_worker_url. It assumes ValidateTTSPlace has
// already refused plaintext off-loopback and credentials in the URL.
func SpeechDialTargetOf(cfg *Config) (SpeechDialTarget, error) {
	if cfg == nil {
		return SpeechDialTarget{}, errors.New("nil config")
	}
	if err := ValidateTTSPlace(cfg); err != nil {
		return SpeechDialTarget{}, err
	}
	raw := strings.TrimSpace(cfg.TTSWorkerURL)
	parsed, err := url.Parse(raw)
	if err != nil {
		return SpeechDialTarget{}, fmt.Errorf("tts_worker_url %q is not a URL: %w", raw, err)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = SpeechDefaultPort
	}
	return SpeechDialTarget{
		Address:   net.JoinHostPort(host, port),
		Host:      host,
		ServerTLS: strings.EqualFold(parsed.Scheme, "https"),
	}, nil
}

// TTSWorkerTokenOf returns the speech token: the config key, else the
// environment override. It is never read from the URL.
func TTSWorkerTokenOf(cfg *Config) string {
	if cfg != nil {
		if token := strings.TrimSpace(cfg.TTSWorkerToken); token != "" {
			return token
		}
	}
	return strings.TrimSpace(os.Getenv(SpeechTokenEnv))
}

// DialSpeechWorker opens a connection to the configured speech worker and
// bounds it with tts_lan_timeout_ms. It does not write the hello frame; the
// caller owns the handshake because the client and the probe read the ready
// line differently.
func DialSpeechWorker(ctx context.Context, cfg *Config) (net.Conn, SpeechDialTarget, error) {
	target, err := SpeechDialTargetOf(cfg)
	if err != nil {
		return nil, SpeechDialTarget{}, err
	}
	timeout := time.Duration(TTSLanTimeoutMSOf(cfg)) * time.Millisecond
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var conn net.Conn
	if target.ServerTLS {
		dialer := &tls.Dialer{Config: &tls.Config{ServerName: target.Host, MinVersion: tls.VersionTLS12}}
		conn, err = dialer.DialContext(dialCtx, "tcp", target.Address)
	} else {
		dialer := &net.Dialer{}
		conn, err = dialer.DialContext(dialCtx, "tcp", target.Address)
	}
	if err != nil {
		return nil, target, fmt.Errorf("dial speech worker %s: %w", target.Address, err)
	}
	return conn, target, nil
}

// SpeechProbeResult is what a successful hello/ready exchange reports.
type SpeechProbeResult struct {
	Host      string        `json:"host"`
	RTT       time.Duration `json:"rtt"`
	Streaming bool          `json:"streaming"`
	Tier      string        `json:"tier,omitempty"`
}

// SpeechProbe dials, says hello, reads ready and closes. Injected so doctor
// tests never open a socket.
type SpeechProbe func(ctx context.Context, cfg *Config) (SpeechProbeResult, error)

// ProbeSpeechWorker performs one hello/ready exchange and closes the socket
// without sending shutdown: a probe must never cost the worker its warm model.
// It retries once, as the timeout bounds the dial and handshake only.
func ProbeSpeechWorker(ctx context.Context, cfg *Config) (SpeechProbeResult, error) {
	result, err := probeSpeechWorkerOnce(ctx, cfg)
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	retry, retryErr := probeSpeechWorkerOnce(ctx, cfg)
	if retryErr != nil {
		return SpeechProbeResult{}, err
	}
	return retry, nil
}

func probeSpeechWorkerOnce(ctx context.Context, cfg *Config) (SpeechProbeResult, error) {
	started := time.Now()
	conn, target, err := DialSpeechWorker(ctx, cfg)
	if err != nil {
		return SpeechProbeResult{}, err
	}
	// No shutdown line: closing is unambiguous and keeps the probe incapable of
	// affecting worker lifetime (spec/30 §4).
	defer conn.Close()

	timeout := time.Duration(TTSLanTimeoutMSOf(cfg)) * time.Millisecond
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return SpeechProbeResult{}, err
	}

	hello, err := SpeechHelloLine(TTSWorkerTokenOf(cfg))
	if err != nil {
		return SpeechProbeResult{}, err
	}
	if _, err := conn.Write(hello); err != nil {
		return SpeechProbeResult{}, fmt.Errorf("speech worker hello: %w", err)
	}
	ready, err := readSpeechReadyLine(conn)
	if err != nil {
		return SpeechProbeResult{}, err
	}
	return SpeechProbeResult{
		Host:      target.Address,
		RTT:       time.Since(started),
		Streaming: ready.Streaming,
		Tier:      strings.TrimSpace(ready.Tier),
	}, nil
}

// speechReadyLine is the subset of the ready frame the probe reports. Extra
// fields are ignored so a newer speech-serve does not fail an older host.
type speechReadyLine struct {
	Type      string `json:"type"`
	Protocol  string `json:"protocol"`
	Message   string `json:"message"`
	Streaming bool   `json:"streaming"`
	Tier      string `json:"tier"`
}

func readSpeechReadyLine(conn net.Conn) (speechReadyLine, error) {
	line, err := readLimitedLine(conn, maxSpeechLine)
	if err != nil {
		return speechReadyLine{}, fmt.Errorf("speech worker ready: %w", err)
	}
	var ready speechReadyLine
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		return speechReadyLine{}, fmt.Errorf("speech worker ready json: %w (%q)", err, strings.TrimSpace(line))
	}
	switch {
	case ready.Type == "error":
		message := strings.TrimSpace(ready.Message)
		if message == "" {
			message = "rejected"
		}
		return speechReadyLine{}, fmt.Errorf("speech worker refused the handshake: %s", message)
	case ready.Type != "ready":
		return speechReadyLine{}, fmt.Errorf("speech worker handshake failed: type=%s", ready.Type)
	case ready.Protocol != SpeechProtocol:
		return speechReadyLine{}, fmt.Errorf("speech worker protocol %q unsupported; want %q", ready.Protocol, SpeechProtocol)
	}
	return ready, nil
}

// maxSpeechLine caps a control line so a hostile or broken peer cannot make the
// host allocate without bound before the deadline fires.
const maxSpeechLine = 64 << 10

func readLimitedLine(conn net.Conn, limit int) (string, error) {
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for len(buf) < limit {
		n, err := conn.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return string(buf), nil
			}
			buf = append(buf, one[0])
			continue
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("control line exceeded %d bytes", limit)
}
