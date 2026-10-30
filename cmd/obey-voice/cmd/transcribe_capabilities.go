//go:build !integration

package cmd

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/transcription/protocol"
)

func newTranscribeCapabilitiesCmd(deps transcribeDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Report what this install can transcribe (read-only, offline)",
		Long: `Report what this install can transcribe.

Read-only and offline: it resolves the configured provider, reads the asset
tables and the models directory, and never constructs a recognizer, opens the
microphone, or downloads anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			cfg, err := deps.loadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			doc := buildTranscribeCapabilities(deps, cfg)
			if asJSON {
				return writeJSON(cmd, doc)
			}
			printTranscribeCapabilities(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output machine-readable JSON")
	return cmd
}

func printTranscribeCapabilities(out io.Writer, doc protocol.Capabilities) {
	fmt.Fprintf(out, "\n  Transcribe capabilities (models dir: %s)\n\n", doc.Readiness.ModelsDir)
	fmt.Fprintf(out, "  Protocol: %s    Formats: ", strings.Join(doc.Protocols, ", "))
	for i, f := range doc.Formats {
		if i > 0 {
			fmt.Fprint(out, ", ")
		}
		fmt.Fprintf(out, "%s %d Hz %dch", f.Format, f.SampleRateHz, f.Channels)
	}
	fmt.Fprintf(out, "    Timing: %s\n\n", strings.Join(doc.Timing, ", "))

	for _, p := range doc.Providers {
		marker := " "
		if p.Selected {
			marker = "*"
		}
		langs := strings.Join(p.Languages, ", ")
		if p.AutoDetect {
			langs = strings.TrimPrefix(langs+" (provider default)", " ")
		}
		decode := "file transcription"
		if !p.SegmentDecode {
			decode = "live only"
		}
		fmt.Fprintf(out, "  %s %s/%s — %s, languages: %s\n", marker, p.Name, p.Mode, decode, langs)
		for _, m := range p.Models {
			state := "installed"
			if !m.Installed {
				state = "missing " + strings.Join(m.Missing, ", ")
			}
			fmt.Fprintf(out, "      model %s — %s\n", m.ID, state)
		}
	}

	fmt.Fprintln(out)
	if doc.Readiness.Ready {
		fmt.Fprintln(out, "  Ready.")
	} else {
		fmt.Fprintf(out, "  Not ready: %s\n", doc.Readiness.Reason)
	}
	if doc.DownloadRequired {
		fmt.Fprintln(out, "  Install what is missing with: obey-voice models ensure --stt --vad")
	}
	for _, d := range doc.Diagnostics {
		fmt.Fprintf(out, "  [%s] %s — %s\n", d.Severity, d.Name, d.Detail)
	}
	fmt.Fprintln(out)
}

// buildTranscribeCapabilities assembles the capabilities document from the
// read-only surfaces only: normalization, the spec table, the asset tables and
// the manifest's on-disk status.
func buildTranscribeCapabilities(deps transcribeDeps, cfg *config.Config) protocol.Capabilities {
	modelsDir := deps.modelsDir(cfg)
	doc := protocol.Capabilities{
		Schema:      protocol.CapabilitiesSchema,
		Protocols:   []string{protocol.Protocol, protocol.WordProtocol},
		Formats:     protocol.SupportedFormats(),
		Timing:      protocol.SupportedTiming(),
		Providers:   []protocol.ProviderCap{},
		Readiness:   protocol.Readiness{ModelsDir: modelsDir},
		Diagnostics: []protocol.Diagnostic{},
	}

	selected, selErr := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode)
	doc.WordAnalysis = &protocol.WordCapability{
		Protocol: protocol.WordProtocol, Provider: selected.Provider,
		Supported:  selErr == nil && selected.Provider == config.STTProviderParakeet && (cfg.ParakeetModel == "" || cfg.ParakeetModel == "tdt-0.6b-v2"),
		TextPolicy: "verbatim", TimingMethod: stt.WordTimingMethod, TimingValidity: "estimated",
		Reason: "Word edges are model estimates; review cuts. Batch safety is not qualified.",
	}
	if !doc.WordAnalysis.Supported {
		doc.WordAnalysis.Reason = "Select parakeet in an isolated speech configuration for measured word estimates."
	}

	manifest, manifestErr := deps.manifest(cfg)
	statuses := manifest.Status(modelsDir)
	missing := missingAssets(statuses)

	for _, norm := range normalizedProviders() {
		spec, ok := stt.SpecForNormalized(norm)
		if !ok {
			continue
		}
		langs, auto := transcribeLanguages(cfg, norm)
		row := protocol.ProviderCap{
			Name:          norm.Provider,
			Mode:          norm.Mode,
			Selected:      selErr == nil && norm.Provider == selected.Provider && norm.Mode == selected.Mode,
			SegmentDecode: spec.SegmentDecode,
			Timing:        orEmpty(spec.Timing),
			Languages:     orEmpty(langs),
			AutoDetect:    auto,
			Models:        []protocol.ModelCap{},
		}
		if row.Selected {
			row.Models = selectedModels(manifest, statuses)
		}
		doc.Providers = append(doc.Providers, row)
	}

	doc.Diagnostics = transcribeDiagnostics(deps.diagnose(cfg, modelsDir), statuses)
	doc.DownloadRequired = len(missing) > 0
	doc.Readiness.Missing = missing
	doc.Readiness.Ready, doc.Readiness.Reason = transcribeReadiness(selected, selErr, manifestErr, missing)
	return doc
}

// transcribeDiagnostics keeps the setup checks this surface can act on. The
// brain, TTS and hotkey rows belong to `obey-voice doctor`: a caller deciding
// whether to submit a transcription job cannot do anything with them.
func transcribeDiagnostics(all []config.Diagnostic, statuses []config.AssetStatus) []protocol.Diagnostic {
	relevant := map[string]bool{
		"stt-provider":         true,
		"stt-language":         true,
		"model-assets":         true,
		"whispercpp-binary":    true,
		"transcribecpp-binary": true,
	}
	for _, st := range statuses {
		relevant["asset:"+st.ID] = true
	}
	out := []protocol.Diagnostic{}
	for _, d := range all {
		if d.Severity == config.SeverityOK || !relevant[d.Name] {
			continue
		}
		out = append(out, protocol.Diagnostic{
			Name:        d.Name,
			Severity:    string(d.Severity),
			Detail:      d.Detail,
			Remediation: d.Remediation,
		})
	}
	return out
}

// orEmpty keeps a JSON list a list: a client that iterates it should never
// have to handle null.
func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// transcribeReadiness answers "could a job start right now", and names the
// first thing in the way when it could not.
func transcribeReadiness(selected config.NormalizedSTT, selErr, manifestErr error, missing []string) (bool, string) {
	switch {
	case selErr != nil:
		return false, selErr.Error()
	case manifestErr != nil:
		return false, manifestErr.Error()
	case len(missing) > 0:
		return false, fmt.Sprintf("missing model assets: %s", strings.Join(missing, ", "))
	}
	spec, ok := stt.SpecForNormalized(selected)
	if !ok || !spec.SegmentDecode {
		return false, fmt.Sprintf("stt_provider %s/%s cannot transcribe a file", selected.Provider, selected.Mode)
	}
	return true, ""
}

// selectedModels reports the install state and revision of the selected
// provider's speech models. The VAD and any other required asset are reported
// under readiness, which is what a caller checks before submitting a job.
func selectedModels(manifest config.AssetManifest, statuses []config.AssetStatus) []protocol.ModelCap {
	revisions := map[string]string{}
	for _, a := range manifest.Assets {
		revisions[a.ID] = assetRevision(a)
	}
	models := []protocol.ModelCap{}
	for _, st := range statuses {
		if st.Kind != config.AssetKindSTT {
			continue
		}
		models = append(models, protocol.ModelCap{
			ID:        st.ID,
			Revision:  revisions[st.ID],
			Installed: st.Installed,
			Missing:   st.Missing,
		})
	}
	return models
}

// assetRevision is the digest the asset table pins, which is the same value
// the accepted event reports as model_revision. An asset installed from several
// files has no single revision.
func assetRevision(a config.Asset) string {
	if a.Archive != nil {
		return a.Archive.SHA256
	}
	if len(a.Files) == 1 {
		return a.Files[0].SHA256
	}
	return ""
}

// normalizedProviders lists every provider/mode pair this build implements,
// resolved from the alias table so the list cannot disagree with what a config
// value selects.
func normalizedProviders() []config.NormalizedSTT {
	seen := map[string]bool{}
	var out []config.NormalizedSTT
	for _, spec := range stt.Providers() {
		norm, ok := config.NormalizeSTT(spec.Name)
		if !ok {
			continue
		}
		key := norm.Provider + "/" + norm.Mode
		if seen[key] {
			continue
		}
		seen[key] = true
		norm.Alias = ""
		out = append(out, norm)
	}
	slices.SortFunc(out, func(a, b config.NormalizedSTT) int {
		if c := cmp.Compare(a.Provider, b.Provider); c != 0 {
			return c
		}
		return cmp.Compare(a.Mode, b.Mode)
	})
	return out
}
