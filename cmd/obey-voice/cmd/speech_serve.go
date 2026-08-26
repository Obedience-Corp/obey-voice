//go:build !integration

package cmd

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/internal/speechserve"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

var (
	speechServeBind      string
	speechServeTokenFile string
	speechServeTLSCert   string
	speechServeTLSKey    string
	speechServeTailscale bool
	speechServeLogFile   string
)

// speechServeCmd shares this computer's Qwen worker with one conversation host
// on the network. It is never started automatically: a single-machine install
// runs the worker in-process and must never bind 7263.
var speechServeCmd = &cobra.Command{
	Use:   "speech-serve",
	Short: "Speak for another computer's Obey Voice over obey-speech/v1",
	Long: `Run this computer's Qwen3-TTS worker as a speech service for one
conversation host on your network.

Only useful when the GPU is a different computer than the one you talk to. A
machine that runs both needs nothing here — set tts_place: local and the worker
runs in-process.

The worker starts once and stays warm. Clients connect, authenticate with the
shared token, and disconnect without ever costing the model a reload.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		absPathFlag(&speechServeTLSCert)
		absPathFlag(&speechServeTLSKey)
		return runSpeechServe(cmd, cfg)
	},
}

func init() {
	speechServeCmd.Flags().StringVar(&speechServeBind, "bind", speechserve.DefaultBind, "Address to listen on (host:port). Anything but loopback requires TLS")
	speechServeCmd.Flags().StringVar(&speechServeTokenFile, "token-file", "", "Read the shared token from this file instead of the speech credentials directory")
	speechServeCmd.Flags().StringVar(&speechServeTLSCert, "tls-cert", "", "TLS certificate PEM (e.g. from tailscale cert)")
	speechServeCmd.Flags().StringVar(&speechServeTLSKey, "tls-key", "", "TLS private key PEM (e.g. from tailscale cert)")
	speechServeCmd.Flags().BoolVar(&speechServeTailscale, "tailscale", false, "Bind this node's tailnet address and use a tailscale cert")
	speechServeCmd.Flags().StringVar(&speechServeLogFile, "log-file", "", "Append output to this file (created 0600) instead of the console")
	rootCmd.AddCommand(speechServeCmd)
}

func runSpeechServe(cmd *cobra.Command, cfg *config.Config) error {
	ctx, cancel := signalContext()
	defer cancel()

	out := cmd.OutOrStdout()
	if path := strings.TrimSpace(speechServeLogFile); path != "" {
		file, err := openServeLogFile(path)
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}

	if err := applySpeechServeTailscale(cmd); err != nil {
		return err
	}
	tlsConfig, err := speechServeTLS()
	if err != nil {
		return err
	}
	token, minted, err := resolveSpeechToken()
	if err != nil {
		return err
	}
	spec, err := resolveSpeechChild(cfg)
	if err != nil {
		return err
	}

	server, err := speechserve.New(speechserve.Options{
		Bind:      speechServeBind,
		Token:     token,
		TLSConfig: tlsConfig,
		Child:     spec,
		Tier:      qwen.NormalizeModelTier(cfg.QwenTTSModelTier),
		Logf:      func(format string, args ...any) { fmt.Fprintf(out, "  "+format+"\n", args...) },
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\n  Obey Voice speech worker (%s)\n\n", spec.ModelDir)
	if minted {
		// Printed once, at creation, exactly like the serve bearer token.
		fmt.Fprintf(out, "  Token (share with the conversation host, once): %s\n", token)
	}
	fmt.Fprintf(out, "  Loading the model, then listening on %s\n\n", speechServeBind)
	if err := server.Start(ctx); err != nil {
		return err
	}
	defer server.Close()
	printSpeechServeHost(out, server.Addr(), tlsConfig != nil)
	return server.Serve(ctx)
}

// printSpeechServeHost tells the operator what to put on the other computer.
// The token is deliberately absent: this line is meant to be readable over a
// shoulder and pasted into an issue.
func printSpeechServeHost(out io.Writer, addr string, secure bool) {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	fmt.Fprintf(out, "  Listening on %s\n", addr)
	fmt.Fprintln(out, "  On the computer you talk to:")
	fmt.Fprintln(out, "    obey-voice config set tts_provider qwen3-tts")
	fmt.Fprintln(out, "    obey-voice config set tts_place lan")
	fmt.Fprintf(out, "    obey-voice config set tts_worker_url %s://%s\n", scheme, addr)
	fmt.Fprintln(out, "    obey-voice config set tts_worker_token <the token above>")
	fmt.Fprintln(out)
}

// applySpeechServeTailscale binds the tailnet address and mints a trusted cert
// there. It reuses serve's cert pipeline rather than inventing a second one.
func applySpeechServeTailscale(cmd *cobra.Command) error {
	if !speechServeTailscale {
		return nil
	}
	id, err := discoverTailscale()
	if err != nil {
		return err
	}
	if !cmd.Flags().Changed("bind") {
		speechServeBind = net.JoinHostPort(id.IPv4, config.SpeechDefaultPort)
	}
	if speechServeTLSCert == "" && speechServeTLSKey == "" {
		tlsDir := filepath.Join(config.ConfigDir(), "speech", "tls")
		cert, key, err := ensureTailscaleCert(tlsDir, id.DNSName)
		if err != nil {
			return fmt.Errorf("speech-serve --tailscale needs a certificate: %w", err)
		}
		speechServeTLSCert, speechServeTLSKey = cert, key
	}
	return nil
}

func speechServeTLS() (*tls.Config, error) {
	cert, key := strings.TrimSpace(speechServeTLSCert), strings.TrimSpace(speechServeTLSKey)
	if cert == "" && key == "" {
		return nil, nil
	}
	if cert == "" || key == "" {
		return nil, fmt.Errorf("both --tls-cert and --tls-key are required")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
}

// resolveSpeechToken prefers an explicit file, then the environment, then the
// speech credentials directory — minting there on first run.
func resolveSpeechToken() (token string, minted bool, err error) {
	if path := strings.TrimSpace(speechServeTokenFile); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, fmt.Errorf("read --token-file: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return "", false, fmt.Errorf("--token-file %s is empty", path)
		}
		return token, false, nil
	}
	if env := strings.TrimSpace(os.Getenv(config.SpeechTokenEnv)); env != "" {
		return env, false, nil
	}
	return speechserve.LoadOrMintToken(filepath.Join(config.ConfigDir(), "speech"))
}

// resolveSpeechChild finds the worker this daemon will own: an explicitly
// configured native worker, else the installed native package.
func resolveSpeechChild(cfg *config.Config) (speechserve.ChildSpec, error) {
	tier := qwen.NormalizeModelTier(cfg.QwenTTSModelTier)
	binary := strings.TrimSpace(cfg.QwenTTSBinary)
	model := strings.TrimSpace(cfg.QwenTTSModel)
	if binary != "" && model != "" {
		return speechserve.ChildSpec{Binary: binary, ModelDir: model, Tier: tier}, nil
	}
	native := qwen.InspectNative(config.ModelsDirFrom(cfg), tier)
	if !native.Installed {
		return speechserve.ChildSpec{}, fmt.Errorf("speech-serve needs the native Qwen package: run 'obey-voice models ensure --tts' on this computer")
	}
	return speechserve.ChildSpec{Binary: native.Worker, ModelDir: native.ModelDir, Tier: tier}, nil
}
