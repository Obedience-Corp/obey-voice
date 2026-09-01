package qwen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// linuxVulkanICDDirs are the standard ICD search paths. Tests override this.
var linuxVulkanICDDirs = []string{
	"/usr/share/vulkan/icd.d",
	"/etc/vulkan/icd.d",
	"/usr/local/share/vulkan/icd.d",
}

// DetectLinuxTTSBackend picks the public Qwen native tarball lane for this
// Linux host. NVIDIA gets cuda only when the linux-amd64-cuda pin SHA is set;
// otherwise it stays on the CPU pin (fail closed — no 404). Hardware Vulkan
// ICDs (RADV/ANV) get vulkan. Lavapipe is ignored. NVIDIA ICDs are never
// treated as vulkan.
func DetectLinuxTTSBackend() string {
	if runtime.GOOS != "linux" {
		return "cpu"
	}
	if nvidiaGPUPresent() {
		if linuxAmd64CudaSHA256 != "" {
			return "cuda"
		}
		return "cpu"
	}
	if hardwareVulkanICD(linuxVulkanICDDirs) {
		return "vulkan"
	}
	return "cpu"
}

func nvidiaGPUPresent() bool {
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return true
	}
	return false
}

func hardwareVulkanICD(dirs []string) bool {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if e.IsDir() || !strings.HasSuffix(name, ".json") {
				continue
			}
			base := filepath.Base(name)
			if strings.Contains(base, "lvp") || strings.Contains(base, "lavapipe") {
				continue
			}
			return true
		}
	}
	return false
}
