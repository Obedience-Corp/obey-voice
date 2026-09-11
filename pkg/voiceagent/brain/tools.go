package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/skills"
)

// maxToolIterations prevents infinite tool call loops.
const maxToolIterations = 10

// skillBodyMaxBytes caps read_skill output (same budget as read_file).
const skillBodyMaxBytes = 32 * 1024

// defaultToolCommandTimeout bounds one shell command when no provider config
// is available (for example, in a direct unit-test tool session).
const defaultToolCommandTimeout = 30 * time.Second

// toolSession holds the skill catalog and UI hooks for one Think* turn.
// Skills add instructions; they never remove tools. The global
// voice_tools_enabled / remote_tools_enabled gates remain the authority for
// whether tools are available at all.
type toolSession struct {
	delegation *delegation.Runtime
	catalog    []skills.Skill
	// routerPolicy selects live route_only vs legacy full tools. Workers use legacy.
	routerPolicy RouterToolsPolicy
	// commandTimeout bounds each run_command call in this session.
	commandTimeout time.Duration
	// boundary fences file and shell tools to the worker's campaign root.
	boundary Boundary
	// Optional UI hooks (from StreamOptions).
	onStart func(ToolCall)
	onEnd   func(ToolResult)
	// callSeq synthesizes ids per tool name for models that send none.
	callSeq map[string]int
	// hookIDs contains ids already exposed through the UI hooks. Provider call
	// ids are allowed to repeat across malformed/retried tool calls, while hook
	// ids must remain unique so folding cannot merge distinct invocations.
	hookIDs map[string]struct{}
}

// nextCallID returns the next synthesized id for name ("read_file#2").
func (s *toolSession) nextCallID(name string) string {
	if s.callSeq == nil {
		s.callSeq = make(map[string]int)
	}
	for {
		s.callSeq[name]++
		candidate := fmt.Sprintf("%s#%d", name, s.callSeq[name])
		if s.reserveHookID(candidate) {
			return candidate
		}
	}
}

func (s *toolSession) reserveHookID(candidate string) bool {
	if s.hookIDs == nil {
		s.hookIDs = make(map[string]struct{})
	}
	if _, exists := s.hookIDs[candidate]; exists {
		return false
	}
	s.hookIDs[candidate] = struct{}{}
	return true
}

func (s *toolSession) uniqueProviderHookID(name, raw string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		return s.nextCallID(name)
	}
	if s.reserveHookID(base) {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s#%d", base, suffix)
		if s.reserveHookID(candidate) {
			return candidate
		}
	}
}

// describe builds the hook payload for one call, synthesizing an id when the
// model did not send one.
func (s *toolSession) describe(name, summary, id string) ToolCall {
	id = s.uniqueProviderHookID(name, id)
	return ToolCall{ID: id, Name: name, Kind: KindFor(name), Summary: redactToolText(summary, 80)}
}

// tools returns every enabled tool definition for the next model request.
// Agent Skills allowed-tools metadata is intentionally not a runtime filter:
// local Ollama should behave like a general harness and may load more than one
// relevant skill without losing capabilities on the legacy worker path.
func (s *toolSession) tools() api.Tools {
	tools := RouterToolsForSession(s.routerPolicy, s.catalog)
	if s.delegation != nil {
		tools = append(tools, delegationTools()...)
	}
	return tools
}

