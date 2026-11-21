package config

import "strings"

// MentionRoot is the directory an @path in a chat message is relative to, and
// whether it is a campaign root.
//
// It is derived from the conversation's default directory rather than the
// working directory: each persona carries its own pinned directory and the
// process cwd never changes, so only the campaign root of the default (or the
// default itself outside a campaign) means the same thing whoever answers.
func MentionRoot(cfg *Config) (root string, campaign bool) {
	dir := ""
	if cfg != nil && cfg.ConversationDirectories != nil {
		dir = strings.TrimSpace(cfg.ConversationDirectories.Default.Dir)
	}
	if dir == "" {
		dir = WorkDirFrom(cfg)
	}
	if c := CampaignFor(dir); c.Found() {
		return c.Root, true
	}
	return dir, false
}
