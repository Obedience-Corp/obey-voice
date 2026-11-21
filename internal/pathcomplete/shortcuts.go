package pathcomplete

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultShortcuts are camp's default navigation shortcuts, used when a
// campaign has no jumps.yaml or no shortcuts block in it.
func DefaultShortcuts() map[string]string {
	return map[string]string{
		"p":        "projects/",
		"f":        "festivals/",
		"i":        ".campaign/intents/",
		"wt":       "projects/worktrees/",
		"w":        "workflow/",
		"d":        "docs/",
		"du":       "dungeon/",
		"r":        "workflow/reviews/",
		"de":       "workflow/design/",
		"ex":       "workflow/explore/",
		"settings": ".campaign/",
		"cfg":      ".campaign/",
	}
}

// LoadShortcuts reads the navigation shortcuts of the campaign at root from
// .campaign/settings/jumps.yaml. As in camp, a shortcuts block replaces the
// defaults entirely, and entries without a path (command-only shortcuts) are
// left out. A missing or unreadable file, or one without the block, yields the
// defaults.
func LoadShortcuts(root string) map[string]string {
	data, err := os.ReadFile(filepath.Join(root, ".campaign", "settings", "jumps.yaml"))
	if err != nil {
		return DefaultShortcuts()
	}
	var doc struct {
		Shortcuts map[string]struct {
			Path string `yaml:"path"`
		} `yaml:"shortcuts"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Shortcuts == nil {
		return DefaultShortcuts()
	}
	out := make(map[string]string, len(doc.Shortcuts))
	for key, entry := range doc.Shortcuts {
		path := strings.TrimSpace(entry.Path)
		key = strings.TrimSpace(key)
		if path == "" || key == "" {
			continue
		}
		if !strings.HasSuffix(path, "/") {
			path += "/"
		}
		out[key] = path
	}
	return out
}