// execute runs a tool call. read_skill returns instructions without changing
// the tools available on subsequent model requests.
// onEnd always runs (via defer) so a slow or failed tool cannot leave the TUI
// stuck on "🔧 tool..." without a finish event.
func (s *toolSession) execute(ctx context.Context, workDir string, call api.ToolCall) (result string) {
	name := call.Function.Name
	started := s.describe(name, toolArgSummary(call), call.ID)
	if s.onStart != nil {
		s.onStart(started)
	}
	begun := time.Now()
	var observed *events.ToolOutcome
	defer func() {
		elapsed := time.Since(begun)
		preview, errText := toolResultPreview(result), toolResultError(result)
		if observed != nil {
			observed.Elapsed = elapsed
			preview = observed.OutputPreview
			errText = ""
			if observed.Status == events.ToolOutcomeFailed || observed.Status == events.ToolOutcomeTimedOut || observed.Status == events.ToolOutcomeCanceled {
				errText = "error: " + observed.Reason
			}
		}
		if s.onEnd != nil {
			s.onEnd(ToolResult{
				ToolCall: started,
				Preview:  preview,
				Err:      errText,
				Elapsed:  elapsed,
				Outcome:  observed,
			})
		}
	}()
	if name == "agent_delegate" || name == "agent_cancel" || name == "agent_delegations" {
		return s.executeDelegation(call)
	}
	if name == "read_skill" {
		return s.executeReadSkill(call)
	}
	if s.routerPolicy == RouterToolsRouteOnly && name != "route" {
		observed = failedToolOutcome(started.Summary, fmt.Sprintf("tool %q is not available on the live router", name))
		return fmt.Sprintf("error: tool %q is not available on the live router", name)
	}
	if err := s.boundary.check(workDir, call); err != nil {
		observed = failedToolOutcome(started.Summary, err.Error())
		return "error: " + err.Error()
	}
	result, observed = executeToolWithOutcome(ctx, workDir, call, s.catalog, s.commandTimeout)
	return result
}

// toolArgSummary is a short, non-sensitive description of tool arguments.
func toolArgSummary(call api.ToolCall) string {
	args := call.Function.Arguments.ToMap()
	switch call.Function.Name {
	case "list_files", "read_file", "write_file":
		if p, _ := args["path"].(string); p != "" {
			return p
		}
	case "run_command":
		if c, _ := args["command"].(string); c != "" {
			return redactToolText(c, 80)
		}
	case "web_search":
		if q, _ := args["query"].(string); q != "" {
			return q
		}
	case "fetch_url":
		if u, _ := args["url"].(string); u != "" {
			return u
		}
	case "read_skill":
		if n, _ := args["name"].(string); n != "" {
			return n
		}
	case "route":
		if k, _ := args["kind"].(string); k != "" {
			return k
		}
	}
	return ""
}

// toolResultError returns the leading "error: …" line when a tool result is an
// error, and "" otherwise. Tool handlers report failure in the result string,
// so this is the only place the distinction exists.
func toolResultError(result string) string {
	result = strings.TrimSpace(result)
	if !strings.HasPrefix(result, "error:") {
		return ""
	}
	if i := strings.IndexByte(result, '\n'); i >= 0 {
		result = result[:i]
	}
	return strings.TrimSpace(result)
}

func toolResultPreview(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return "(empty)"
	}
	// One line, short.
	if i := strings.IndexByte(result, '\n'); i >= 0 {
		result = result[:i] + "…"
	}
	return skills.TruncateRunes(redactToolText(result, 240), 240)
}

var sensitiveToolText = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key)(["']?\s*[:=]\s*|\s+)("[^"]*"|'[^']*'|[^\s,;&}]+)`)
var toolAuthorization = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?(?:bearer|basic)\s+)[^\s"',;}]+`)

// redactToolText bounds user-visible diagnostics and removes common
// key/value credentials. Tool output is still arbitrary provider data; this
// is a bounded best-effort redaction, not a secret-free guarantee.
func redactToolText(text string, maxRunes int) string {
	text = sensitiveToolText.ReplaceAllString(text, `$1$2[redacted]`)
	text = toolAuthorization.ReplaceAllString(text, `$1[redacted]`)
	return skills.TruncateRunes(text, maxRunes)
}

func failedToolOutcome(input, reason string) *events.ToolOutcome {
	return &events.ToolOutcome{Status: events.ToolOutcomeFailed, InputSummary: redactToolText(input, 80), Reason: reason}
}

func (s *toolSession) executeReadSkill(call api.ToolCall) string {
	args := call.Function.Arguments.ToMap()
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "error: name is required"
	}
	for i := range s.catalog {
		sk := &s.catalog[i]
		if sk.Name != name {
			continue
		}
		body := sk.Body
		if len(body) > skillBodyMaxBytes {
			body = body[:skillBodyMaxBytes] + "\n... (truncated)"
		}
		msg := fmt.Sprintf("Skill %q (directory: %s)\n\n%s", sk.Name, sk.Dir, body)
		return msg
	}
	return fmt.Sprintf("error: unknown skill %q", name)
}

// orderedProps builds a tool schema whose properties always serialize in the
// same order. Ollama renders the tool JSON at the front of the prompt, and
// api.ToolPropertiesMap keeps insertion order, so feeding it from a Go map
// shuffled the schema on every request. On qwen3.8 that turned every turn
// into a cold prompt (~5 s to first token) because the prefix no longer
// matched the cached one.
func orderedProps(fields map[string]api.ToolProperty) *api.ToolPropertiesMap {
	m := api.NewToolPropertiesMap()
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		m.Set(k, fields[k])
	}
	return m
}

