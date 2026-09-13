//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import "errors"

func acquireLockGuard(string) (func(), error) {
	return nil, errors.New("config lock guards are unsupported on this platform")
}
