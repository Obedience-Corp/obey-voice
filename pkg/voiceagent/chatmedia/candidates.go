package chatmedia

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// ReplyCandidates finds usable alternatives after a failed image reply. Check
// only visible, regular files immediately inside the agent's working directory.
// Bound directory reads and image decodes so repair cannot become a workspace crawl.
func (s Store) ReplyCandidates(ctx context.Context, workDir string) []string {
	if workDir == "" || ctx.Err() != nil {
		return nil
	}
	dir, err := os.Open(workDir)
	if err != nil {
		return nil
	}
	defer dir.Close()
	var paths []string
	checked := 0
	for batch := 0; batch < 16; batch++ {
		entries, err := dir.ReadDir(64)
		for _, entry := range entries {
			if ctx.Err() != nil {
				return nil
			}
			name := entry.Name()
			if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.ContainsAny(name, "<>\r\n") {
				continue
			}
			switch strings.ToLower(filepath.Ext(name)) {
			case ".png", ".jpeg", ".jpg", ".gif":
			default:
				continue
			}
			resolved, resolveErr := s.resolveReplyFile(name, workDir)
			if resolveErr != nil {
				continue
			}
			if _, inspectErr := inspectReplyFile(resolved); inspectErr == nil {
				paths = append(paths, name)
			}
			checked++
			if len(paths) == MaxAttachments || checked == 16 {
				return paths
			}
		}
		if err != nil || len(entries) == 0 {
			break
		}
	}
	return paths
}
