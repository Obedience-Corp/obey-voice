//go:build linux

package config

import "golang.org/x/sys/unix"

func freeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	unit := int64(st.Frsize)
	if unit == 0 {
		unit = int64(st.Bsize)
	}
	return int64(st.Bavail) * unit, nil
}
