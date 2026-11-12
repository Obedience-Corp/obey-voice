//go:build !windows

package brain

// promptArgLimit bounds the one argument Grok and Cursor take their request in.
var promptArgLimit = unixArgLimit
