package qwen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHardwareVulkanICDIgnoresLavapipe(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lvp_icd.x86_64.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hardwareVulkanICD([]string{dir}) {
		t.Fatal("lavapipe must not count as a hardware Vulkan ICD")
	}
}

func TestHardwareVulkanICDAcceptsRadeon(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "radeon_icd.x86_64.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hardwareVulkanICD([]string{dir}) {
		t.Fatal("radeon ICD should count as hardware Vulkan")
	}
}

func TestHardwareVulkanICDEmptyDir(t *testing.T) {
	if hardwareVulkanICD([]string{t.TempDir()}) {
		t.Fatal("empty ICD dir is not hardware Vulkan")
	}
}

func TestDetectLinuxTTSBackendNvidiaEmptyCudaSHAStaysCPU(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only probe")
	}
	if !nvidiaGPUPresent() {
		t.Skip("no /dev/nvidia0")
	}
	if linuxAmd64CudaSHA256 != "" {
		t.Skip("cuda pin SHA is published; empty-SHA fail-closed path not active")
	}
	if got := DetectLinuxTTSBackend(); got != "cpu" {
		t.Fatalf("DetectLinuxTTSBackend() = %q, want cpu while cuda SHA empty", got)
	}
	url, sha := DefaultNativeReleaseForBackend("linux", "amd64", DetectLinuxTTSBackend())
	if !strings.Contains(url, "qwen3-tts-native-linux-amd64.tar.gz") {
		t.Fatalf("NVIDIA with empty cuda SHA must use CPU pin, got %q", url)
	}
	if strings.Contains(url, "vulkan") || strings.Contains(url, "cuda") {
		t.Fatalf("NVIDIA must not download vulkan/cuda while cuda SHA empty: %q", url)
	}
	if len(sha) != 64 {
		t.Fatalf("cpu pin sha length = %d, want 64 hex", len(sha))
	}
}

func TestDetectLinuxTTSBackendNvidiaNotVulkan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only probe")
	}
	if !nvidiaGPUPresent() {
		t.Skip("no /dev/nvidia0")
	}
	got := DetectLinuxTTSBackend()
	if got == "vulkan" {
		t.Fatal("NVIDIA must not select the vulkan (RADV) tarball")
	}
}
