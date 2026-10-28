package brain

import (
	"encoding/json"
	"strings"
)

func addresseeNames(participants []GroupParticipant) []string {
	names := []string{""}
	for _, p := range participants {
		for _, name := range append([]string{p.Name}, p.Aliases...) {
			name = strings.TrimSpace(name)
			if !addresseeNameAllowed(name, names) {
				names = append(names, name)
			}
		}
	}
	return names
}

func addresseeNameAllowed(name string, names []string) bool {
	for _, candidate := range names {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}

func addresseeFormat(names []string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"none", "address", "ambiguous"}},
			"name": map[string]any{"type": "string", "enum": names},
			"bare": map[string]any{"type": "boolean"},
		},
		"required": []string{"kind", "name", "bare"},
	})
}
