package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	// Optional UI hooks (from StreamOptions).
	onStart func(ToolCall)
	onEnd   func(ToolResult)
	// callSeq synthesizes ids per tool name for models that send none.
	callSeq map[string]int
}

// nextCallID returns the next synthesized id for name ("read_file#2").
func (s *toolSession) nextCallID(name string) string {
	if s.callSeq == nil {
		s.callSeq = make(map[string]int)
	}
	s.callSeq[name]++
	return fmt.Sprintf("%s#%d", name, s.callSeq[name])
}

// describe builds the hook payload for one call, synthesizing an id when the
// model did not send one.
func (s *toolSession) describe(name, summary, id string) ToolCall {
	if strings.TrimSpace(id) == "" {
		id = s.nextCallID(name)
	}
	return ToolCall{ID: id, Name: name, Kind: KindFor(name), Summary: summary}
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
	defer func() {
		if s.onEnd != nil {
			s.onEnd(ToolResult{
				ToolCall: started,
				Preview:  toolResultPreview(result),
				Err:      toolResultError(result),
				Elapsed:  time.Since(begun),
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
		return fmt.Sprintf("error: tool %q is not available on the live router", name)
	}
	return executeToolWithTimeout(ctx, workDir, call, s.catalog, s.commandTimeout)
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
			if len(c) > 60 {
				return c[:60] + "…"
			}
			return c
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
	if len(result) > 80 {
		return result[:80] + "…"
	}
	return result
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

// voiceAssistantTools returns the tool definitions for the Ollama agent.
// When catalog is non-empty, read_skill is included so the model can load a
// skill body on demand (progressive disclosure).
func voiceAssistantTools(catalog []skills.Skill) api.Tools {
	props := func(fields map[string]api.ToolProperty) *api.ToolPropertiesMap {
		m := api.NewToolPropertiesMap()
		for k, v := range fields {
			m.Set(k, v)
		}
		return m
	}

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
	props := func(fields map[string]api.ToolProperty) *api.ToolPropertiesMap {
		m := api.NewToolPropertiesMap()
		for k, v := range fields {
			m.Set(k, v)
		}
		return m
	}
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
	args := call.Function.Arguments.ToMap()

	switch call.Function.Name {
	case "list_files":
		return toolListFiles(workDir, args)
	case "read_file":
		return toolReadFile(workDir, args)
	case "write_file":
		return toolWriteFile(workDir, args)
	case "run_command":
		return toolRunCommandWithTimeout(ctx, workDir, args, commandTimeout)
	case "web_search":
		return toolWebSearch(ctx, args)
	case "fetch_url":
		return toolFetchURL(ctx, args)
	case "read_skill":
		// Without a session, activation is not tracked (tests / legacy).
		return toolReadSkill(catalog, args)
	case "route":
		return toolRoute(args)
	default:
		return fmt.Sprintf("unknown tool: %s", call.Function.Name)
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

func toolRunCommandWithTimeout(ctx context.Context, workDir string, args map[string]any, timeout time.Duration) string {
	command, _ := args["command"].(string)
	if command == "" {
		return "error: command is required"
	}

	timeout = clampCommandTimeout(timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir
	configureCommandCancellation(cmd)
	// Bound pipe draining if a descendant escapes cancellation. On Unix the
	// process-group cancellation handles the normal shell-and-children case;
	// WaitDelay is the portable final guard.
	cmd.WaitDelay = 100 * time.Millisecond

	output, err := cmd.CombinedOutput()
	result := string(output)
	if err != nil {
		result += fmt.Sprintf("\nexit error: %v", err)
	}

	// Limit output.
	const maxBytes = 16 * 1024
	if len(result) > maxBytes {
		result = result[:maxBytes] + "\n... (truncated)"
	}

	// Return as JSON for structured parsing.
	resp, _ := json.Marshal(map[string]string{
		"output":  result,
		"command": command,
	})
	return string(resp)
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
