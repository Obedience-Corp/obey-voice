package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	buildutil "github.com/Obedience-Corp/build-util"
)

func nativePlatform(goos, arch string) (string, error) {
	switch goos + "/" + arch {
	case "darwin/arm64":
		return "macos-arm64", nil
	case "linux/amd64":
		return "linux-amd64", nil
	default:
		return "", fmt.Errorf("development install supports Apple Silicon macOS and x86_64 Linux, got %s/%s", goos, arch)
	}
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func installCheckout(ctx context.Context, cfg buildutil.BuildConfig) error {
	target, err := nativePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	version, err := commandOutput(ctx, "git", "describe", "--tags", "--match", "v[0-9]*", "--always", "--dirty")
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`).MatchString(version) {
		return fmt.Errorf("unsafe checkout version %q", version)
	}
	destination, err := commandOutput(ctx, "go", "env", "GOBIN")
	if err != nil {
		return err
	}
	if destination == "" {
		gopath, err := commandOutput(ctx, "go", "env", "GOPATH")
		if err != nil {
			return err
		}
		paths := filepath.SplitList(gopath)
		if len(paths) == 0 {
			return fmt.Errorf("go env GOPATH is empty")
		}
		destination = filepath.Join(paths[0], "bin")
	}
	// An installation must build for this host with native audio enabled, even
	// when the caller's environment was configured for cross-compilation.
	for key, value := range map[string]string{"GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH, "CGO_ENABLED": "1"} {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	cfg.LDFlags = func() string {
		return "-X github.com/Obedience-Corp/obey-voice/cmd/obey-voice/cmd.releaseVersion=" + version
	}
	if err := buildutil.Execute(ctx, []string{"build"}, cfg); err != nil {
		return fmt.Errorf("build checkout: %w", err)
	}
	entry, err := installPayload(ctx, destination, target, version, func(stage string) error {
		return runCommand(ctx, "bash", "scripts/stage-"+strings.Split(target, "-")[0]+"-native.sh", "bin/obey-voice", stage)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Installed checkout %s: %s\n", version, entry)
	if os.Getenv("OBEY_VOICE_SKIP_MODELS_ENSURE") == "1" {
		fmt.Println("Skipping model download. Run: obey-voice models ensure")
		return nil
	}
	fmt.Println("Downloading default model assets (STT/TTS/VAD for this config)...")
	if err := runCommand(ctx, entry, "models", "ensure"); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Printf("Binary installed; voice setup is incomplete: %v\nResolve the model/runtime error above, then run: obey-voice models ensure\n", err)
	}
	return nil
}

// installPayload stages directly into the destination filesystem. Unlike a
// release install, there is no downloaded archive to extract or trust here.
// The shared staging scripts verify native dependencies and license digests;
// the installed executable is exercised before either public link changes.
func installPayload(ctx context.Context, destination, target, version string, stageFiles func(string) error) (string, error) {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	entry := filepath.Join(destination, "obey-voice")
	alias := filepath.Join(destination, "samantha")
	for _, path := range []string{entry, alias} {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && info.IsDir() {
			return "", fmt.Errorf("install entry is a directory: %s", path)
		}
	}
	store := filepath.Join(destination, ".obey-voice-dev")
	if err := os.MkdirAll(store, 0o755); err != nil {
		return "", err
	}
	previous, _ := filepath.EvalSymlinks(entry)
	staging, err := os.MkdirTemp(store, "install-")
	if err != nil {
		return "", err
	}
	activated := false
	defer func() {
		if !activated {
			_ = os.RemoveAll(staging)
		}
	}()
	payload := filepath.Join(staging, "obey-voice-"+target)
	if err := stageFiles(payload); err != nil {
		return "", fmt.Errorf("stage native payload: %w", err)
	}
	if err := verifyPayload(ctx, payload, target, version); err != nil {
		return "", fmt.Errorf("verify native payload: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Prepare both symlinks before activation so creation failures cannot
	// replace a working command. Rename replaces links, never their targets.
	executable := filepath.Join(payload, "obey-voice")
	link := filepath.Join(staging, "entry")
	aliasLink := filepath.Join(staging, "alias")
	if err := os.Symlink(executable, link); err != nil {
		return "", err
	}
	if err := os.Symlink(entry, aliasLink); err != nil {
		return "", err
	}
	if err := os.Rename(link, entry); err != nil {
		return "", err
	}
	activated = true
	if err := os.Rename(aliasLink, alias); err != nil {
		return "", fmt.Errorf("installed %s but could not update compatibility alias: %w", entry, err)
	}
	// Only retire this installer's previous payload, never a release or
	// Homebrew link target (or the command that was just installed).
	old := filepath.Dir(filepath.Dir(previous))
	realStore, err := filepath.EvalSymlinks(store)
	if err == nil && filepath.Dir(old) == realStore && strings.HasPrefix(filepath.Base(old), "install-") &&
		filepath.Base(previous) == "obey-voice" && filepath.Base(filepath.Dir(previous)) == "obey-voice-"+target {
		if err := os.RemoveAll(old); err != nil {
			fmt.Fprintf(os.Stderr, "Could not remove previous dev payload %s: %v\n", old, err)
		}
	}
	return entry, nil
}

func verifyPayload(ctx context.Context, payload, target, version string) error {
	suffix, module := "so", "linux"
	if target == "macos-arm64" {
		suffix, module = "dylib", "macos"
	}
	for _, name := range []string{"obey-voice", "LICENSE", "NOTICE", "lib/libsherpa-onnx-c-api." + suffix,
		"lib/libonnxruntime." + suffix, "licenses/sherpa-onnx-go-" + module + "-LICENSE",
		"licenses/onnxruntime-LICENSE", "licenses/onnxruntime-ThirdPartyNotices.txt"} {
		info, err := os.Lstat(filepath.Join(payload, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("required payload file %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("required payload file %s must be a nonempty regular file", name)
		}
	}
	scratch, err := os.MkdirTemp("", "obey-voice-install-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(payload, "obey-voice"), "--version")
	cmd.Dir = scratch
	cmd.Env = cleanSmokeEnv(os.Environ(), scratch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("installed executable startup: %w: %s", err, output)
	}
	for _, word := range strings.Fields(string(output)) {
		if word == version {
			return nil
		}
	}
	return fmt.Errorf("installed version does not match %s: %q", version, output)
}

func cleanSmokeEnv(env []string, scratch string) []string {
	clean := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "DYLD_") || strings.HasPrefix(key, "LD_") ||
			strings.HasPrefix(key, "OBEY_VOICE_") || strings.HasPrefix(key, "SAMANTHA_") ||
			key == "HOME" || key == "XDG_CONFIG_HOME" || key == "XDG_CACHE_HOME" {
			continue
		}
		clean = append(clean, item)
	}
	return append(clean, "HOME="+scratch, "XDG_CONFIG_HOME="+scratch, "XDG_CACHE_HOME="+scratch)
}
