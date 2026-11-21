package config

import (
	"fmt"
	"slices"
	"strings"
)

// RouterToolsValues lists the voice_router_tools values. legacy is accepted
// as an alias for direct but is not offered.
func RouterToolsValues() []string {
	return []string{"direct", "route_only", "json_route", "single_call"}
}

// ValidateRouter accepts an empty persona brain.router or a
// voice_router_tools value.
func ValidateRouter(value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "legacy" || slices.Contains(RouterToolsValues(), value) {
		return nil
	}
	return fmt.Errorf("brain.router %q: use one of %s (empty takes voice_router_tools)", value, strings.Join(RouterToolsValues(), ", "))
}
