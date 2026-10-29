package config

func decisionKeys() []KeySpec {
	return []KeySpec{
		{Key: "decision_enabled", Type: TypeBool, Default: false, Group: GroupBrain, Title: "Decision shortcuts (experimental)", Help: "Use ready decisions to skip some speaking-model calls; uncertain or late decisions fall back immediately."},
		{Key: "decision_voice_work", Type: TypeBool, Default: true, Group: GroupBrain, Title: "Decide voice work", Help: "Allow optional worker decisions during voice input admission."},
		{Key: "decision_text_work", Type: TypeBool, Default: true, Group: GroupBrain, Title: "Decide typed work", Help: "Allow optional worker decisions during typed input admission."},
		{Key: "decision_provider", Type: TypeEnum, Default: "local", Enum: []string{"local", "jev", "openai"}, Group: GroupBrain, Title: "Decision provider", Help: "Local keeps inference on this computer. Jev and OpenAI send eligible messages and recent conversation to the selected service."},
		{Key: "decision_runtime", Type: TypeEnum, Default: "managed", Enum: []string{"managed", "external"}, Group: GroupBrain, Title: "Local decision runtime", Help: "Managed installs and starts the local model. External uses an existing loopback server."},
		{Key: "decision_local_model", Type: TypeEnum, Default: "2b", Enum: []string{"2b", "0.8b"}, Group: GroupBrain, Title: "Local decision model", Help: "2b is the tested route-choice candidate; 0.8b uses less memory and remains experimental."},
		{Key: "decision_endpoint", Type: TypeString, Default: "", Group: GroupBrain, Title: "Decision server", Help: "Optional external local /v1/systemone endpoint; must be loopback HTTP. Hosted providers use their official endpoints."},
		{Key: "decision_model", Type: TypeString, Default: "", Group: GroupBrain, Title: "Hosted decision model", Help: "Explicit model name required for Jev or OpenAI; empty uses decider for local inference."},
		{Key: "decision_api_key", Type: TypeString, Default: "", Secret: true, Group: GroupHidden, Title: "Decision API key", Help: "Credential used only for an explicitly selected hosted decision provider."},
		{Key: "decision_budget_ms", Type: TypeInt, Default: 750, Min: num(25), Max: num(3000), Unit: "ms", Group: GroupBrain, Title: "Decision deadline", Help: "Cancel optional inference after this deadline. The conversation never waits for it."},
		{Key: "decision_policy", Type: TypeEnum, Default: "route_choice", Enum: []string{"route_choice", "intent_and_tier"}, Group: GroupBrain, Title: "Decision questions", Help: "Choose the frozen routing policy; intent_and_tier retains the original experiment for comparison."},
	}
}
