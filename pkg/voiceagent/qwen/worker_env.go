package qwen

import (
	"os"
	"strings"
)

// NativeWorkerEnv sets DYLD/LD library paths and optional QWEN3_TTS_TIER.
// Parent QWEN3_TTS_TIER / QWEN3_TTS_MODEL are stripped so the host config wins
// and ambient shell env cannot override a fail-closed product tier.
func NativeWorkerEnv(env []string, libDir, tier string) []string {
	return NativeWorkerEnvWithBackend(env, libDir, tier, "")
}

// NativeWorkerEnvWithBackend is NativeWorkerEnv plus a fail-closed
// QWEN3_TTS_BACKEND from the installed package's backend_hint.
func NativeWorkerEnvWithBackend(env []string, libDir, tier, backendHint string) []string {
	out := make([]string, 0, len(env)+5)
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			out = append(out, e)
			continue
		}
		switch key {
		case "DYLD_LIBRARY_PATH", "LD_LIBRARY_PATH", "QWEN3_TTS_TIER", "QWEN3_TTS_MODEL", "QWEN3_TTS_BACKEND":
			continue
		}
		out = append(out, e)
	}
	sep := string(os.PathListSeparator)
	for _, key := range []string{"DYLD_LIBRARY_PATH", "LD_LIBRARY_PATH"} {
		prev := os.Getenv(key)
		if prev != "" {
			out = append(out, key+"="+libDir+sep+prev)
		} else {
			out = append(out, key+"="+libDir)
		}
	}
	if tier = strings.TrimSpace(tier); tier != "" {
		out = append(out, "QWEN3_TTS_TIER="+tier)
	}
	switch b := strings.ToLower(strings.TrimSpace(backendHint)); b {
	case "vulkan", "cuda", "cpu":
		out = append(out, "QWEN3_TTS_BACKEND="+b)
	}
	return out
}
