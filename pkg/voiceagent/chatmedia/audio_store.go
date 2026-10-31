package chatmedia

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

// AudioPath is separate from the image attachment contract. Callers opt into
// this durable store only when the owning session has retain_audio enabled.
func (s Store) AudioPath(ref utterance.AudioRef) string {
	if ref.Validate() != nil || s.Dir == "" {
		return ""
	}
	root, err := canonicalStoreRoot(s.Dir)
	if err != nil {
		return ""
	}
	return filepath.Join(root, "audio", ref.ID+".wav")
}

// Resolve the caller-selected store root once, allowing macOS system aliases
// such as /var. Children inside that selected root remain symlink-rejected.
func canonicalStoreRoot(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	var missing []string
	current := absolute
	for {
		_, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, filepath.Base(current))
			current = filepath.Dir(current)
			continue
		}
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return "", err
		}
		for i := len(missing) - 1; i >= 0; i-- {
			resolved = filepath.Join(resolved, missing[i])
		}
		return resolved, nil
	}
}
func syncAudioDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s Store) PersistAudio(ctx context.Context, ref utterance.AudioRef, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inspected, canonical, err := CanonicalAudio(data)
	if err != nil {
		return err
	}
	if inspected != ref {
		return fmt.Errorf("audio identity differs: %w", utterance.ErrInvalidAudio)
	}
	path := s.AudioPath(ref)
	if path == "" {
		return utterance.ErrInvalidAudio
	}
	dir := filepath.Dir(path)
	if err := privateAudioDir(dir); err != nil {
		return err
	}
	if _, err := s.ReadAudio(ctx, ref); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".audio-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := f.Write(canonical); err != nil {
		return fmt.Errorf("write retained audio: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync retained audio: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Link publishes without replacing an existing immutable asset. A concurrent
	// publisher is accepted only after reading/validating that exact identity.
	if err := os.Link(f.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		_, err = s.ReadAudio(ctx, ref)
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
func privateAudioDir(dir string) error {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	// Validate existing parents before creating children; no symlink traversal.
	var missing []string
	for current := absolute; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, current)
			continue
		}
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("audio media parent must be a real directory")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := syncAudioDirectory(filepath.Dir(missing[i])); err != nil {
			return err
		}
		st, err := os.Lstat(missing[i])
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("audio media directory changed")
		}
	}
	st, err := os.Stat(absolute)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0077 != 0 {
		return errors.New("retained audio directory must be private")
	}
	return nil
}
func (s Store) ReadAudio(ctx context.Context, ref utterance.AudioRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := s.AudioPath(ref)
	if path == "" {
		return nil, utterance.ErrInvalidAudio
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		st, err := os.Lstat(parent)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, utterance.ErrInvalidAudio
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, utterance.ErrInvalidAudio
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(st, opened) {
		return nil, utterance.ErrInvalidAudio
	}
	data, err := io.ReadAll(io.LimitReader(f, utterance.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inspected, canonical, err := CanonicalAudio(data)
	if err != nil {
		return nil, err
	}
	if inspected != ref {
		return nil, utterance.ErrInvalidAudio
	}
	return canonical, nil
}

// PersistAudio authorizes persistence through the live owning session. The
// caller must gate this explicit operation on retain_audio before saving the
// canonical reference. It does not release the live turn's memory ownership.
func (s *AudioSession) PersistAudio(ctx context.Context, store Store, ref utterance.AudioRef) error {
	data, err := s.ResolveAudio(ctx, ref)
	if err != nil {
		return err
	}
	return store.PersistAudio(ctx, ref, data)
}

// RestoreAudio explicitly loads a retained canonical reference into a new
// session's bounded ownership. Ordinary ResolveAudio never searches disk.
func (s *AudioSession) RestoreAudio(ctx context.Context, store Store, ref utterance.AudioRef) error {
	data, err := store.ReadAudio(ctx, ref)
	if err != nil {
		return err
	}
	restored, err := s.Import(ctx, data)
	if err != nil {
		return err
	}
	if restored != ref {
		s.Release(restored)
		return utterance.ErrInvalidAudio
	}
	return nil
}
