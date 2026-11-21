package config

import "strings"

// MentionRoot is the directory an @path in a chat message is relative to, and
// whether it is a campaign root. workDir stands in for the conversation's
// default directory when cfg carries none; empty means WorkDirFrom(cfg).
//
// It is derived from the conversation's default directory rather than the
// working directory: each persona carries its own pinned directory and the
// process cwd never changes, so only the campaign root of the default (or the
// default itself outside a campaign) means the same thing whoever answers.
func MentionRoot(cfg *Config, workDir string) (root string, campaign bool) {
	dir := ""
	if cfg != nil && cfg.ConversationDirectories != nil {
		dir = strings.TrimSpace(cfg.ConversationDirectories.Default.Dir)
	}
	if dir == "" {
		dir = strings.TrimSpace(workDir)
	}
	if dir == "" {
		dir = WorkDirFrom(cfg)
	}
	if c := CampaignFor(dir); c.Found() {
		return c.Root, true
	}
	return dir, false
}

// MentionRootStated reports whether the model's prompt says what an @path is
// relative to. That line is part of the environment block, so with the block
// off a completed path has to carry the root itself.
func MentionRootStated(cfg *Config) bool {
	return cfg == nil || cfg.EnvironmentContextEnabled
}
