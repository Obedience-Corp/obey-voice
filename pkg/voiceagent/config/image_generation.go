package config

import "strings"

// Picture makers generate_image can use.
const (
	ImageGenerationOff     = "off"
	ImageGenerationComfyUI = "comfyui"
)

// DefaultComfyUIURL is where ComfyUI listens when started with its defaults.
const DefaultComfyUIURL = "http://127.0.0.1:8188"

// ImageGenerationValues lists the image_generation values.
func ImageGenerationValues() []string {
	return []string{ImageGenerationOff, ImageGenerationComfyUI}
}

// ImageGenerationOf is the picture maker cfg selects; anything unknown is off.
func ImageGenerationOf(cfg *Config) string {
	if cfg == nil {
		return ImageGenerationOff
	}
	if strings.EqualFold(strings.TrimSpace(cfg.ImageGeneration), ImageGenerationComfyUI) {
		return ImageGenerationComfyUI
	}
	return ImageGenerationOff
}
