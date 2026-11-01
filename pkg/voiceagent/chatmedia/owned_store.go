package chatmedia

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrOwnedMediaPath = errors.New("session media must remain beneath its selected store without symlinks")

// OwnedSubdir derives a session-owned namespace from a caller-selected root.
// System/selected aliases resolve once at the root; every child remains checked
// on later audio operations, including after directories are created or replaced.
// It performs no filesystem writes.
func (s Store) OwnedSubdir(parts ...string) (Store, error) {
	if s.Dir == "" || len(parts) == 0 {
		return Store{}, ErrOwnedMediaPath
	}
	root, err := s.audioRoot()
	if err != nil {
		return Store{}, err
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) || filepath.IsAbs(part) {
			return Store{}, ErrOwnedMediaPath
		}
	}
	anchor := s.anchor
	if anchor == "" {
		anchor = root
	}
	derived := Store{Dir: filepath.Join(append([]string{root}, parts...)...), anchor: anchor}
	if _, err := derived.audioRoot(); err != nil {
		return Store{}, err
	}
	return derived, nil
}

func (s Store) audioRoot() (string, error) {
	if s.anchor == "" {
		return canonicalStoreRoot(s.Dir)
	}
	root := filepath.Clean(s.Dir)
	relative, err := filepath.Rel(s.anchor, root)
	if err != nil || !filepath.IsAbs(root) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrOwnedMediaPath
	}
	// The canonical anchor already resolved legitimate system aliases. Reject
	// subsequently introduced links in both the selected namespace and children.
	for current := root; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", ErrOwnedMediaPath
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return root, nil
}

// RemoveOwned removes only an explicitly derived namespace. Direct selected
// roots cannot be removed through this API. RemoveAll does not follow links
// inside the owned directory; other session namespaces and attachment stores
// remain outside this operation.
func (s Store) RemoveOwned() error {
	if s.anchor == "" {
		return ErrOwnedMediaPath
	}
	root, err := s.audioRoot()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove retained session media: %w", err)
	}
	if err := syncAudioDirectory(filepath.Dir(root)); err != nil {
		return fmt.Errorf("sync removed session media: %w", err)
	}
	return nil
}
