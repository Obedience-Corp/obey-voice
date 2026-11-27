package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/imagegen"
)

// pictureMakerNames are the Picture maker options as the row shows them,
// index-aligned with config.ImageGenerationValues.
var pictureMakerNames = []string{"off", "ComfyUI"}

// imageRows are the Tools rows for generate_image: which picture maker, and
// with ComfyUI, where it runs and which workflow it runs. Obey Voice installs
// none of it; the rows only point at what is already running.
func (m settingsModel) imageRows() []settingRow {
	cfg := m.cfg
	maker := config.ImageGenerationOf(cfg)
	on := maker == config.ImageGenerationComfyUI
	address := strings.TrimSpace(cfg.ComfyUIURL)
	if address == "" {
		address = config.DefaultComfyUIURL
	}
	workflow := strings.TrimSpace(cfg.ImageWorkflow)
	if workflow == "" {
		workflow = "built-in (Z-Image Turbo)"
	}
	off := "picture maker is off"
	return []settingRow{
		{
			key: "image_generation", name: "Picture maker", kind: rowChoice,
			value: pictureMakerNames[indexOf(config.ImageGenerationValues(), maker)], options: pictureMakerNames,
			current: indexOf(config.ImageGenerationValues(), maker), applies: appliesNextConversation,
			help: "Needs a running ComfyUI; Obey Voice does not install it.",
			set:  func(m *settingsModel, i int) tea.Cmd { return m.setPictureMaker(i) },
		},
		{
			key: "comfyui_url", name: "ComfyUI address", kind: rowText,
			value: address, applies: appliesNextConversation, disabled: !on, reason: off,
			help: "Where ComfyUI is running. Empty restores " + config.DefaultComfyUIURL + ".",
			run: func(m *settingsModel) tea.Cmd {
				cmd := m.beginEdit(editImage, "ComfyUI address", address)
				m.edit.decisionKey = "comfyui_url"
				return cmd
			},
		},
		{
			key: "image_workflow", name: "Picture workflow", kind: rowText,
			value: workflow, applies: appliesNextConversation, disabled: !on, reason: off,
			help: "ComfyUI API-format workflow with {{prompt}}. Empty: Z-Image Turbo.",
			run: func(m *settingsModel) tea.Cmd {
				cmd := m.beginEdit(editImage, "Picture workflow", strings.TrimSpace(m.cfg.ImageWorkflow))
				m.edit.decisionKey = "image_workflow"
				return cmd
			},
		},
	}
}

func (m *settingsModel) setPictureMaker(i int) tea.Cmd {
	values := config.ImageGenerationValues()
	if i < 0 || i >= len(values) {
		return nil
	}
	if err := m.save("image_generation", values[i]); err != nil {
		m.message = fmt.Sprintf("Failed to save picture maker: %v", err)
		return nil
	}
	m.cfg.ImageGeneration = values[i]
	if values[i] == config.ImageGenerationOff {
		m.message = "Picture maker off; generate_image tells the model so · applies to the next conversation"
		return nil
	}
	m.message = "Picture maker ComfyUI; ComfyUI must be running at " + m.cfg.ComfyUIURL + " · applies to the next conversation"
	if strings.TrimSpace(m.cfg.ComfyUIURL) == "" {
		m.message = "Picture maker ComfyUI; ComfyUI must be running at " + config.DefaultComfyUIURL + " · applies to the next conversation"
	}
	return nil
}

// commitImageSetting saves a typed ComfyUI address or workflow path. A value
// that cannot work keeps the editor open with the reason.
func (m settingsModel) commitImageSetting(key, value string) (settingsModel, tea.Cmd) {
	value = strings.TrimSpace(value)
	switch key {
	case "comfyui_url":
		if value == "" {
			value = config.DefaultComfyUIURL
		}
		if _, err := imagegen.NewComfyUI(value, ""); err != nil {
			m.message = err.Error()
			return m, nil
		}
	case "image_workflow":
		if value != "" {
			if info, err := os.Stat(value); err != nil || info.IsDir() {
				m.message = fmt.Sprintf("%s is not a workflow file", value)
				return m, nil
			}
		}
	default:
		m.cancelEdit()
		return m, nil
	}
	if err := m.save(key, value); err != nil {
		m.message = fmt.Sprintf("Failed to save %s: %v", key, err)
		return m, nil
	}
	m.cancelEdit()
	if key == "comfyui_url" {
		m.cfg.ComfyUIURL = value
		m.message = "ComfyUI address " + value + " · applies to the next conversation"
	} else {
		m.cfg.ImageWorkflow = value
		m.message = "Picture workflow saved · applies to the next conversation"
		if value == "" {
			m.message = "Picture workflow is the built-in one · applies to the next conversation"
		}
	}
	return m, nil
}
