package meeting

import (
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// FromConfig maps config.Meeting into a routing Config, falling back to the
// resolved launch workspace's campaign when no default destination is set.
func FromConfig(cfg *config.Config) Config {
	if cfg == nil {
		return Config{}.Normalize()
	}
	mc := cfg.Meeting.Route
	out := Config{
		Mode:    mc.Mode,
		Default: mc.Default,
		Body:    mc.Body,
	}
	for _, d := range mc.Destinations {
		out.Destinations = append(out.Destinations, Destination{
			ID:       d.ID,
			Type:     d.Type,
			Campaign: d.Campaign,
			Capture:  d.Capture,
			Tags:     d.Tags,
			Path:     d.Path,
			Folder:   d.Folder,
		})
	}
	out = out.Normalize()
	// Resolve a session default without writing it into the user's settings.
	// Explicit defaults and disabled routing always retain their meaning.
	if strings.TrimSpace(out.Default) != "" || out.Mode == ModeOff {
		return out
	}
	campaign := config.CampaignFor(config.WorkDirFrom(cfg))
	if !campaign.Found() {
		return out
	}
	for i, d := range out.Destinations {
		if d.ID != "" && d.Type == TypeCampaign && (d.Campaign == campaign.Root ||
			(campaign.Name != "" && strings.EqualFold(d.Campaign, campaign.Name))) {
			out.Destinations[i].CampaignRoot = campaign.Root
			out.Default = d.ID
			return out
		}
	}
	name := campaign.Name
	if name == "" {
		name = campaign.Root
	}
	dest := DestinationFromCampaign(Campaign{Name: name})
	// Pin delivery to the detected root even if camp's registry is unavailable
	// or contains a stale path for this name.
	dest.CampaignRoot = campaign.Root
	if _, exists := out.DestinationByID(dest.ID); exists {
		return out // a configured ID must never be repurposed
	}
	out.Destinations = append(out.Destinations, dest)
	out.Default = dest.ID
	return out
}
