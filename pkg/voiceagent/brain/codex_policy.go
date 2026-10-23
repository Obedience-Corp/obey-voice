package brain

import (
	"context"
	"fmt"
)

// codexPolicy returns per-thread overrides. The tools-off path must enumerate
// effective named integrations: assigning an empty TOML table would merge with,
// rather than remove, the user's configured MCP servers and plugins.
func codexPolicy(ctx context.Context, c *codexRPC, cwd string, tools bool) (map[string]any, error) {
	cfg := map[string]any{"features.codex_hooks": false, "notify": []string{}}
	if tools {
		cfg["sandbox_workspace_write.writable_roots"] = []string{}
		cfg["sandbox_workspace_write.network_access"] = false
		cfg["sandbox_workspace_write.exclude_tmpdir_env_var"] = true
		cfg["sandbox_workspace_write.exclude_slash_tmp"] = true
		return cfg, nil
	}
	var effective struct {
		Config map[string]any `json:"config"`
	}
	if err := c.call(ctx, "config/read", map[string]any{"includeLayers": false, "cwd": cwd}, &effective, nil); err != nil {
		return nil, fmt.Errorf("read Codex tools-off policy: %w", err)
	}
	for _, feature := range []string{
		"shell_tool", "unified_exec", "apply_patch_freeform", "apps",
		"connectors", "plugins", "multi_agent", "collab",
		"multi_agent_v2", "js_repl", "code_mode", "code_mode_only",
		"computer_use", "browser_use", "browser_use_external", "image_generation",
		"memory_tool", "agent_message_board", "view_image", "imagegenext",
		"tool_suggest", "request_permissions_tool", "search_tool", "runtime_agent_instances",
		"stable_environment_tools", "multi_agent_v2_dynamic_tools", "sleep_tool", "js_repl_tools_only",
		"code_mode_host",
	} {
		cfg["features."+feature] = false
	}
	cfg["web_search"] = "disabled"
	cfg["tools.update_plan.enabled"] = false
	cfg["apps._default.enabled"] = false
	cfg["plugins._default.enabled"] = false
	for _, key := range []string{"mcp_servers", "plugins"} {
		entries, ok := effective.Config[key].(map[string]any)
		if effective.Config[key] != nil && !ok {
			return nil, fmt.Errorf("codex %s configuration is not an object", key)
		}
		for name, value := range entries {
			entry, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("codex %s entry %q is not an object", key, name)
			}
			entry["enabled"] = false
		}
		// Use the complete nested table. app-server's override keys split on
		// dots without TOML quote parsing; a dotted server ID must stay a key.
		if len(entries) > 0 {
			cfg[key] = codexWithoutNulls(entries)
		}
	}
	return cfg, nil
}

// config/read includes null optional fields. JSON null is not a TOML value;
// round-tripping it through thread overrides otherwise becomes an empty string.
func codexWithoutNulls(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			if x != nil {
				out[k] = codexWithoutNulls(x)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, x := range v {
			if x != nil {
				out = append(out, codexWithoutNulls(x))
			}
		}
		return out
	default:
		return value
	}
}