// voiceAssistantTools returns the tool definitions for the Ollama agent.
// When catalog is non-empty, read_skill is included so the model can load a
// skill body on demand (progressive disclosure).
func voiceAssistantTools(catalog []skills.Skill) api.Tools {
	props := orderedProps

	tools := api.Tools{
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "list_files",
				Description: "List files and directories at a given path. Returns names with / suffix for directories.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"path"},
					Properties: props(map[string]api.ToolProperty{
						"path": {
							Type:        api.PropertyType{"string"},
							Description: "Directory path to list. Use '.' for current directory.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "read_file",
				Description: "Read the contents of a file. Returns the file text.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"path"},
					Properties: props(map[string]api.ToolProperty{
						"path": {
							Type:        api.PropertyType{"string"},
							Description: "File path to read.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "write_file",
				Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"path", "content"},
					Properties: props(map[string]api.ToolProperty{
						"path": {
							Type:        api.PropertyType{"string"},
							Description: "File path to write.",
						},
						"content": {
							Type:        api.PropertyType{"string"},
							Description: "Content to write to the file.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "run_command",
				Description: "Execute a shell command and return stdout and stderr. Runs in the user's working directory.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"command"},
					Properties: props(map[string]api.ToolProperty{
						"command": {
							Type:        api.PropertyType{"string"},
							Description: "Shell command to execute.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "web_search",
				Description: "Search the public web and return result titles, URLs, and snippets. Use this for current information instead of inventing a shell search command.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"query"},
					Properties: props(map[string]api.ToolProperty{
						"query": {
							Type:        api.PropertyType{"string"},
							Description: "Search query.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "fetch_url",
				Description: "Fetch an HTTP or HTTPS web page and return readable text. Use it to inspect a URL returned by web_search.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"url"},
					Properties: props(map[string]api.ToolProperty{
						"url": {
							Type:        api.PropertyType{"string"},
							Description: "Absolute HTTP or HTTPS URL to read.",
						},
					}),
				},
			},
		},
	}

	if len(catalog) > 0 {
		tools = append(tools, api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "read_skill",
				Description: "Load the full instructions for a named Agent Skill when semantic routing did not already activate a relevant skill, or when the user explicitly requests one. You may load multiple relevant skills, and all tools remain available. Bundled scripts live under the returned skill directory.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"name"},
					Properties: props(map[string]api.ToolProperty{
						"name": {
							Type:        api.PropertyType{"string"},
							Description: "Skill name as advertised in the Available skills list.",
						},
					}),
				},
			},
		})
	}
	return tools
}

// liveRouterTools returns the constrained route tool for the conversational model.
func liveRouterTools() api.Tools {
	props := orderedProps
	return api.Tools{
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "route",
				Description: "Classify this user turn. Use spawn when the user asks you to do something: search the web, look something up, read or write files, run commands, or build. Spawn starts a worker that has those tools. Use narrate for long reading. Otherwise chat.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"kind"},
					Properties: props(map[string]api.ToolProperty{
						"kind": {
							Type:        api.PropertyType{"string"},
							Description: "One of: chat, spawn, narrate.",
							Enum:        []any{"chat", "spawn", "narrate"},
						},
						"source": {
							Type:        api.PropertyType{"string"},
							Description: "Optional URL or file path when kind is narrate.",
						},
						"goal": {
							Type:        api.PropertyType{"string"},
							Description: "Optional summary of work when kind is spawn.",
						},
					}),
				},
			},
		},
	}
}

// ParseRouteKind normalizes a route tool kind. Invalid values fall through to chat.
func ParseRouteKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "spawn", "narrate":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "chat"
	}
}

// executeTool runs a tool call and returns the result as a string.
// catalog is used by read_skill; other tools ignore it.
// Prefer toolSession.execute so skill activation and policy are tracked.
func executeTool(ctx context.Context, workDir string, call api.ToolCall, catalog []skills.Skill) string {
	return executeToolWithTimeout(ctx, workDir, call, catalog, 0)
}

func executeToolWithTimeout(ctx context.Context, workDir string, call api.ToolCall, catalog []skills.Skill, commandTimeout time.Duration) string {
	result, _ := executeToolWithOutcome(ctx, workDir, call, catalog, commandTimeout)
	return result
}

func executeToolWithOutcome(ctx context.Context, workDir string, call api.ToolCall, catalog []skills.Skill, commandTimeout time.Duration) (string, *events.ToolOutcome) {
	args := call.Function.Arguments.ToMap()

	switch call.Function.Name {
	case "list_files":
		return toolListFiles(workDir, args), nil
	case "read_file":
		return toolReadFile(workDir, args), nil
	case "write_file":
		return toolWriteFile(workDir, args), nil
	case "run_command":
		return toolRunCommandWithTimeoutOutcome(ctx, workDir, args, commandTimeout)
	case "web_search":
		return toolWebSearch(ctx, args), nil
	case "fetch_url":
		return toolFetchURL(ctx, args), nil
	case "read_skill":
		// Without a session, activation is not tracked (tests / legacy).
		return toolReadSkill(catalog, args), nil
	case "route":
		return toolRoute(args), nil
	default:
		return fmt.Sprintf("unknown tool: %s", call.Function.Name), nil
	}
}

// toolReadSkill returns the capped skill body and skill directory for scripts.
// Does not track activation; use toolSession.execute for allow-list lifecycle.
func toolReadSkill(catalog []skills.Skill, args map[string]any) string {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "error: name is required"
	}
	for _, s := range catalog {
		if s.Name != name {
			continue
		}
		body := s.Body
		if len(body) > skillBodyMaxBytes {
			body = body[:skillBodyMaxBytes] + "\n... (truncated)"
		}
		return fmt.Sprintf("Skill %q (directory: %s)\n\n%s", s.Name, s.Dir, body)
	}
	return fmt.Sprintf("error: unknown skill %q", name)
}

func toolRoute(args map[string]any) string {
	kind, _ := args["kind"].(string)
	kind = ParseRouteKind(kind)
	resp, err := json.Marshal(map[string]string{"route": kind})
	if err != nil {
		return `{"route":"chat"}`
	}
	return string(resp)
}

func resolvePath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workDir, path)
}

func toolListFiles(workDir string, args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	resolved := resolvePath(workDir, path)

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	return strings.Join(names, "\n")
}

func toolReadFile(workDir string, args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "error: path is required"
	}
	resolved := resolvePath(workDir, path)

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	// Limit output to prevent context overflow.
	const maxBytes = 32 * 1024
	if len(data) > maxBytes {
		return string(data[:maxBytes]) + "\n... (truncated)"
	}
	return string(data)
}

func toolWriteFile(workDir string, args map[string]any) string {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return "error: path is required"
	}
	resolved := resolvePath(workDir, path)

	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("error creating directory: %v", err)
	}

	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path)
}

// clampCommandTimeout caps one run_command at 120s. Zero/negative fall back to
// the default 30s. Sub-second values are allowed (tests / callers with short
// bounds); user config is clamped to whole seconds in config.ClampToolCommandTimeout.
func clampCommandTimeout(timeout time.Duration) time.Duration {
	const maxTimeout = 120 * time.Second
	if timeout <= 0 {
		return defaultToolCommandTimeout
	}
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}
