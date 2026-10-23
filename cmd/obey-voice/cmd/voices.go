//go:build !integration

package cmd

import (
	"context"
	"fmt"
	ttsproviders "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts/providers"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// voiceStackDeps are the heavy dependencies of the voices verbs: constructing
// a provider pages a model into memory, and ensuring assets can download
// hundreds of megabytes. Tests swap them for fakes so the JSON contract and
// the WAV path are exercised without a model on disk.
type voiceStackDeps struct {
	newProvider   func(*config.Config) (tts.Provider, func(), error)
	missingAssets func(context.Context, *config.Config) []string
	ensureAssets  func(context.Context, *config.Config, config.AssetRequest, func(string, float64)) error
}

var voiceStack = voiceStackDeps{
	newProvider:   ttsproviders.NewProvider,
	missingAssets: missingTTSAssets,
	ensureAssets:  config.EnsureRuntimeAssets,
}

// voiceJSON is one row of `voices --json`.
type voiceJSON struct {
	Name         string `json:"name"`
	FriendlyName string `json:"friendly_name"`
	Gender       string `json:"gender"`
	Locale       string `json:"locale"`
	// Kind is where the voice came from, where the provider distinguishes:
	// audio8 reports "recording" or "synthetic" for a registered voice.
	// Omitted for the built-in catalogs rather than emitted empty.
	Kind string `json:"kind,omitempty"`
	// Age and Description come from a descriptive catalog (Phonon's
	// voices.jsonl); omitted when the catalog carries only names.
	Age         string `json:"age,omitempty"`
	Description string `json:"description,omitempty"`
	Active      bool   `json:"active"`
}

// voicesJSON is the body of `voices --json`: the catalog of one provider,
// with the voice that provider is currently configured to speak marked.
type voicesJSON struct {
	Provider    string      `json:"provider"`
	Source      string      `json:"source"` // "provider" (constructed and queried live) | "static" (compile-time catalog, no provider construction)
	ActiveVoice string      `json:"active_voice"`
	Voices      []voiceJSON `json:"voices"`
	// Languages lists qwen3-tts's supported synthesis languages (independent
	// of which voice is selected); omitted for every other provider.
	Languages []string `json:"languages,omitempty"`
	// Catalog names where a live provider's catalog came from when it has a
	// choice: openai-speech reports "server", "ollama" or "configured".
	Catalog string `json:"catalog,omitempty"`
	// Notice explains a catalog smaller than it could be, in words the user
	// can act on (an Ollama release without voice lists). Omitted when empty.
	Notice string `json:"notice,omitempty"`
}

func newVoicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "voices",
		Short: "List available TTS voices",
		Long: `List the voices of a TTS provider.

--provider lists another provider's catalog without changing any config: the
provider is built from a copy of the loaded config, never the live one.

--json never downloads. When the provider's assets are missing it reports
assets_missing and names the command that installs them. qwen3-tts answers
from the compile-time preset catalog (source "static"), because constructing
that provider pages a multi-gigabyte model in to report names this binary
already knows; --live asks the installed native package instead.`,
		RunE: runVoices,
	}
	cmd.Flags().StringP("locale", "l", "", "Filter by locale (e.g. en-US)")
	cmd.Flags().StringP("gender", "g", "", "Filter by gender (male/female)")
	cmd.Flags().StringP("provider", "p", "", "List this provider's catalog instead of the configured one")
	cmd.Flags().Bool("json", false, "Emit JSON")
	cmd.Flags().Bool("live", false, "Query the constructed provider (qwen3-tts: loads the model, ~10s, and reports presets shipped with the installed native package); no effect on providers that are already listed live")
	cmd.AddCommand(newVoicesPreviewCmd())
	cmd.AddCommand(newVoicesRegisterCmd())
	cmd.AddCommand(newVoicesRegisteredCmd())
	cmd.AddCommand(newVoicesRemoveCmd())
	return cmd
}

func runVoices(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	asJSON, _ := cmd.Flags().GetBool("json")
	if !asJSON {
		return runVoicesTable(cmd)
	}
	catalog, err := voicesCatalog(cmd)
	if err != nil {
		return emitJSONError(cmd, err)
	}
	return encodeJSON(cmd, catalog)
}

