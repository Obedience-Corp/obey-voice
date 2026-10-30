package config

import (
	"context"
	"fmt"
	"path/filepath"
)

const openAISpeechEnsureRemedy = "run `obey-voice models ensure --tts`"

// DiagnoseOpenAISpeech asks the configured openai-speech target what it serves:
// whether it answers, the Ollama release, whether the model is pulled, whether
// that release lists Phonon's voices, and whether the voice catalog file is in
// models_dir. One probe bounded by OpenAISpeechProbeTimeout feeds every row. It
// reports nothing for another provider.
func DiagnoseOpenAISpeech(ctx context.Context, cfg *Config, modelsDir string) []Diagnostic {
	if !OpenAISpeechSelected(cfg) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, OpenAISpeechProbeTimeout)
	defer cancel()
	t := ProbeOpenAISpeech(ctx, cfg)
	if t.Native {
		severity, detail := SeverityOK, "pinned Phonon runtime/model/voices cached for "+t.NativeBackend+"; actual device is checked at speech startup"
		if t.Err != nil {
			severity, detail = SeverityError, t.Err.Error()
		}
		return []Diagnostic{{Name: "openai-speech-native", Severity: severity, Detail: detail, Remediation: openAISpeechEnsureRemedy}}
	}

	var diags []Diagnostic
	switch {
	case t.RejectedCode() != 0:
		diags = append(diags, Diagnostic{
			Name: "openai-speech-server", Severity: SeverityError,
			Detail:      fmt.Sprintf("%s rejected the request (%d)", t.Base, t.RejectedCode()),
			Remediation: "check openai_speech_api_key",
		})
	case !t.Reachable:
		diags = append(diags, Diagnostic{
			Name: "openai-speech-server", Severity: SeverityError,
			Detail:      fmt.Sprintf("%s did not answer: %v", t.Base, t.Err),
			Remediation: "start Ollama (`ollama serve`) or point openai_speech_url at a running speech server",
		})
	case t.TooOld():
		diags = append(diags, Diagnostic{
			Name: "openai-speech-server", Severity: SeverityError,
			Detail:      fmt.Sprintf("Ollama %s at %s is older than %s, the first release that serves Phonon", t.Version, t.Root, MinPhononOllama),
			Remediation: "update Ollama",
		})
	case t.Ollama:
		diags = append(diags, Diagnostic{Name: "openai-speech-server", Severity: SeverityOK, Detail: fmt.Sprintf("Ollama %s at %s", t.Version, t.Root)})
	default:
		diags = append(diags, Diagnostic{Name: "openai-speech-server", Severity: SeverityOK, Detail: t.Base + " answers GET /models"})
	}

	switch {
	case t.Ollama && !t.ModelsKnown:
		diags = append(diags, Diagnostic{
			Name: "openai-speech-model", Severity: SeverityWarn,
			Detail: t.ListingProblem(),
		})
	case t.Reachable && (t.Ollama || len(t.Models) > 0):
		if t.ModelListed {
			diags = append(diags, Diagnostic{Name: "openai-speech-model", Severity: SeverityOK, Detail: fmt.Sprintf("%s served by %s", t.Model, t.Base)})
		} else {
			remedy := "set openai_speech_model to a model the server lists"
			if t.Ollama {
				remedy = openAISpeechEnsureRemedy + " (or `ollama pull " + t.Model + "`)"
			}
			diags = append(diags, Diagnostic{
				Name: "openai-speech-model", Severity: SeverityError,
				Detail:      fmt.Sprintf("%s is not served by %s", t.Model, t.Base),
				Remediation: remedy,
			})
		}
	}

	if t.Ollama && t.ModelListed {
		diags = append(diags, ollamaVoicesDiagnostic(ctx, t, openAISpeechAPIKey(cfg)))
	}

	path := filepath.Join(modelsDir, filepath.FromSlash(OpenAISpeechVoicesFile))
	switch err := checkVoicesCatalog(path); {
	case err == nil:
		diags = append(diags, Diagnostic{Name: "openai-speech-catalog-file", Severity: SeverityOK, Detail: path})
	case pathExists(path):
		diags = append(diags, Diagnostic{
			Name: "openai-speech-catalog-file", Severity: SeverityWarn,
			Detail:      fmt.Sprintf("%s is not a voice catalog (%v)", path, err),
			Remediation: openAISpeechEnsureRemedy,
		})
	default:
		diags = append(diags, Diagnostic{
			Name: "openai-speech-catalog-file", Severity: SeverityWarn,
			Detail:      path + " is missing; voices are listed without locale, gender or description",
			Remediation: openAISpeechEnsureRemedy,
		})
	}
	return diags
}

// ollamaVoicesDiagnostic reports whether this Ollama release lists the
// model's voices. Only a 200 without voices earns the upgrade note; a failed
// request says it could not ask.
func ollamaVoicesDiagnostic(ctx context.Context, t OpenAISpeechTarget, apiKey string) Diagnostic {
	names, answered, err := t.Voices, t.VoicesErr == nil, t.VoicesErr
	if !t.usesPhononCatalog() {
		names, answered, err = OllamaShowVoices(ctx, t.Root, t.Model, apiKey)
	}
	switch {
	case !answered:
		return Diagnostic{
			Name: "openai-speech-voices", Severity: SeverityWarn,
			Detail: fmt.Sprintf("could not read %s's voices from /api/show: %v", t.Model, err),
		}
	case t.usesPhononCatalog() && !HasNamedVoices(names):
		return Diagnostic{
			Name: "openai-speech-voices", Severity: SeverityWarn,
			Detail:      fmt.Sprintf("Ollama %s lists no voices for %s; only the configured voice and default can be chosen", t.Version, t.Model),
			Remediation: PhononVoicesRemedy(t.Version),
		}
	default:
		return Diagnostic{Name: "openai-speech-voices", Severity: SeverityOK, Detail: fmt.Sprintf("Ollama lists %d voices for %s", len(names), t.Model)}
	}
}
