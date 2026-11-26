//go:build windows

package config

import "os"

// Windows keeps a temp folder per user, and its test homes are not pruned.

func ownedPrivateDir(os.FileInfo) bool { return true }

func pruneTestHomes(string) {}

func lockTestHome(string, string) *os.File { return nil }
