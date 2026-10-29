package brain

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

// personaSystemPrompt resolves the configured persona document and returns the
// assembled system prompt with {agent_name} substituted. A missing or invalid
// configured document is an error, so a bad persona surfaces at construction
// rather than mid-session.
func personaSystemPrompt(cfg *config.Config) (string, error) {
	return resolvePrompt(cfg, prompts.KindPersona, cfg.Persona)
}

// PersonaPromptHash identifies the resolved identity text, including display
// placeholders, that a freshly constructed provider will receive.
func PersonaPromptHash(cfg *config.Config) (string, error) {
	text, err := personaSystemPrompt(cfg)
	if err != nil {
		return "", err
	}
	return shortPromptHash(text), nil
}

// turnInstruction resolves the per-turn instruction appended to each user
// message on the Claude, Cursor, and Grok prompt paths.
//
// Name comes from cfg.TurnPrompt (profile prompts.turn). Empty uses the
// shared embedded turn document — not the persona system-prompt name — so a
// custom agent does not need a private turn file and never confuses
// "turn: samantha" with the Samantha identity.
func turnInstruction(cfg *config.Config) (string, error) {
	name := ""
	if cfg != nil {
		name = cfg.TurnPrompt
	}
	return resolvePrompt(cfg, prompts.KindTurn, name)
}

// TurnInstructionFor exposes the resolved per-turn instruction so the CLI can
// render exactly what a provider receives. Reusing the brain's own resolution is
// the point: a second implementation would drift and the preview would lie.
func TurnInstructionFor(cfg *config.Config) (string, error) {
	return turnInstruction(cfg)
}

// CompactInstruction resolves the kind=compact document used as /compact's
// summarize-turn prompt. Name comes from cfg.CompactPrompt; empty selects the
// embedded default, so users customize compaction the same way as any prompt:
// a kind=compact document in the prompts dir.
func CompactInstruction(cfg *config.Config) (string, error) {
	name := ""
	if cfg != nil {
		name = cfg.CompactPrompt
	}
	return resolvePrompt(cfg, prompts.KindCompact, name)
}

// resolvePrompt resolves a document of the given kind and name (explicit path,
// then the user prompts dir, then the embedded default when name matches),
// assembles it, and substitutes {agent_name}.
func resolvePrompt(cfg *config.Config, kind prompts.Kind, name string) (string, error) {
	userDir := ""
	agentName := ""
	if cfg != nil {
		userDir = cfg.PromptsDir
		agentName = cfg.AgentName
	}
	if userDir == "" {
		userDir = config.PromptsDirFrom(cfg)
	}
	doc, err := prompts.Resolver{UserDir: userDir}.Resolve(kind, name)
	if err != nil {
		return "", fmt.Errorf("resolving %s prompt %q: %w", kind, name, err)
	}
	env := prompts.PlaceholderEnv{}
	if cfg != nil {
		env.UserName = strings.TrimSpace(cfg.UserDisplayName)
	}
	// Catalog-driven names/values (#166) with the nil-cfg-safe agent name (#164).
	text, err := prompts.ResolvePlaceholders(doc.Assemble(), prompts.PlaceholderNames(), prompts.PlaceholderValues(agentName, env))
	if err != nil {
		return "", fmt.Errorf("%s prompt %q: %w", kind, name, err)
	}
	return text, nil
}

// EnvironmentContext returns system context for grounding the model.
// Env describes the machine the agent is running on, for the grounding block in
// the system prompt.
//
// A zero Env falls back to the host: USER, os.Hostname() and the compiled GOOS
// and GOARCH. That keeps the CLI's behaviour identical while letting an embedder
// state its own environment — a server answering for many users has no business
// telling the model it is running as whoever started the process.
type Env struct {
	User     string
	Hostname string
	OS       string // e.g. "darwin/arm64"
}

// resolve fills empty fields from the host.
func (e Env) resolve() Env {
	if e.User == "" {
		e.User = os.Getenv("USER")
	}
	if e.Hostname == "" {
		e.Hostname, _ = os.Hostname()
	}
	if e.OS == "" {
		e.OS = runtime.GOOS + "/" + runtime.GOARCH
	}
	return e
}

// EnvironmentContext renders the grounding block from the host environment.
func EnvironmentContext(workDir string) string {
	return EnvironmentContextFrom(workDir, Env{})
}

// EnvironmentContextFrom renders the grounding block from an explicit Env, with
// empty fields falling back to the host.
func EnvironmentContextFrom(workDir string, env Env) string {
	env = env.resolve()
	return fmt.Sprintf(`
Environment:
- User: %s
- Working directory: %s
- Campaign: %s
- Hostname: %s
- OS: %s
- All file paths are relative to the working directory unless absolute`, env.User, workDir, campaignLine(workDir), env.Hostname, env.OS)
}

// ToolSurfaceContext is the ollama-only tool list. Claude, Cursor, and Grok
// have native tools; listing Obey Voice names there made them invent calls
// they cannot make. On the live route_only path it must not advertise
// web_search — that tool lives on the worker, reached by route(spawn).
func ToolSurfaceContext(cfg *config.Config) string {
	if cfg != nil && !cfg.VoiceToolsEnabled {
		return ""
	}
	policy := RouterToolsRouteOnly
	if cfg != nil {
		policy = NormalizeRouterToolsPolicy(cfg.VoiceRouterTools)
	}
	if policy == RouterToolsLegacy {
		return "\n" + systemPrompt(cfg, "tool-surface-legacy", nil)
	}
	return "\n" + systemPrompt(cfg, "tool-surface-live", nil)
}

// campaignLine grounds the agent in the campaign its working directory belongs
// to. Naming it is the point: a working directory redirected by a stray
// environment variable reads exactly like the one the user launched in, and the
// agent went on to act on another campaign's work items (WI-742a80). Detection
// is cached per directory, so this costs nothing per turn.
func campaignLine(workDir string) string {
	if c := config.CampaignFor(workDir); c.Found() {
		return c.Describe()
	}
	return "none — the working directory is not inside a campaign"
}

// SkillContext renders the progressive-disclosure skills menu for the Ollama
// system prompt: names and descriptions only. Empty catalog yields "".
// Descriptions are re-capped at skills.MaxDescriptionRunes so a pre-built
// catalog cannot blow the system prompt budget.
func SkillContext(catalog []skills.Skill) string {
	if len(catalog) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Agent Skills\n")
	b.WriteString("The harness semantically matches each user request and injects relevant skill instructions in an <activated_skills> block. The catalog below is the discovery fallback. If a relevant skill was not activated automatically, call read_skill yourself before proceeding. You may load multiple relevant skills, and loading a skill never removes other tools.\n\n")
	b.WriteString("Available skills:\n")
	for _, s := range catalog {
		desc := skills.TruncateRunes(strings.TrimSpace(s.Description), skills.MaxDescriptionRunes)
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, desc)
	}
	return b.String()
}
