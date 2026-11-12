// Package phonon owns the native Phonon HTTP server and its verified installation.
package phonon

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/archiveutil"
)

const (
	SourceRevision  = "a6bb4161f15afb29f3f7dd5a4d1b0d9e082c47e7"
	sourceSHA256    = "f60911be97349115c4117d87038662a6ea84c4e7c6c6f7f2b3969d72ccd18e93"
	Model           = "gradium/phonon-31m-en"
	ModelRevision   = "8c64b55ec301728e2d1695bfea87a08a267d0c2d"
	CatalogRevision = "0ee17205f254b5abc43b266c8a0f215565c352f7"
)

var sourceURL = "https://codeload.github.com/gradium-ai/xn-ptts/tar.gz/" + SourceRevision
var namedVoices = []string{"Harper", "Juniper", "Edwin", "Zoey", "Fennick the Young Elf"}

// namedVoiceFiles are the catalog embeddings of namedVoices at CatalogRevision.
var namedVoiceFiles = []string{
	"voices/4SZHfMpw-p46Ywgs.safetensors",
	"voices/NIWIOv5vTi5eRMj8.safetensors",
	"voices/zUCxzrOrugTSis03.safetensors",
	"voices/NbpkqMVS3CJeq2j8.safetensors",
	"voices/cXoIXJ2llS8S0WOW.safetensors",
}

type Options struct {
	ModelsDir string
	Backend   string
	// Binary is an optional externally supplied native server. Empty uses the
	// pinned, managed build. It never changes the model or catalog pins.
	Binary string
}

func (o Options) root() string       { return filepath.Join(o.ModelsDir, "phonon", "native") }
func (o Options) installDir() string { return filepath.Join(o.root(), SourceRevision, o.Backend) }
func (o Options) binary() string {
	if o.Binary != "" {
		return o.Binary
	}
	return filepath.Join(o.installDir(), "ptts-openai-server")
}
func (o Options) cache() string { return filepath.Join(o.root(), "cache") }

func Validate(o Options) error {
	if o.ModelsDir == "" {
		return errors.New("native Phonon requires a models directory")
	}
	if !filepath.IsAbs(o.ModelsDir) {
		return errors.New("native Phonon requires an absolute models directory")
	}
	switch o.Backend {
	case "cpu", "vulkan", "cuda", "metal":
		return nil
	default:
		return fmt.Errorf("unsupported native Phonon backend %q; use auto, cpu, vulkan or cuda", o.Backend)
	}
}

// DetectBackend only chooses a build lane. Start asks that runtime to select
// the explicit device, and records its actual device banner after readiness.
func DetectBackend() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return "metal"
	}
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return "cuda"
	}
	for _, dir := range []string{"/usr/share/vulkan/icd.d", "/etc/vulkan/icd.d"} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if strings.HasSuffix(n, ".json") && (strings.Contains(n, "radeon") || strings.Contains(n, "intel")) {
				return "vulkan"
			}
		}
	}
	return "cpu"
}

// Installed is offline and never starts a process or downloads anything.
func Installed(o Options) bool {
	if Validate(o) != nil {
		return false
	}
	info, err := os.Stat(o.binary())
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return false
	}
	b, err := os.ReadFile(filepath.Join(o.installDir(), "ready.json"))
	if err != nil || string(b) != readyRecord() {
		return false
	}
	snapshot := filepath.Join(o.cache(), "hub", "models--gradium--phonon-31m-en", "snapshots", ModelRevision)
	for _, name := range []string{"config.json", "tokenizer.json", "model.safetensors", "default-voice.safetensors"} {
		if st, err := os.Stat(filepath.Join(snapshot, name)); err != nil || st.Size() == 0 {
			return false
		}
	}
	// Include catalog embeddings in readiness, not only descriptive metadata.
	voices := filepath.Join(o.cache(), "hub", "models--gradium--phonon-voices", "snapshots", CatalogRevision)
	for _, name := range append([]string{"voices.jsonl"}, namedVoiceFiles...) {
		if st, err := os.Stat(filepath.Join(voices, name)); err != nil || st.Size() == 0 {
			return false
		}
	}
	return true
}

