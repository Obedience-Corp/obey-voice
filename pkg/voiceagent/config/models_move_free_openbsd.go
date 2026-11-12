//go:build openbsd

package config

import "golang.org/x/sys/unix"

func freeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.F_bavail) * int64(st.F_bsize), nil
}