// isAudio8Provider reports whether a provider key selects the Audio8 provider.
func isAudio8Provider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), config.Audio8ProviderName)
}

func voicesCatalog(cmd *cobra.Command) (*voicesJSON, error) {
	cfg, err := voicesConfig(cmd)
	if err != nil {
		return nil, err
	}
	locale, _ := cmd.Flags().GetString("locale")
	gender, _ := cmd.Flags().GetString("gender")
	live, _ := cmd.Flags().GetBool("live")

	// Two providers can answer without a model. qwen3-tts's selectable voices
	// are a compile-time registry (presets plus shipped clones), not a property
	// of the loaded model; audio8's are the packaged reference plus the
	// voice.json files under models_dir. Constructing either provider pages
	// gigabytes in to report names that are already on disk, which is longer
	// than a voice picker's whole CLI budget. Static is therefore the default
	// answer for both. Presets the installed qwen3-tts native package ships
	// beyond the registry stay reachable through --live; for audio8 --live
	// asks the constructed provider, which reads the same store.
	if (isQwenTTS(cfg.TTSProvider) || isAudio8Provider(cfg.TTSProvider)) && !live {
		voices, err := tts.StaticVoicesFor(cfg, cfg.TTSProvider, locale, gender)
		if err != nil {
			return nil, codedError(codeInvalidProvider, "static voices: %v", err)
		}
		return buildVoicesJSON(cfg, voices, "static"), nil
	}

	if err := requireTTSAssets(cmd.Context(), cfg); err != nil {
		// A fresh machine can still browse qwen3-tts's model-native presets
		// before running `models ensure --tts`, even when --live asked for the
		// installed package. Every other provider keeps the original
		// refuse-on-missing contract: --json must never download, and a live
		// catalog it cannot safely list without a model on disk is not
		// silently swapped for something incomplete.
		if isQwenTTS(cfg.TTSProvider) || isAudio8Provider(cfg.TTSProvider) {
			if voices, staticErr := tts.StaticVoicesFor(cfg, cfg.TTSProvider, locale, gender); staticErr == nil && len(voices) > 0 {
				return buildVoicesJSON(cfg, voices, "static"), nil
			}
		}
		return nil, err
	}

	provider, cleanup, err := voiceStack.newProvider(cfg)
	if err != nil {
		return nil, codedError(codeInvalidProvider, "init TTS: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	out := buildVoicesJSON(cfg, provider.ListVoices(locale, gender), "provider")
	out.Catalog = tts.CatalogSourceOf(provider)
	out.Notice = tts.CatalogNoticeOf(provider)
	return out, nil
}

// buildVoicesJSON assembles the voices --json body from an already-filtered
// voice list and source ("provider" | "static"); Languages is populated for
// qwen3-tts regardless of source, since it does not vary with it.
func buildVoicesJSON(cfg *config.Config, voices []tts.Voice, source string) *voicesJSON {
	active := activeVoiceFor(cfg)
	out := &voicesJSON{Provider: cfg.TTSProvider, Source: source, ActiveVoice: active, Voices: []voiceJSON{}}
	for _, v := range voices {
		out.Voices = append(out.Voices, voiceJSON{
			Name:         v.Name,
			FriendlyName: v.FriendlyName,
			Gender:       v.Gender,
			Locale:       v.Locale,
			Kind:         v.Kind,
			Age:          v.Age,
			Description:  v.Description,
			Active:       strings.EqualFold(v.Name, active),
		})
	}
	if isQwenTTS(cfg.TTSProvider) {
		out.Languages = managedqwen.SupportedLanguages()
	}
	return out
}

// voicesConfig loads the config and applies --provider to a copy. Listing a
// catalog is a read: it must never leave the requested provider behind in
// config.yaml or in the process-wide viper state.
func voicesConfig(cmd *cobra.Command) (*config.Config, error) {
	loaded, err := config.Load()
	if err != nil {
		return nil, err
	}
	cfg := *loaded
	if override, _ := cmd.Flags().GetString("provider"); strings.TrimSpace(override) != "" {
		if err := validateTTSProvider(strings.TrimSpace(override)); err != nil {
			return nil, err
		}
		cfg.TTSProvider = strings.TrimSpace(override)
	}
	if strings.TrimSpace(cfg.TTSProvider) == "" {
		cfg.TTSProvider = "kokoro"
	}
	return &cfg, nil
}

// requireTTSAssets refuses rather than downloads. A voice picker opening or a
// Preview button being pressed must not start a hundreds-of-megabyte fetch the
// user never asked for; `obey-voice models ensure --tts` is the explicit path.
func requireTTSAssets(ctx context.Context, cfg *config.Config) error {
	missing := voiceStack.missingAssets(ctx, cfg)
	if len(missing) == 0 {
		return nil
	}
	return codedError(codeAssetsMissing,
		"%s assets are not installed (%s): run `obey-voice models ensure --tts`",
		cfg.TTSProvider, strings.Join(missing, ", "))
}

// missingTTSAssets names the not-yet-installed assets for cfg's TTS provider,
// reading the manifest offline. It mirrors `obey-voice models status --tts`,
// including the managed Qwen native package that lives outside the manifest
// and an openai-speech model Ollama has not pulled. Phonon's voices.jsonl is
// metadata and never counts.
func missingTTSAssets(ctx context.Context, cfg *config.Config) []string {
	manifest, err := config.ManifestFor(cfg, config.AssetRequest{NeedTTS: true})
	if err != nil {
		return []string{err.Error()}
	}
	modelsDir := config.ModelsDirFrom(cfg)
	var missing []string
	for _, s := range manifest.Status(modelsDir) {
		if !s.Installed {
			missing = append(missing, s.Name)
		}
	}
	if isQwenTTS(cfg.TTSProvider) && managedqwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
		if native := managedqwen.InspectNative(modelsDir, cfg.QwenTTSModelTier); !native.Installed {
			missing = append(missing, "Qwen3-TTS native package")
		}
	}
	if config.OpenAISpeechSelected(cfg) {
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(ctx, config.OpenAISpeechProbeTimeout)
		defer cancel()
		// A server that does not answer is not an install problem; building
		// the provider reports it.
		if t := config.ProbeOpenAISpeech(ctx, cfg); t.Ollama && !t.ModelListed {
			missing = append(missing, config.OpenAISpeechModelAssetName(t.Model))
		}
	}
	return missing
}

// runVoicesTable keeps the pre-existing human path byte-for-byte, including
// its download-on-demand behaviour: a terminal user asking for the list can
// answer a progress bar, a front end parsing --json cannot.
func runVoicesTable(cmd *cobra.Command) error {
	cfg, err := voicesConfig(cmd)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s %s\n\n", titleStyle.Render("Voices for:"), sectionStyle.Render(cfg.TTSProvider))

	if err := voiceStack.ensureAssets(cmd.Context(), cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
		return err
	}

	provider, cleanup, err := voiceStack.newProvider(cfg)
	if err != nil {
		return fmt.Errorf("init TTS: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	locale, _ := cmd.Flags().GetString("locale")
	gender, _ := cmd.Flags().GetString("gender")
	voices := provider.ListVoices(locale, gender)
	if len(voices) == 0 {
		fmt.Fprintln(out, dimStyle.Render("  No voices found."))
		return nil
	}

	activeVoice := activeVoiceFor(cfg)
	for _, v := range voices {
		active := ""
		if strings.EqualFold(v.Name, activeVoice) {
			active = " " + activeStyle.Render("●")
		}
		fmt.Fprintf(out, "  %s %s  %s\n", keyStyle.Render(fmt.Sprintf("%-16s", v.Name)), v.FriendlyName, dimStyle.Render(fmt.Sprintf("%s / %s", v.Gender, v.Locale))+active)
	}
	fmt.Fprintf(out, "\n  %s\n", dimStyle.Render(fmt.Sprintf("%d voices found.", len(voices))))
	if notice := tts.CatalogNoticeOf(provider); notice != "" {
		fmt.Fprintf(out, "  %s\n", dimStyle.Render(notice))
	}
	fmt.Fprintln(out)
	return nil
}
