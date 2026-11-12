//go:build windows

package config

import "golang.org/x/sys/windows"

var errCrossDevice error = windows.ERROR_NOT_SAME_DEVICE
