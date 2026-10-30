package decider

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const DefaultModel = "2b"

// ModelIDs is deliberately ordered, with the stable, qualified default first.
func ModelIDs() []string { return []string{"2b", "0.8b", "decider-2b-v12", "decider-4b-v3", "d1-3b"} }
func Devices() []string  { return []string{"auto", "cpu", "mps", "cuda"} }
func (m Model) DownloadBytes() int64 {
	var total int64
	for _, f := range m.Files {
		total += f.Size
	}
	return total
}
func (m Model) Details() string {
	note := ""
	if m.Engine == "liquid" {
		note = "; LFM license: commercial restrictions apply"
	}
	return fmt.Sprintf("%.1f GiB download · ~%d GiB GPU memory (CPU may need more) · %s%s", float64(m.DownloadBytes())/(1<<30), m.MemoryGiB, m.License, note)
}

// DeviceFor validates the platform matrix without doing I/O. Auto is resolved
// using a bounded host probe at preparation time; it never chooses a model.
func DeviceFor(goos, arch, requested string, cuda bool) (string, error) {
	if _, _, err := UVAsset(goos, arch); err != nil {
		return "", err
	}
	if requested == "" {
		requested = "auto"
	}
	switch requested {
	case "auto":
		if goos == "darwin" && arch == "arm64" {
			return "mps", nil
		}
		if goos == "linux" && arch == "amd64" && cuda {
			return "cuda", nil
		}
		return "cpu", nil
	case "cpu":
		return "cpu", nil
	case "mps":
		if goos == "darwin" && arch == "arm64" {
			return "mps", nil
		}
	case "cuda":
		if goos == "linux" && arch == "amd64" {
			return "cuda", nil
		}
	default:
		return "", fmt.Errorf("unknown decision compute device %q", requested)
	}
	return "", fmt.Errorf("decision device %s is unavailable on %s/%s; choose CPU or an external local server", requested, goos, arch)
}

func ResolveDevice(ctx context.Context, requested string) (string, error) {
	if _, err := DeviceFor(runtime.GOOS, runtime.GOARCH, requested, false); err != nil {
		return "", err
	}
	cuda := false
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" && (requested == "" || requested == "auto" || requested == "cuda") {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		cmd := exec.CommandContext(probeCtx, "nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
		cmd.Env = cleanEnvironment()
		out, err := cmd.Output()
		cancel()
		cuda = err == nil && strings.TrimSpace(string(out)) != ""
		if requested == "cuda" && !cuda {
			return "", fmt.Errorf("CUDA requested but no NVIDIA GPU/driver was detected; install its driver or choose CPU")
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return DeviceFor(runtime.GOOS, runtime.GOARCH, requested, cuda)
}