func readyRecord() string {
	return SourceRevision + "\n" + ModelRevision + "\n" + CatalogRevision + "\n"
}

// Ensure compiles the pinned Rust server once and provisions its pinned model
// and voice embeddings. No daemon is left running after setup.
func Ensure(ctx context.Context, o Options, progress func(string)) error {
	if err := Validate(o); err != nil {
		return err
	}
	if Installed(o) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := os.MkdirAll(o.installDir(), 0755); err != nil {
		return err
	}
	if _, err := os.Stat(o.binary()); err != nil {
		if o.Binary != "" {
			return fmt.Errorf("native Phonon binary: %w", err)
		}
		if progress != nil {
			progress("Building pinned native Phonon (" + o.Backend + ")")
		}
		if err := build(ctx, o); err != nil {
			return err
		}
	}
	if progress != nil {
		progress("Preparing pinned Phonon model and named voices")
	}
	p, err := Start(ctx, o, false)
	if err != nil {
		return err
	}
	defer p.Close()
	if err := p.checkVoices(ctx, namedVoices); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(o.installDir(), "ready.json"), strings.NewReader(readyRecord()), 0644); err != nil {
		return err
	}
	if !Installed(o) {
		return errors.New("native Phonon setup did not produce a complete model/voice cache; retry `obey-voice models ensure --tts`")
	}
	return nil
}

func build(ctx context.Context, o Options) error {
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		return errors.New("native Phonon setup requires Rust cargo, a C/C++ toolchain, pkg-config, opus and lame development libraries; on Arch run `sudo pacman -S --needed rust base-devel opus lame`, then retry `obey-voice models ensure --tts`")
	}
	dir, err := os.MkdirTemp(o.root(), "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download native Phonon source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("native Phonon source returned %s", resp.Status)
	}
	archive := filepath.Join(dir, "source.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 32<<20))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(h.Sum(nil)) != sourceSHA256 {
		return errors.New("native Phonon source checksum mismatch; source was not executed")
	}
	if err := extract(archive, dir); err != nil {
		return err
	}
	source := filepath.Join(dir, "xn-ptts-"+SourceRevision)
	args := []string{"build", "--release", "--locked", "-p", "ptts-openai-server"}
	if o.Backend != "cpu" {
		args = append(args, "--features", o.Backend)
	}
	cmd := exec.CommandContext(ctx, cargo, args...)
	configureCommand(cmd)
	cmd.Dir = source
	// Ignore a user CARGO_TARGET_DIR: only install the binary built from the
	// verified source in this operation, never an unrelated cached executable.
	cmd.Env = environment(map[string]string{"CARGO_TARGET_DIR": filepath.Join(dir, "target")})
	var logs tailBuffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("build native Phonon (%s): %w; install Rust, pkg-config, opus/lame development libraries and the selected GPU SDK/driver, then retry:\n%s", o.Backend, err, logs.String())
	}
	binary, err := os.Open(filepath.Join(dir, "target", "release", "ptts-openai-server"))
	if err != nil {
		return err
	}
	defer binary.Close()
	return atomicWrite(o.binary(), binary, 0755)
}

func atomicWrite(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".install-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Chmod(mode)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func extract(archive, dest string) error {
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
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(gz)
	var total int64
	var links []archiveutil.Link
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return archiveutil.CreateLinks(root, links)
		}
		if err != nil {
			return err
		}
		rel, err := archiveutil.Name(h.Name)
		if err != nil {
			return err
		}
		path := filepath.FromSlash(rel)
		switch h.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// GitHub codeload includes PAX metadata, not filesystem entries.
			continue
		case tar.TypeDir:
			if err := root.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			links = append(links, archiveutil.Link{Name: rel, Target: h.Linkname})
		case tar.TypeReg:
			total += h.Size
			if total > 128<<20 {
				return errors.New("native Phonon source archive is too large")
			}
			if err := root.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			out, err := root.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported source archive entry %q", h.Name)
		}
	}
}
