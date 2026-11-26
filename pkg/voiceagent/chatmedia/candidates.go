package chatmedia

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
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

// MaxPictureNames bounds the picture names PictureNames lists.
const MaxPictureNames = 64

// maxDirEntries bounds the entries read from any one directory.
const maxDirEntries = 512

// PictureNames lists PNG, JPEG and GIF files in workDir and in its immediate,
// visible subfolders, as slash-separated paths relative to workDir: the top
// level first, then each subfolder, all in name order, up to MaxPictureNames.
// Names that resolve outside workDir are left out. Files are not decoded; a
// listed file is checked when a reply uses it.
func PictureNames(ctx context.Context, workDir string) []string {
	if workDir == "" || ctx.Err() != nil {
		return nil
	}
	files, dirs := pictureEntries(workDir)
	var names []string
	add := func(rel string) bool {
		if ctx.Err() != nil {
			return false
		}
		resolved, err := resolveLocalFile(rel, workDir, workDir)
		if err != nil {
			return true
		}
		if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
			names = append(names, rel)
		}
		return len(names) < MaxPictureNames
	}
	for _, name := range files {
		if !add(name) {
			return names
		}
	}
	for _, dir := range dirs {
		sub, _ := pictureEntries(filepath.Join(workDir, dir))
		for _, name := range sub {
			if !add(path.Join(dir, name)) {
				return names
			}
		}
	}
	return names
}

// pictureEntries reads up to maxDirEntries entries of dir and returns its
// visible picture names and visible directory names, sorted. A symbolic link
// is listed as a picture when its name is one; its target is checked later.
func pictureEntries(dir string) (files, dirs []string) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	entries, _ := f.ReadDir(maxDirEntries)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.ContainsAny(name, "<>\r\n") {
			continue
		}
		if entry.IsDir() {
			dirs = append(dirs, name)
			continue
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".png", ".jpeg", ".jpg", ".gif":
			files = append(files, name)
		}
	}
	slices.Sort(files)
	slices.Sort(dirs)
	return files, dirs
}
