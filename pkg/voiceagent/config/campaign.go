package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	// campaignMarkerDir/campaignMarkerFile mark a campaign root. The pair is the
	// whole contract: a directory holding .campaign/campaign.yaml IS a campaign
	// root, and no `camp` subprocess is consulted to learn it.
	campaignMarkerDir  = ".campaign"
	campaignMarkerFile = "campaign.yaml"

	// maxCampaignMarkerBytes caps what is read from a marker at startup and on a
	// cache miss. A campaign.yaml is a few kilobytes; anything past this is not
	// one, and the campaign is still reported — the file just goes unread.
	maxCampaignMarkerBytes = 1 << 20
)

// Campaign is the campaign workspace a directory belongs to: the root that
// carries the marker, plus the identity read from it.
//
// The agent's campaign was implicit until now — it knew its working directory
// and nothing about which workspace that directory lived in, so a launch
// redirected by a stray environment variable looked exactly like a normal one
// until the agent acted on another campaign's work items (WI-742a80).
type Campaign struct {
	Name string
	ID   string
	// Root is the absolute path of the directory holding .campaign/campaign.yaml.
	// Empty means no campaign was found, which is what Found reports.
	Root string
}

// Found reports whether dir was inside a campaign at all.
func (c Campaign) Found() bool { return c.Root != "" }

// Describe renders the one-line human form shared by the grounding block, the
// serve banner, and `paths`. A campaign whose marker could not be read is still
// a campaign, so it is named "unnamed" rather than denied.
func (c Campaign) Describe() string {
	if !c.Found() {
		return "none"
	}
	name := c.Name
	if name == "" {
		name = "unnamed"
	}
	return fmt.Sprintf("%s (root: %s)", name, c.Root)
}

// campaignCache memoizes detection per absolute directory. The grounding block
// is rebuilt on every turn, and for a running agent the answer cannot change:
// the work directory is resolved once at startup, and a campaign root does not
// move under it.
var campaignCache sync.Map // string -> Campaign

// CampaignFor is DetectCampaign behind a process-lifetime cache, for the callers
// that ask repeatedly (the per-turn system prompt). Use DetectCampaign when a
// fresh answer matters.
func CampaignFor(dir string) Campaign {
	abs, ok := absCampaignStart(dir)
	if !ok {
		return Campaign{}
	}
	if hit, cached := campaignCache.Load(abs); cached {
		return hit.(Campaign)
	}
	found := detectCampaignFrom(abs)
	campaignCache.Store(abs, found)
	return found
}

// DetectCampaign walks up from dir to the nearest campaign root — the first
// ancestor holding .campaign/campaign.yaml — and reads its name and id.
//
// The marker file IS the detection. A campaign.yaml that cannot be read or
// parsed still reports a campaign with an empty Name: the agent is standing in
// one either way, and answering "none" there is the mistake this exists to
// prevent. An empty dir is not resolved against the process cwd — a caller that
// does not know its working directory must not be told it is in whatever
// campaign the binary happens to sit in.
//
// The walk is lexical, so a symlinked ancestor cannot send it around a cycle,
// and every step shortens the path until it reaches the filesystem root.
func DetectCampaign(dir string) Campaign {
	abs, ok := absCampaignStart(dir)
	if !ok {
		return Campaign{}
	}
	return detectCampaignFrom(abs)
}

func detectCampaignFrom(abs string) Campaign {
	for {
		marker := filepath.Join(abs, campaignMarkerDir, campaignMarkerFile)
		if info, err := os.Stat(marker); err == nil && info.Mode().IsRegular() {
			found := Campaign{Root: abs}
			if info.Size() <= maxCampaignMarkerBytes {
				found.Name, found.ID = readCampaignMarker(marker)
			}
			return found
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return Campaign{}
		}
		abs = parent
	}
}

// absCampaignStart normalizes the directory the walk starts from. It reports
// false for an empty path rather than substituting the cwd.
func absCampaignStart(dir string) (string, bool) {
	dir = expandHomePath(strings.TrimSpace(dir))
	if dir == "" {
		return "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	return abs, true
}

// readCampaignMarker reads name and id out of a campaign.yaml. Parsing is
// tolerant: unknown fields are ignored so the campaign format can grow, and an
// unreadable or malformed file yields empty strings rather than an error —
// the caller has already established that the campaign exists.
func readCampaignMarker(path string) (name, id string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var doc struct {
		Name string `yaml:"name"`
		ID   string `yaml:"id"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", ""
	}
	return strings.TrimSpace(doc.Name), strings.TrimSpace(doc.ID)
}
