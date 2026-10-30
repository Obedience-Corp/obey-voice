// Package persona loads obey-voice persona profiles (multi-agent support).
//
// Profiles live at:
//
//	<ConfigDir>/personas/<id>/persona.yaml
//
// Prompt bodies remain in the existing prompts catalog; profiles only
// reference them and carry display name + TTS voice.
package persona

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	managedqwen "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// Schema identifies the persona profile document version.
const Schema = "obey-voice.persona.v1"

// DefaultID is the shipped default profile id: the persona a fresh install
// talks to and the id every heal path prefers. Companions (samantha, veronica,
// jarvis, whoever is added next) live in shipped/*.yaml; they are not defaults.
const DefaultID = "obey"

// DefaultDisplayName is the display name of the shipped default profile. It
// matches the agent_name config default; a config that names the agent wins.
const DefaultDisplayName = "Obey"

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Profile is one voice agent persona (not the system-prompt body).
//
// The json tags mirror the yaml paths so every machine-readable surface
// (persona show/edit/create --json, GET /v1/personas) speaks the same
// snake_case as persona list --json. Without them encoding/json emits Go
// field names, which no client should have to know. They deliberately carry
// no omitempty: a stable key set lets a decoder distinguish "inherit the app
// default" (empty string) from "field absent".
type Profile struct {
	ParticipationDescription string     `yaml:"participation_description,omitempty" json:"participation_description,omitempty"`
	StartingDirectory        string     `yaml:"starting_directory,omitempty" json:"starting_directory"`
	Schema                   string     `yaml:"schema" json:"schema"`
	ID                       string     `yaml:"id" json:"id"`
	DisplayName              string     `yaml:"display_name" json:"display_name"`
	Aliases                  []string   `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Builtin                  bool       `yaml:"builtin,omitempty" json:"builtin"`
	Inactive                 bool       `yaml:"inactive,omitempty" json:"inactive"`
	Brain                    Brain      `yaml:"brain,omitempty" json:"brain"`
	Audio                    *Audio     `yaml:"audio,omitempty" json:"audio,omitempty"`
	TTS                      TTS        `yaml:"tts" json:"tts"`
	Prompts                  PromptRefs `yaml:"prompts" json:"prompts"`
	Path                     string     `yaml:"-" json:"path"` // absolute path of persona.yaml when loaded
}

// ErrInactive marks a profile that is present on disk but unavailable for
// selection or execution.
var ErrInactive = errors.New("persona is inactive")

// RequireEnabled rejects an inactive profile with the command needed to make
// it selectable again.
func RequireEnabled(p *Profile) error {
	if p == nil {
		return fmt.Errorf("persona profile: nil")
	}
	if p.Inactive {
		return fmt.Errorf("persona %q is inactive; run 'persona enable %s': %w", p.ID, p.ID, ErrInactive)
	}
	if p.Audio != nil && p.Audio.Mode == AudioNative {
		return fmt.Errorf("persona %q: %w; select Standard mode until native input and transport are available", p.ID, ErrNativeAudioUnavailable)
	}
	return nil
}

// CatalogIssue identifies a profile that could not be loaded while building
// a roster. A bad edit must not hide healthy profiles from a running session,
// but callers that own a user-facing surface can report the issue.
type CatalogIssue struct {
	ID  string
	Err error
}

// Brain holds per-persona model routing.
//
//	brain:
//	  provider: ollama          # or claude, grok; empty = app default
//	  model: qwen2.5:14b        # provider-specific model id; empty = app default
//	  worker:                   # the model that runs spawned background jobs
//	    provider: ollama        # or claude, grok; empty = app default
//	    model: qwen3:8b         # empty = that provider's usual default
//
// Profiles written before this field existed have the zero value and keep
// using the app-level brain keys until edited.
type Brain struct {
	Provider string `yaml:"provider,omitempty" json:"provider"`
	Model    string `yaml:"model,omitempty" json:"model"`
	Worker   Worker `yaml:"worker,omitempty" json:"worker"`
}

// Worker is the persona's async job brain. It is a peer of the live brain,
// not an afterthought: a persona that talks locally and works remotely is a
// deliberate choice, and one nobody should make by accident.
type Worker struct {
	Provider   string `yaml:"provider,omitempty" json:"provider"`
	Model      string `yaml:"model,omitempty" json:"model"`
	OllamaHost string `yaml:"ollama_host,omitempty" json:"ollama_host"`
}

// TTS holds per-persona speech settings. Each persona may choose any supported
// TTS provider and a voice id valid for that provider.
//
//	tts:
//	  provider: kokoro      # or qwen3-tts, …
//	  voice: af_heart       # kokoro voice id, or Qwen preset (e.g. Vivian)
//	  tier: 1.7b            # qwen3-tts only: native model tier (0.6b / 1.7b)
//
// Empty provider leaves the app-level tts_provider unchanged and routes voice
// using the effective provider after Apply. Empty voice leaves voice keys alone.
type TTS struct {
	// Provider is the TTS backend (e.g. kokoro, qwen3-tts). Empty = keep app default.
	Provider string `yaml:"provider,omitempty" json:"provider"`
	// Voice is the speaker id for Provider (Kokoro voice name or Qwen preset).
	Voice string `yaml:"voice,omitempty" json:"voice"`
	// Tier selects the native Qwen3-TTS model tier for this persona (0.6b for
	// latency, 1.7b for quality). Empty inherits the app-level
	// qwen_tts_model_tier; only qwen3-tts reads it.
	Tier string `yaml:"tier,omitempty" json:"tier"`
}

// PromptRefs names documents in the prompts catalog.
//
//	persona: catalog name of the kind=persona system prompt (usually this
//	         profile's id; private file at prompts/persona/<id>.yaml).
//	turn:    optional catalog name of the kind=turn per-reply voice instruction
//	         appended on Claude/Grok paths. Empty = shared embedded default.
//	         This is NOT a conversation-turn counter and is independent of the
//	         TTS voice id (e.g. Uncle_Fu).
type PromptRefs struct {
	Persona string `yaml:"persona" json:"persona"`
	Turn    string `yaml:"turn,omitempty" json:"turn"`
}

// Validate checks structural invariants.
func (p *Profile) Validate() error {
	if p == nil {
		return fmt.Errorf("persona profile: nil")
	}
	if !utf8.ValidString(p.ParticipationDescription) || utf8.RuneCountInString(p.ParticipationDescription) > 256 || len(p.ParticipationDescription) > 1024 {
		return fmt.Errorf("participation description must be valid UTF-8 and at most 256 characters")
	}
	if p.Schema != Schema {
		return fmt.Errorf("persona profile: schema %q, want %q", p.Schema, Schema)
	}
	if err := ValidateID(p.ID); err != nil {
		return err
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		return fmt.Errorf("persona profile %q: missing display_name", p.ID)
	}
	if strings.TrimSpace(p.Prompts.Persona) == "" {
		return fmt.Errorf("persona profile %q: missing prompts.persona", p.ID)
	}
	if err := validateTier(p.TTS.Tier); err != nil {
		return fmt.Errorf("persona profile %q: %w", p.ID, err)
	}
	if err := p.Audio.Validate(); err != nil {
		return fmt.Errorf("persona profile %q: %w", p.ID, err)
	}
	return nil
}

// canonicalTier normalizes a non-empty tier to its canonical spelling on
// persist, so the profile on disk matches the catalog ("1.7" never sits
// beside "1.7b" in the editor).
func canonicalTier(tier string) string {
	if strings.TrimSpace(tier) == "" {
		return ""
	}
	return managedqwen.NormalizeModelTier(tier)
}

// validateTier accepts an empty tier (inherit the app default) or any spelling
// that normalizes to a known native Qwen3-TTS tier.
func validateTier(tier string) error {
	if strings.TrimSpace(tier) == "" {
		return nil
	}
	switch managedqwen.NormalizeModelTier(tier) {
	case managedqwen.DefaultModelTier, managedqwen.Tier1_7B:
		return nil
	}
	return fmt.Errorf("tts.tier %q: unknown tier (use 0.6b or 1.7b)", tier)
}

// ValidateTier reports whether tier is an accepted Qwen3-TTS tier spelling.
// Exported so callers can reject a bad --tier before any write lands, rather
// than discovering it inside Write's validation halfway through an edit.
func ValidateTier(tier string) error {
	return validateTier(tier)
}

// ValidateID enforces lowercase kebab-case persona ids.
func ValidateID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("persona id: empty")
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("persona id %q: must be lowercase kebab-case [a-z0-9-]+", id)
	}
	return nil
}

// Dir returns the personas root under the install config dir.
func Dir() string {
	return filepath.Join(config.ConfigDir(), "personas")
}

// ProfilePath returns the persona.yaml path for id.
func ProfilePath(id string) string {
	return filepath.Join(Dir(), id, "persona.yaml")
}

// LoadFile reads and validates a persona profile from path.
func LoadFile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading persona profile %s: %w", path, err)
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing persona profile %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Directory name is authoritative when it disagrees (defensive).
	dirID := filepath.Base(filepath.Dir(path))
	if dirID != "" && dirID != "." && dirID != p.ID {
		return nil, fmt.Errorf("persona profile %s: id %q does not match directory %q", path, p.ID, dirID)
	}
	p.Path = path
	return &p, nil
}

// Load loads the profile for id from the personas directory.
func Load(id string) (*Profile, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	return LoadFile(ProfilePath(id))
}

// List returns profiles found under personas/, sorted by id.
func List() ([]*Profile, error) {
	return listProfiles(Dir(), Load)
}

// ListWithIssues returns every valid profile and records unloadable profile
// directories separately. Directory/read errors still fail the listing; an
// individual malformed profile is recoverable by repairing that profile.
func ListWithIssues() ([]*Profile, []CatalogIssue, error) {
	return listProfilesWithIssues(Dir(), Load)
}

func listProfiles(root string, load func(string) (*Profile, error)) ([]*Profile, error) {
	profiles, _, err := listProfilesWithIssues(root, load)
	return profiles, err
}

func listProfilesWithIssues(root string, load func(string) (*Profile, error)) ([]*Profile, []CatalogIssue, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("listing personas: %w", err)
	}
	var out []*Profile
	var issues []CatalogIssue
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if err := ValidateID(id); err != nil {
			continue
		}
		p, err := load(id)
		if err != nil {
			issues = append(issues, CatalogIssue{ID: id, Err: err})
			continue
		}
		out = append(out, p)
	}
	return out, issues, nil
}

// Write writes profile to personas/<id>/persona.yaml. It does not overwrite
// when skipExisting is true and the file already exists.
func Write(p *Profile, skipExisting bool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	path := ProfilePath(p.ID)
	if skipExisting {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating persona dir: %w", err)
	}
	// Encode without Path field (yaml:"-").
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding persona profile: %w", err)
	}
	// Prefer a stable header for humans.
	header := "# yaml-language-server: $schema=obey-voice.persona.v1\n"
	body := append([]byte(header), data...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	p.Path = path
	return nil
}

// FromConfig snapshots the live config as a user profile. Create uses it to
// clone today's voice onto a new persona. It is not a migration path and it
// never flags the result builtin.
func FromConfig(cfg *config.Config) *Profile {
	if cfg == nil {
		cfg = &config.Config{}
	}
	id := DefaultID
	promptName := strings.TrimSpace(cfg.Persona)
	if err := ValidateID(strings.TrimSpace(cfg.ActivePersona)); err == nil {
		id = strings.TrimSpace(cfg.ActivePersona)
	} else if err := ValidateID(promptName); err == nil {
		id = promptName
	}
	if promptName == "" {
		promptName = id
	}
	display := strings.TrimSpace(cfg.AgentName)
	if display == "" {
		display = DefaultDisplayName
	}
	provider := strings.TrimSpace(cfg.TTSProvider)
	voice := voiceForProvider(cfg, provider)
	return &Profile{
		Schema:      Schema,
		ID:          id,
		DisplayName: display,
		TTS: TTS{
			Provider: provider,
			Voice:    voice,
		},
		Prompts: PromptRefs{
			Persona: promptName,
		},
	}
}

// Apply overlays profile fields onto cfg (non-empty profile fields win).
// TTS provider and voice are per-persona: users may pick any supported backend
// and any voice valid for that backend on each profile.
func Apply(cfg *config.Config, p *Profile) {
	if cfg == nil || p == nil {
		return
	}
	applyIdentity(cfg, p)
	applyBrain(cfg, p.Brain)
	applyTTS(cfg, p.TTS)
}

// applyIdentity copies name, prompt refs, and id onto cfg. Brain and TTS stay
// with the caller; Apply adds the profile's saved model and voice afterward.
func applyIdentity(cfg *config.Config, p *Profile) {
	if name := strings.TrimSpace(p.DisplayName); name != "" {
		cfg.AgentName = name
	}
	if ref := effectivePersonaRefFrom(p, config.PromptsDirFrom(cfg)); ref != "" {
		cfg.Persona = ref
	}
	// Turn prompt is independent of the persona system-prompt name. Empty
	// means the brain uses the shared embedded turn instruction.
	cfg.TurnPrompt = strings.TrimSpace(p.Prompts.Turn)
	if id := strings.TrimSpace(p.ID); id != "" {
		cfg.ActivePersona = id
	}
}

// effectivePersonaRef is the kind=persona catalog name the brain should resolve
// for p. The private prompts/persona/<id>.yaml wins over prompts.persona so a
// stale ref (the real Uncle_Fu case: prompts.persona held the TTS voice id)
// cannot hard-fail brain construction now that the resolver no longer falls back
// to the embedded default document. The editor heals the file on disk; this
// keeps the running process working before anyone opens it.
func effectivePersonaRef(p *Profile) string {
	return effectivePersonaRefFrom(p, promptsDir())
}

func effectivePersonaRefFrom(p *Profile, promptDir string) string {
	ref := strings.TrimSpace(p.Prompts.Persona)
	id := strings.TrimSpace(p.ID)
	if id == "" || ref == id {
		return ref
	}
	// Only a real user document at prompts/persona/<id>.yaml wins. Checking via
	// LoadSystemPrompt would also match the embedded fallback, which would
	// override a deliberate shared ref on the samantha profile.
	entry, err := prompts.Describe(promptDir, prompts.KindPersona, id)
	if err != nil || entry.Source != prompts.SourceUser {
		return ref
	}
	return id
}

// applyBrain overlays per-persona model routing. Empty fields inherit the
// app-level keys, so pre-Brain profiles behave exactly as before.
func applyBrain(cfg *config.Config, b Brain) {
	provider := strings.TrimSpace(b.Provider)
	if provider != "" {
		cfg.BrainProvider = provider
		// Provider flips change what capabilities make sense (tools, skills);
		// re-run the ollama auto-enable rules against the new provider.
		config.ApplyOllamaDefaults(cfg)
	}
	applyWorker(cfg, b.Worker)
	model := strings.TrimSpace(b.Model)
	if model == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(cfg.BrainProvider)) {
	case "ollama":
		cfg.OllamaModel = model
	case "grok":
		cfg.GrokModel = model
	case "cursor":
		cfg.CursorModel = model
	case "muse-spark":
		cfg.MuseSparkModel = model
	case "obey":
		cfg.ObeyModel = model
	case "codex":
		cfg.CodexModel = model
	}
}

// applyWorker overlays the persona's async job brain onto the app-level
// voice_worker_* keys. Empty fields inherit, so a persona that never named a
// worker keeps whatever Settings chose.
func applyWorker(cfg *config.Config, w Worker) {
	if cfg.VoiceWorkers != nil && cfg.VoiceWorkers.Mode == config.WorkerModeTiered {
		return
	}
	if provider := strings.TrimSpace(w.Provider); provider != "" {
		cfg.VoiceWorkerProvider = provider
	}
	if model := strings.TrimSpace(w.Model); model != "" {
		cfg.VoiceWorkerModel = model
	}
	if host := strings.TrimSpace(w.OllamaHost); host != "" {
		cfg.VoiceWorkerOllamaHost = host
	}
}

// ModelForProvider reads the app-level model key a brain provider routes to.
// Exported because every surface that reports a persona's effective stack
// (persona use --json, GET /v1/personas) needs the same provider→key mapping
// Apply uses, and claude deliberately has no key at all.
func ModelForProvider(cfg *config.Config, provider string) string {
	return modelForProvider(cfg, provider)
}

// modelForProvider reads the app-level model key for a brain provider.
func modelForProvider(cfg *config.Config, provider string) string {
	if cfg == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return strings.TrimSpace(cfg.OllamaModel)
	case "grok":
		return strings.TrimSpace(cfg.GrokModel)
	case "cursor":
		return strings.TrimSpace(cfg.CursorModel)
	case "muse-spark":
		return strings.TrimSpace(cfg.MuseSparkModel)
	case "obey":
		return strings.TrimSpace(cfg.ObeyModel)
	case "codex":
		return strings.TrimSpace(cfg.CodexModel)
	default:
		return ""
	}
}

// applyTTS writes provider/voice/tier from the profile onto cfg.
func applyTTS(cfg *config.Config, t TTS) {
	if provider := strings.TrimSpace(t.Provider); provider != "" {
		cfg.TTSProvider = provider
	}
	if voice := strings.TrimSpace(t.Voice); voice != "" {
		// Onto the key the selected provider reads, and no other. audio8 needs
		// its own: tts_voice is a Kokoro enum, so an audio8 voice landing there
		// was rejected on save and never reached the provider.
		config.SetTTSVoiceFor(cfg, cfg.TTSProvider, voice)
	}
	// Tier picks the native model inside the multi-tier package; only qwen
	// reads it, and empty inherits the app-level qwen_tts_model_tier.
	if tier := strings.TrimSpace(t.Tier); tier != "" && normalizeTTSProvider(cfg.TTSProvider) == "qwen3-tts" {
		cfg.QwenTTSModelTier = managedqwen.NormalizeModelTier(tier)
	}
}

// voiceForProvider reads the voice field the given provider uses from cfg.
func voiceForProvider(cfg *config.Config, provider string) string {
	if cfg == nil {
		return ""
	}
	if normalizeTTSProvider(provider) == "qwen3-tts" {
		if v := strings.TrimSpace(cfg.QwenTTSVoice); v != "" {
			return v
		}
		// Preserve legacy single-agent Qwen selections only when they belong
		// to Qwen's voice catalog. tts_voice normally holds a Kokoro name;
		// copying it would persist a persona that Qwen cannot synthesize.
		if v := strings.TrimSpace(cfg.TTSVoice); v != "" {
			if _, _, ok := managedqwen.ResolveVoice(v); ok {
				return v
			}
		}
		// Empty inherits Qwen's configured/provider default, including the
		// default-only external CLI; do not pin a voice the user never chose.
		return ""
	}
	// ConfiguredTTSVoice, not TTSVoiceFor: this feeds a persona profile, where
	// an empty voice means "inherit the global" and substituting audio8's
	// packaged reference would pin a voice the user never chose.
	return config.ConfiguredTTSVoice(cfg, provider)
}

func normalizeTTSProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// PersistTTS writes the profile's TTS selection into the live config file so
// persona use and settings stay aligned.
func PersistTTS(p *Profile) error {
	if p == nil {
		return nil
	}
	if provider := strings.TrimSpace(p.TTS.Provider); provider != "" {
		if err := config.SetAndSave("tts_provider", provider); err != nil {
			return err
		}
	}
	voice := strings.TrimSpace(p.TTS.Voice)
	if voice == "" {
		return nil
	}
	// Need effective provider after optional profile provider.
	provider := strings.TrimSpace(p.TTS.Provider)
	if provider == "" {
		if v, ok := config.Get("tts_provider").(string); ok {
			provider = v
		}
	}
	return config.SetAndSave(config.TTSVoiceKeyFor(provider), voice)
}

// ActiveID returns the configured active persona id, defaulting to DefaultID.
func ActiveID(cfg *config.Config) string {
	if cfg == nil {
		return DefaultID
	}
	id := strings.TrimSpace(cfg.ActivePersona)
	if id == "" {
		return DefaultID
	}
	return id
}
