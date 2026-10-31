//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audiojob"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/openaispeech"
	"github.com/spf13/cobra"
)

func newAudioJobCmd() *cobra.Command {
	var configPath, requestPath string
	var jsonl bool
	cmd := &cobra.Command{Use: "audio-job", Short: "Create finite audio or text findings from an explicit isolated request", Args: cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			if configPath == "" || requestPath == "" || !jsonl {
				return fmt.Errorf("audio-job requires --audio-config, --request and --jsonl")
			}
			var c audiojob.Config
			var r audiojob.Request
			if e := readAudioDocument(configPath, &c); e != nil {
				return e
			}
			if e := readAudioDocument(requestPath, &r); e != nil {
				return e
			}
			if e := r.Validate(c); e != nil {
				return e
			}
			ctx, cancel := signalContext()
			defer cancel()
			ctx, cancel = context.WithTimeout(ctx, time.Duration(r.Limits.TimeoutSeconds)*time.Second)
			defer cancel()
			enc := json.NewEncoder(cmd.OutOrStdout())
			if err := r.Preflight(ctx, c); err != nil {
				return err
			}
			if c.Managed != nil {
				runtime, provenance, err := audiojob.StartManaged(ctx, c)
				if err != nil {
					return err
				}
				defer runtime.Close()
				if r.Protocol == audiojob.TextProtocol {
					_, err = audiojob.RunText(ctx, c, r, runtime, provenance, func(event audiojob.Event) error { return enc.Encode(event) })
					return err
				}
				var speech audiojob.Synthesizer = runtime
				if r.Protocol == audiojob.DubbingProtocol {
					speech, err = runtime.SpeechInLanguage(r.TargetLanguage)
					if err != nil {
						return err
					}
				}
				_, err = audiojob.Run(ctx, c, r, speech, provenance, func(event audiojob.Event) error { return enc.Encode(event) })
				return err
			}
			provenance, e := audioRuntimeProvenance(ctx, c.Runtime)
			if e != nil {
				return e
			}
			voice := c.Runtime.Voice
			if voice == "" {
				voice = "default"
			}
			cfg := &config.Config{TTSProvider: "openai-speech", OpenAISpeechRuntime: "external", OpenAISpeechURL: c.Runtime.Origin() + "/v1", OpenAISpeechModel: c.Runtime.Model, OpenAISpeechVoice: voice, OpenAISpeechTimeout: r.Limits.TimeoutSeconds, ModelsDir: c.StorageRoot}
			speech, closeSpeech, e := openaispeech.NewWithContext(ctx, cfg, localAudioHTTPClient())
			if e != nil {
				return e
			}
			defer closeSpeech()
			_, e = audiojob.Run(ctx, c, r, speech, provenance, func(event audiojob.Event) error { return enc.Encode(event) })
			return e
		}}
	cmd.Flags().StringVar(&configPath, "audio-config", "", "Absolute isolated obey-voice.audio-config/v1 JSON")
	cmd.Flags().StringVar(&requestPath, "request", "", "Absolute v1 narration, v2 audio-text or v3 reviewed dubbing request JSON")
	cmd.Flags().BoolVar(&jsonl, "jsonl", false, "Write bounded job events as JSONL")
	return cmd
}
func readAudioDocument(path string, out any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return audiojob.Decode(f, out)
}
func audioRuntimeProvenance(ctx context.Context, r audiojob.Runtime) (audiojob.Provenance, error) {
	get := func(path string, out any) error {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, r.Origin()+path, nil)
		if e != nil {
			return e
		}
		resp, e := localAudioHTTPClient().Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("runtime metadata %s: HTTP %d", path, resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	var version struct {
		Version string `json:"version"`
	}
	if e := get("/api/version", &version); e != nil {
		return audiojob.Provenance{}, e
	}
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Model  string `json:"model"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if e := get("/api/tags", &tags); e != nil {
		return audiojob.Provenance{}, e
	}
	for _, m := range tags.Models {
		if m.Name == r.Model || m.Model == r.Model {
			return audiojob.Provenance{RuntimeVersion: version.Version, ModelDigest: m.Digest}, nil
		}
	}
	return audiojob.Provenance{}, fmt.Errorf("model %q is not installed in selected runtime; no download attempted", r.Model)
}

// Local jobs never follow a redirect to a remote service or send data beyond
// the endpoint the isolated config explicitly selected.
func localAudioHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
