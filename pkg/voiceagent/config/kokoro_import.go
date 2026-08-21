package config

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed kokoro_import.py
var kokoroImportPy []byte

const (
	// thewh1teagle Kokoro v1.0 — same files Python samantha-cli downloads.
	kokoroV1OnnxURL    = "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx"
	kokoroV1VoicesURL  = "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/voices-v1.0.bin"
	kokoroV1OnnxName   = "kokoro-v1.0.onnx"
	kokoroV1VoicesName = "voices-v1.0.bin"

	// kokoroV1DownloadsSubdir holds the raw upstream weights between attempts.
	// A temp stage threw away ~350 MB on every failed convert, so each serve
	// start downloaded them again.
	kokoroV1DownloadsSubdir = ".kokoro-v1-downloads"

	// kokoroConverterProbeTimeout bounds the import probe: a wedged
	// interpreter must not hold up the first Ready.
	kokoroConverterProbeTimeout = 10 * time.Second
)

// kokoroImportEnv are the host seams the v1 import depends on — interpreter
// discovery, the numpy/onnx probe, and the file fetch — injected so tests
// never depend on the machine's Python or reach the network.
type kokoroImportEnv struct {
	lookupPython func() (string, error)
	probe        func(ctx context.Context, python string) error
	fetch        func(ctx context.Context, url, dest, name, localPath string, onProgress func(string, float64)) error
}

func defaultKokoroImportEnv() kokoroImportEnv {
	return kokoroImportEnv{lookupPython: lookupPython3, probe: probeKokoroConverter, fetch: fetchOrReuse}
}

// EnsureKokoroV1EnglishPack downloads thewh1teagle Kokoro v1.0 weights and
// converts them into models_dir/kokoro-v1.0-en for sherpa OfflineTts.
// Requires python3 with numpy+onnx on PATH. On failure returns a soft error
// so callers can keep using the multi-lang pack.
func EnsureKokoroV1EnglishPack(ctx context.Context, onProgress func(name string, pct float64)) error {
	return ensureKokoroV1EnglishPack(ctx, ModelsDir(), defaultKokoroImportEnv(), onProgress)
}

// ensureKokoroV1EnglishPack is the root-explicit core: it verifies the
// converter can actually run before fetching anything, then keeps the weights
// under root so a retry resumes instead of re-downloading.
func ensureKokoroV1EnglishPack(ctx context.Context, root string, env kokoroImportEnv, onProgress func(name string, pct float64)) error {
	outDir := filepath.Join(root, KokoroV1Subdir)
	if kokoroPackReady(outDir) {
		return nil
	}

	// Frontend (tokens/espeak) comes from the multi-lang pack at models root.
	if !archiveExtracted(root, []string{"tokens.txt", "espeak-ng-data"}) {
		return fmt.Errorf("kokoro v1 import needs multi-lang frontend files (tokens.txt, espeak-ng-data) in %s", root)
	}

	python, err := kokoroConverterAvailable(ctx, env)
	if err != nil {
		return err
	}

	stage := filepath.Join(root, kokoroV1DownloadsSubdir)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return fmt.Errorf("kokoro v1 stage: %w", err)
	}

	onnxPath := filepath.Join(stage, kokoroV1OnnxName)
	voicesPath := filepath.Join(stage, kokoroV1VoicesName)
	if err := env.fetch(ctx, kokoroV1OnnxURL, onnxPath, kokoroV1OnnxName, localKokoroV1Onnx(), onProgress); err != nil {
		return err
	}
	if err := env.fetch(ctx, kokoroV1VoicesURL, voicesPath, kokoroV1VoicesName, localKokoroV1Voices(), onProgress); err != nil {
		return err
	}

	if err := convertKokoroV1(ctx, python, root, stage, outDir); err != nil {
		return err
	}
	// The weights have served their purpose only once the pack exists.
	_ = os.RemoveAll(stage)
	return nil
}

