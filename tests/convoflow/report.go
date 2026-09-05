package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func writeReport(e *engine) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# convoflow %s\n\n", e.scn.Name)
	fmt.Fprintf(&b, "%s\n\n", e.scn.Description)
	fmt.Fprintf(&b, "- run dir: `%s`\n", e.runDir)
	fmt.Fprintf(&b, "- turns: %d\n", len(e.scn.Turns))
	fmt.Fprintf(&b, "- events: %d\n", len(e.rec.snapshot()))
	return os.WriteFile(filepath.Join(e.runDir, "report.md"), []byte(b.String()), 0o644)
}
