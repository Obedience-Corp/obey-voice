//go:build !integration

package cmd

import (
	"fmt"
	"sync"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type managedSetting struct {
	Spec  config.KeySpec  `json:"spec"`
	Value config.KeyValue `json:"value"`
}

type managedPersona struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	BrainProvider  string `json:"brain_provider"`
	BrainModel     string `json:"brain_model"`
	TTSProvider    string `json:"tts_provider"`
	Voice          string `json:"voice"`
	Tier           string `json:"tier"`
	Prompt         string `json:"prompt"`
	PromptEditable bool   `json:"prompt_editable"`
}

func remoteSettingAllowed(spec config.KeySpec) bool {
	return spec.Editable && !spec.Secret && spec.Group != config.GroupHidden && spec.Key != "remote_tools_enabled" && spec.Type != config.TypeOpaque
}

func newServeConfigManagement() *netapi.ConfigManagement {
	var mu sync.Mutex
	settings := func() (any, error) {
		mu.Lock()
		defer mu.Unlock()
		cfg, err := config.LoadRaw()
		if err != nil {
			return nil, err
		}
		values := config.Values()
		out := make([]managedSetting, 0)
		for _, spec := range config.SchemaFor(cfg) {
			if remoteSettingAllowed(spec) {
				out = append(out, managedSetting{Spec: spec, Value: values[spec.Key]})
			}
		}
		return out, nil
	}
	setSetting := func(key, value string) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		spec, ok := config.SpecFor(key)
		if !ok || !remoteSettingAllowed(spec) {
			return nil, fmt.Errorf("setting %q is not editable remotely", key)
		}
		result, err := config.SetKeyFileOnDisk(key, value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"changed": result.Changed, "restart_required": result.RestartRequired}, nil
	}
	readPersona := func(id string) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := persona.ValidateID(id); err != nil {
			return nil, err
		}
		p, err := persona.Load(id)
		if err != nil {
			return nil, err
		}
		prompt, err := persona.DescribePrompt(p)
		if err != nil {
			return nil, err
		}
		return managedPersona{ID: p.ID, DisplayName: p.DisplayName, BrainProvider: p.Brain.Provider, BrainModel: p.Brain.Model, TTSProvider: p.TTS.Provider, Voice: p.TTS.Voice, Tier: p.TTS.Tier, Prompt: prompt.Body, PromptEditable: !prompt.Structured && prompt.Source == "user"}, nil
	}
	editPersona := func(id, field, value string) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		flags := map[string]string{"display_name": "display-name", "brain_provider": "brain-provider", "brain_model": "brain-model", "tts_provider": "tts-provider", "voice": "voice", "tier": "tier", "prompt": "prompt"}
		flag, ok := flags[field]
		if !ok {
			return nil, fmt.Errorf("persona field %q is not editable remotely", field)
		}
		if field == "prompt" {
			if err := persona.ValidateID(id); err != nil {
				return nil, err
			}
			p, err := persona.Load(id)
			if err != nil {
				return nil, err
			}
			prompt, err := persona.DescribePrompt(p)
			if err != nil {
				return nil, err
			}
			if prompt.Structured || prompt.Source != "user" {
				return nil, fmt.Errorf("this prompt cannot be edited remotely")
			}
		}
		cmd := personaEditCmd()
		if err := cmd.Flags().Set(flag, value); err != nil {
			return nil, err
		}
		result, err := applyPersonaEdit(cmd, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"changed": result.Changed, "applies": result.Applies}, nil
	}
	return &netapi.ConfigManagement{Settings: settings, SetSetting: setSetting, Persona: readPersona, EditPersona: editPersona}
}
