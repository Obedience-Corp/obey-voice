package decider

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

func defaultDevice() string { d, _ := DeviceFor(runtime.GOOS, runtime.GOARCH, "auto", false); return d }
func NativePath(modelsDir, device string) string {
	return filepath.Join(RuntimeDir(modelsDir, device), "llama-server")
}

type installedNative struct {
	Revision string            `json:"revision"`
	Archive  string            `json:"archive"`
	CUDA     string            `json:"cuda,omitempty"`
	Files    map[string]string `json:"files"`
}

func fileHash(ctx context.Context, name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 128<<10)
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		n, e := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
func NativeReadyFor(modelsDir, model, device string) bool {
	return verifyNative(context.Background(), modelsDir, model, device) == nil
}
func verifyNative(ctx context.Context, modelsDir, model, device string) error {
	if _, err := ModelFor(model); err != nil {
		return err
	}
	a, err := NativeAsset(runtime.GOOS, runtime.GOARCH, device)
	if err != nil {
		return err
	}
	root := RuntimeDir(modelsDir, device)
	raw, err := os.ReadFile(filepath.Join(root, "installed.json"))
	if err != nil {
		return fmt.Errorf("native runtime setup required: %w", err)
	}
	var m installedNative
	if json.Unmarshal(raw, &m) != nil || m.Revision != RuntimeRevision || m.Archive != a.SHA256 || len(m.Files) == 0 || m.Files["llama-server"] == "" {
		return fmt.Errorf("invalid native runtime installation")
	}
	if a.CUDA != nil && m.CUDA != a.CUDA.SHA256 {
		return fmt.Errorf("native CUDA runtime setup required")
	}
	for name, want := range m.Files {
		if filepath.Base(name) != name {
			return fmt.Errorf("invalid native file name")
		}
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("native file unavailable: %s", name)
		}
		got, err := fileHash(ctx, filepath.Join(root, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("native file checksum mismatch: %s", name)
		}
	}
	info, err := os.Stat(NativePath(modelsDir, device))
	if err != nil {
		return err
	}
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("native worker is not executable")
	}
	return nil
}

// EnsureNativeFor consumes only already-downloaded, pinned native archives.
// There is no interpreter, package manager, conversion or source build here.
func EnsureNativeFor(ctx context.Context, modelsDir, model, device string, progress func(string)) error {
	if _, err := ModelFor(model); err != nil {
		return err
	}
	device, err := ResolveDevice(ctx, device)
	if err != nil {
		return err
	}
	a, err := NativeAsset(runtime.GOOS, runtime.GOARCH, device)
	if err != nil {
		return err
	}
	parent := filepath.Join(modelsDir, "decider")
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	release, err := installLock(ctx, filepath.Join(parent, "native-install.lock"))
	if err != nil {
		return err
	}
	defer release()
	if verifyNative(ctx, modelsDir, model, device) == nil {
		return nil
	}
	if progress != nil {
		progress("Installing verified native decision runtime")
	}
	stage, err := os.MkdirTemp(parent, ".native-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, asset := range []*RuntimeAsset{&a, a.CUDA} {
		if asset == nil {
			continue
		}
		archive := RuntimeArchivePath(modelsDir, *asset)
		got, err := fileHash(ctx, archive)
		if err != nil {
			return err
		}
		if got != asset.SHA256 {
			return fmt.Errorf("native archive checksum mismatch: %s", asset.Name)
		}
		target := stage
		if asset == a.CUDA {
			target, err = os.MkdirTemp(parent, ".cuda-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(target)
		}
		if err = extractNative(ctx, archive, target); err != nil {
			return err
		}
		if target != stage {
			entries, err := os.ReadDir(target)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				name := entry.Name()
				if name == "LICENSE" {
					name = "CUDA-LICENSE"
				}
				dst := filepath.Join(stage, name)
				if _, err = os.Lstat(dst); err == nil {
					return fmt.Errorf("duplicate CUDA runtime file %s", name)
				} else if !os.IsNotExist(err) {
					return err
				}
				if err = os.Rename(filepath.Join(target, entry.Name()), dst); err != nil {
					return err
				}
			}
		}
	}
	files := map[string]string{}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		hash, err := fileHash(ctx, filepath.Join(stage, entry.Name()))
		if err != nil {
			return err
		}
		files[entry.Name()] = hash
	}
	if files["llama-server"] == "" {
		return fmt.Errorf("native archive lacks llama-server")
	}
	installed := installedNative{Revision: RuntimeRevision, Archive: a.SHA256, Files: files}
	if a.CUDA != nil {
		installed.CUDA = a.CUDA.SHA256
	}
	marker, err := json.Marshal(installed)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(stage, "installed.json"), marker, 0600); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	root := RuntimeDir(modelsDir, device)
	old := root + ".previous"
	if err = os.RemoveAll(old); err != nil {
		return err
	}
	if _, err = os.Stat(root); err == nil {
		if err = os.Rename(root, old); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(stage, root); err != nil {
		_ = os.Rename(old, root)
		return fmt.Errorf("publish native runtime: %w", err)
	}
	return os.RemoveAll(old)
}

// Upstream archives contain shared-library links. Materialize only links to
// regular files within the verified archive instead of creating filesystem
// symlinks. All runtime libraries live alongside the worker for relocation.
func extractNative(ctx context.Context, archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	links := map[string]string{}
	seen := map[string]bool{}
	total := int64(0)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		clean := path.Clean(h.Name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("unsafe native archive path")
		}
		if h.Typeflag == tar.TypeDir || h.Typeflag == tar.TypeXGlobalHeader || h.Typeflag == tar.TypeXHeader {
			continue
		}
		name := path.Base(clean)
		if name != "llama-server" && !strings.HasPrefix(name, "lib") && name != "LICENSE" {
			continue
		}
		if seen[name] {
			return fmt.Errorf("duplicate native archive file %s", name)
		}
		seen[name] = true
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			target := path.Clean(path.Join(path.Dir(clean), h.Linkname))
			if h.Typeflag == tar.TypeLink {
				target = path.Clean(h.Linkname)
			}
			if path.IsAbs(h.Linkname) || target == ".." || strings.HasPrefix(target, "../") {
				return fmt.Errorf("unsafe native library link")
			}
			links[name] = path.Base(target)
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported native archive entry")
		}
		total += h.Size
		if h.Size < 0 || total > 4<<30 {
			return fmt.Errorf("native archive exceeds size limit")
		}
		out, err := os.OpenFile(filepath.Join(dest, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
		if err != nil {
			return err
		}
		_, err = io.CopyN(out, tr, h.Size)
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for len(links) > 0 {
		progress := false
		for name, target := range links {
			if _, pending := links[target]; pending {
				continue
			}
			src, err := os.Open(filepath.Join(dest, target))
			if err != nil {
				return fmt.Errorf("native library target: %w", err)
			}
			info, err := src.Stat()
			if err != nil {
				src.Close()
				return err
			}
			out, err := os.OpenFile(filepath.Join(dest, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				src.Close()
				return err
			}
			_, err = io.Copy(out, src)
			src.Close()
			ce := out.Close()
			if err != nil {
				return err
			}
			if ce != nil {
				return ce
			}
			delete(links, name)
			progress = true
		}
		if !progress {
			return fmt.Errorf("cyclic native library links")
		}
	}
	return nil
}
func cleanEnvironment() []string {
	allowed := map[string]bool{"HOME": true, "PATH": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "SYSTEMROOT": true}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] {
			env = append(env, entry)
		}
	}
	return append(env, "DO_NOT_TRACK=1")
}
