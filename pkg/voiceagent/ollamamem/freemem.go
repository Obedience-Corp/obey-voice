package ollamamem

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// FreeMemoryEnv overrides the free system memory reading, in bytes, for
// tests and constrained hosts.
const FreeMemoryEnv = "OBEY_VOICE_FREE_MEM_BYTES"

// FreeMemory reports free system memory as a local Ollama counts it when it
// decides whether a model fits. ok is false when it cannot be read.
func FreeMemory(ctx context.Context) (free uint64, ok bool) {
	if v := strings.TrimSpace(os.Getenv(FreeMemoryEnv)); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n, true
		}
	}
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, false
		}
		return parseMemAvailable(string(data))
	case "darwin":
		return darwinFreeMemory(ctx)
	default:
		return 0, false
	}
}

func darwinFreeMemory(ctx context.Context) (uint64, bool) {
	stats, err := exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return 0, false
	}
	memsize, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, false
	}
	total, err := strconv.ParseUint(strings.TrimSpace(string(memsize)), 10, 64)
	if err != nil {
		return 0, false
	}
	return parseVMStat(string(stats), total)
}

// parseMemAvailable reads MemAvailable from /proc/meminfo.
func parseMemAvailable(meminfo string) (uint64, bool) {
	scanner := bufio.NewScanner(strings.NewReader(meminfo))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "MemAvailable:" {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// parseVMStat computes free memory from vm_stat output and total RAM the way
// Ollama does on macOS: total minus active, inactive, speculative, wired and
// compressor pages, with purgeable and file-backed pages counted as free.
func parseVMStat(out string, total uint64) (uint64, bool) {
	if total == 0 {
		return 0, false
	}
	pageSize := uint64(0)
	pages := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if pageSize == 0 {
			if _, rest, ok := strings.Cut(line, "page size of "); ok {
				if fields := strings.Fields(rest); len(fields) > 0 {
					if n, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
						pageSize = n
					}
				}
				continue
			}
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil {
			continue
		}
		pages[strings.Trim(strings.TrimSpace(name), `"`)] = n
	}
	if pageSize == 0 {
		return 0, false
	}
	for _, key := range []string{"Pages active", "Pages inactive", "Pages speculative", "Pages wired down", "Pages occupied by compressor", "Pages purgeable", "File-backed pages"} {
		if _, ok := pages[key]; !ok {
			return 0, false
		}
	}
	used := pages["Pages active"] + pages["Pages inactive"] + pages["Pages speculative"] +
		pages["Pages wired down"] + pages["Pages occupied by compressor"]
	reclaimable := pages["Pages purgeable"] + pages["File-backed pages"]
	if reclaimable >= used {
		return total, true
	}
	usedBytes := (used - reclaimable) * pageSize
	if usedBytes >= total {
		return 0, true
	}
	return total - usedBytes, true
}
