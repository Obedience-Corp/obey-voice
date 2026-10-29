// Package campdirs lists registered local camps as persona starting-directory
// choices. It lives beside persona rather than inside it so persona stays free
// of the meeting package's audio dependencies.
package campdirs

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/meeting"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// Choices is the picker payload shared by `persona directories --json` and the
// TUI form. Campaigns hold only local roots that validate today; Notice
// carries a nonfatal discovery failure.
type Choices struct {
	Campaigns []meeting.Campaign `json:"campaigns"`
	Notice    string             `json:"notice,omitempty"`
}

// Timeout caps one `camp list --json` probe.
const Timeout = 3 * time.Second

// Discover lists registered local camps. Failure is nonfatal: a custom
// directory is always available, so the result carries a notice instead of an
// error. No camp executable means an empty list with no notice.
func Discover(ctx context.Context, run meeting.Runner, look meeting.LookPath) Choices {
	result := Choices{Campaigns: []meeting.Campaign{}}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	checkedRun := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		data, err := run(ctx, name, args...)
		if err == nil && !bytes.Contains(data, []byte("[")) {
			err = fmt.Errorf("camp returned no JSON campaign list")
		}
		return data, err
	}
	camps, err := meeting.ListCampaigns(ctx, checkedRun, look)
	if err != nil {
		result.Notice = "Cannot load registered camps: " + err.Error()
		return result
	}
	for _, c := range camps {
		if c.Path == "" {
			continue
		}
		path, err := config.ValidateStartingDirectory(c.Path)
		if err != nil {
			continue
		}
		c.Path = path
		result.Campaigns = append(result.Campaigns, c)
	}
	return result
}

// DiscoverDefault runs Discover against the real camp CLI.
func DiscoverDefault(ctx context.Context) Choices {
	return Discover(ctx, meeting.DefaultRunner, meeting.DefaultLookPath)
}
