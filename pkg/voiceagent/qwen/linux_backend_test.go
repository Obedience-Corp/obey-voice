package qwen

import (
	"os"
	"path/filepath"
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
