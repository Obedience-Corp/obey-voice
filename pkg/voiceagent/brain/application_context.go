package brain

import (
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"strconv"
)

// ApplicationContext renders the editable system/application YAML document.
func ApplicationContext(cfg *config.Config) string {
	return systemPrompt(cfg, "application", map[string]string{"calibre_enabled": strconv.FormatBool(cfg != nil && cfg.CalibreEnabled)})
}