// convertKokoroV1 runs the Python converter into a temp dir and swaps the
// result into outDir.
func convertKokoroV1(ctx context.Context, python, root, stage, outDir string) error {
	script := filepath.Join(stage, "kokoro_import.py")
	if err := os.WriteFile(script, kokoroImportPy, 0o644); err != nil {
		return err
	}

	outTmp, err := os.MkdirTemp(root, ".kokoro-v1-out-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(outTmp)

	cmd := exec.CommandContext(ctx, python, script,
		"--onnx", filepath.Join(stage, kokoroV1OnnxName),
		"--voices-npz", filepath.Join(stage, kokoroV1VoicesName),
		"--frontend-dir", root,
		"--out-dir", outTmp,
	)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kokoro v1 convert (need python3 + numpy + onnx): %w", err)
	}

	// Atomic replace of destination.
	_ = os.RemoveAll(outDir)
	if err := os.Rename(outTmp, outDir); err != nil {
		// Cross-device rename can fail; fall back to copy.
		if err := copyDir(outTmp, outDir); err != nil {
			return fmt.Errorf("install kokoro v1 pack: %w", err)
		}
	}
	return nil
}

// kokoroConverterAvailable resolves an interpreter that can actually run the
// converter. It runs before any download: without numpy+onnx the conversion
// always fails, and every serve start would fetch ~350 MB only to discard it.
func kokoroConverterAvailable(ctx context.Context, env kokoroImportEnv) (string, error) {
	python, err := env.lookupPython()
	if err != nil {
		return "", err
	}
	if err := env.probe(ctx, python); err != nil {
		return "", err
	}
	return python, nil
}

// probeKokoroConverter checks that python can import the converter's modules,
// naming the missing one and how to install it.
func probeKokoroConverter(ctx context.Context, python string) error {
	ctx, cancel := context.WithTimeout(ctx, kokoroConverterProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, python, "-c", "import numpy, onnx").CombinedOutput()
	if err == nil {
		return nil
	}
	detail := lastLine(string(out))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("python3 with numpy+onnx not available (%s: %s); install with: pip3 install numpy onnx", python, detail)
}

// lastLine returns the final non-empty line of s, the one a Python traceback
// puts the ModuleNotFoundError on.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func lookupPython3() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("python3 not found on PATH (install python3 with numpy and onnx to enable Kokoro v1.0 English pack)")
}

// localKokoroV1Onnx returns a cached thewh1teagle ONNX if present (legacy CLI path).
func localKokoroV1Onnx() string {
	return filepath.Join(DefaultModelsDir(), kokoroV1OnnxName)
}

func localKokoroV1Voices() string {
	return filepath.Join(DefaultModelsDir(), kokoroV1VoicesName)
}

// fetchOrReuse reuses an earlier download at dest, then a cached localPath,
// and only downloads url as a last resort. downloadFile writes to a ".part"
// temp and renames, so anything sitting at dest is a finished file.
func fetchOrReuse(ctx context.Context, url, dest, name, localPath string, onProgress func(string, float64)) error {
	if completeFile(dest) {
		if onProgress != nil {
			onProgress(name, 1)
		}
		return nil
	}
	if completeFile(localPath) {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
		if onProgress != nil {
			onProgress(name, 1)
		}
		return nil
	}
	var fileProg func(bytes, total int64)
	if onProgress != nil {
		fileProg = func(bytes, total int64) { onProgress(name, pctOfBytes(bytes, total)) }
	}
	return downloadFile(ctx, dest, url, name, 0, "", fileProg)
}

// completeFile reports whether path is a finished, reusable download. A
// ".part" file is an interrupted transfer and is never reused.
func completeFile(path string) bool {
	if path == "" || strings.HasSuffix(path, ".part") {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

// TryEnsureKokoroV1EnglishPack attempts the import and prints a warning on
// soft failure so multi-lang remains available.
func TryEnsureKokoroV1EnglishPack(ctx context.Context, onProgress func(name string, pct float64)) {
	if kokoroPackReady(filepath.Join(ModelsDir(), KokoroV1Subdir)) {
		return
	}
	if err := EnsureKokoroV1EnglishPack(ctx, onProgress); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Kokoro v1.0 English pack not installed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  (using sherpa multi-lang pack; re-run `obey-voice models ensure` once it is available)\n")
		return
	}
	fmt.Fprintf(os.Stderr, "Kokoro pack: thewh1teagle v1.0 English (matches Python samantha-cli weights)\n")
}
