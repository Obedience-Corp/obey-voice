package decider

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed requirements.txt
var requirements string

//go:embed liquid_server.py
var liquidServer string

//go:embed requirements-cuda.txt
var cudaRequirements string

const visionVersion = "0.29.2"
const pillowVersion = "13.0.0"

const pythonVersion = "3.13.3"
const torchVersion = "2.14.1"

func defaultDevice() string {
	device, _ := DeviceFor(runtime.GOOS, runtime.GOARCH, "auto", false)
	return device
}
func runtimeID() string { return runtimeIDFor("2b", defaultDevice()) }
func runtimeIDFor(modelID, device string) string {
	model, _ := ModelFor(modelID)
	digest := sha256.Sum256([]byte(RuntimeRevision + pythonVersion + torchVersion + visionVersion + pillowVersion + requirements + cudaRequirements + liquidServer + model.Engine + device))
	return fmt.Sprintf("runtime-%x", digest[:8])
}
func PythonPath(modelsDir string) string { return PythonPathFor(modelsDir, "2b", defaultDevice()) }
func PythonPathFor(modelsDir, model, device string) string {
	return filepath.Join(modelsDir, "decider", runtimeIDFor(model, device), "venv", "bin", "python")
}
func PythonReady(modelsDir string) bool { return PythonReadyFor(modelsDir, "2b", defaultDevice()) }
func PythonReadyFor(modelsDir, model, device string) bool {
	if _, err := ModelFor(model); err != nil {
		return false
	}
	root := filepath.Join(modelsDir, "decider", runtimeIDFor(model, device))
	marker, err := os.ReadFile(filepath.Join(root, "installed"))
	if err != nil || string(marker) != runtimeIDFor(model, device) {
		return false
	}
	info, err := os.Stat(PythonPathFor(modelsDir, model, device))
	return err == nil && !info.IsDir()
}

// EnsurePython uses a private managed interpreter and environment. It never
// changes the user's Python, shell profile or package installation. Downloads
// are an explicit setup operation; normal inference invokes Python directly.
func EnsurePython(ctx context.Context, modelsDir string, progress func(string)) error {
	return EnsurePythonFor(ctx, modelsDir, "2b", defaultDevice(), progress)
}
func EnsurePythonFor(ctx context.Context, modelsDir, modelID, device string, progress func(string)) error {
	model, err := ModelFor(modelID)
	if err != nil {
		return err
	}
	device, err = ResolveDevice(ctx, device)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(modelsDir)
	if err != nil {
		return fmt.Errorf("resolve decision models directory: %w", err)
	}
	modelsDir = absolute
	if _, _, err := UVAsset(runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	root := filepath.Join(modelsDir, "decider", runtimeIDFor(modelID, device))
	if err := os.MkdirAll(root, 0700); err != nil {
		return fmt.Errorf("create decision runtime: %w", err)
	}
	release, err := installLock(ctx, filepath.Join(root, "install.lock"))
	if err != nil {
		return err
	}
	defer release()
	if PythonReadyFor(modelsDir, modelID, device) {
		return nil
	}
	if model.Engine == "decider" {
		source, err := os.Open(SourcePath(modelsDir))
		if err != nil {
			return fmt.Errorf("open decision runtime source: %w", err)
		}
		hash := sha256.New()
		_, err = io.Copy(hash, source)
		_ = source.Close()
		if err != nil {
			return fmt.Errorf("read decision runtime source: %w", err)
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != RuntimeSHA256 {
			return fmt.Errorf("decision runtime source checksum mismatch")
		}
	}
	log, err := os.OpenFile(filepath.Join(root, "install.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open decision setup log: %w", err)
	}
	defer log.Close()
	uv := filepath.Join(UVDir(modelsDir), "uv")
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte(requirements), 0600); err != nil {
		return err
	}
	run := func(stage string, args ...string) error {
		if progress != nil {
			progress(stage)
		}
		cmd := exec.CommandContext(ctx, uv, append([]string{"--no-config", "--no-progress"}, args...)...)
		cmd.Dir = root
		cmd.Env = append(cleanEnvironment(), "UV_PYTHON_INSTALL_DIR="+filepath.Join(modelsDir, "decider", "python"), "UV_CACHE_DIR="+filepath.Join(modelsDir, "decider", "cache"), "UV_KEYRING_PROVIDER=disabled")
		cmd.Stdout, cmd.Stderr = log, log
		configureProcess(cmd)
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%s: %w (details: %s)", stage, err, log.Name())
		}
		return nil
	}
	if err := run("Installing private Python", "venv", "--managed-python", "--python", pythonVersion, "--no-project", "--allow-existing", filepath.Join(root, "venv")); err != nil {
		return err
	}
	python := PythonPathFor(modelsDir, modelID, device)
	if err := run("Installing decision dependencies", "pip", "install", "--python", python, "--no-deps", "--default-index", "https://pypi.org/simple", "-r", filepath.Join(root, "requirements.txt")); err != nil {
		return err
	}
	index := "https://pypi.org/simple"
	if runtime.GOOS == "linux" && device != "cuda" {
		index = "https://download.pytorch.org/whl/cpu"
	}
	if err := run("Installing local inference engine", "pip", "install", "--python", python, "--no-deps", "--default-index", index, "torch=="+torchVersion, "torchvision=="+visionVersion); err != nil {
		return err
	}
	if err := run("Installing image dependencies", "pip", "install", "--python", python, "--no-deps", "--default-index", "https://pypi.org/simple", "pillow=="+pillowVersion); err != nil {
		return err
	}
	if device == "cuda" {
		cudaPath := filepath.Join(root, "requirements-cuda.txt")
		if err := os.WriteFile(cudaPath, []byte(cudaRequirements), 0600); err != nil {
			return err
		}
		if err := run("Installing CUDA dependencies", "pip", "install", "--python", python, "--no-deps", "--default-index", "https://pypi.org/simple", "-r", cudaPath); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "liquid_server.py"), []byte(liquidServer), 0600); err != nil {
		return err
	}
	sourceURL := (&url.URL{Scheme: "file", Path: SourcePath(modelsDir)}).String()
	if model.Engine == "decider" {
		if err := run("Installing decision server", "pip", "install", "--python", python, "--no-deps", "--no-build-isolation", "decider-ai[serve] @ "+sourceURL); err != nil {
			return err
		}
	}
	if err := run("Verifying decision environment", "pip", "check", "--python", python, "--offline"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "installed"), []byte(runtimeIDFor(modelID, device)), 0600)
}

// Optional local processes inherit no provider credentials or Python/uv user
// configuration. Library paths and locale remain available for native loading.
func cleanEnvironment() []string {
	allowed := map[string]bool{"HOME": true, "PATH": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "LD_LIBRARY_PATH": true, "DYLD_LIBRARY_PATH": true, "SYSTEMROOT": true}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] {
			env = append(env, entry)
		}
	}
	return append(env, "PYTHONNOUSERSITE=1", "HF_HUB_DISABLE_TELEMETRY=1", "DO_NOT_TRACK=1")
}
