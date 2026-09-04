package config

func delegationKeys() []KeySpec {
	return []KeySpec{
		{Key: "delegation_enabled", Type: TypeBool, Default: false, Group: GroupTools, Title: "Background tasks", Help: "Lets Ollama delegate work to Obey and summarize finished results on your next turn."},
		{Key: "delegation_binary", Type: TypeString, Default: "obey", Group: GroupAdvanced, Title: "Obey executable", Help: "Executable providing managed delegation commands."},
		{Key: "delegation_socket", Type: TypeString, Default: "", Group: GroupAdvanced, Title: "Obey socket", Help: "Local Unix socket; empty uses Obey's configured socket."},
		{Key: "delegation_campaign_id", Type: TypeString, Default: "", Group: GroupAdvanced, Title: "Delegation campaign", Help: "Existing Obey campaign that owns background tasks."},
		{Key: "delegation_parent_session_id", Type: TypeString, Default: "", Group: GroupAdvanced, Title: "Delegation parent", Help: "Existing Obey parent session whose grants authorize background tasks."},
		{Key: "delegation_profile", Type: TypeString, Default: "", Group: GroupAdvanced, Title: "Worker profile", Help: "Operator-approved managed-child profile in Obey."},
		{Key: "delegation_timeout", Type: TypeInt, Default: 300, Min: num(1), Max: num(3600), Unit: "seconds", Group: GroupAdvanced, Title: "Background task timeout", Help: "Requests cancellation when a background task exceeds this duration."},
	}
}
