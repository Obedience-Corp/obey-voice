package brain

// ToolKind is the coarse category of a tool call. The names are Grok's
// (`snake_case`), so a Grok streaming-json adapter and the obey-chat pager can
// consume our events without translating; `route` and `other` are ours.
type ToolKind string

const (
	KindRead      ToolKind = "read"
	KindListDir   ToolKind = "list_dir"
	KindWrite     ToolKind = "write"
	KindExecute   ToolKind = "execute"
	KindWebSearch ToolKind = "web_search"
	KindWebFetch  ToolKind = "web_fetch"
	KindSkill     ToolKind = "skill"
	KindTask      ToolKind = "task"
	KindRoute     ToolKind = "route"
	KindOther     ToolKind = "other"
)

// KindFor maps a tool name to its kind. Unknown names are KindOther.
func KindFor(name string) ToolKind {
	switch name {
	case "read_file":
		return KindRead
	case "list_files":
		return KindListDir
	case "write_file":
		return KindWrite
	case "run_command":
		return KindExecute
	case "web_search":
		return KindWebSearch
	case "fetch_url":
		return KindWebFetch
	case "read_skill", "activate_skill":
		return KindSkill
	case "agent_delegate", "agent_cancel", "agent_delegations":
		return KindTask
	case "route":
		return KindRoute
	default:
		return KindOther
	}
}
