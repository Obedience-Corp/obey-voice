//go:build netbsd || solaris

package config

import "golang.org/x/sys/unix"

func freeSpace(path string) (int64, error) {
	var st unix.Statvfs_t
	if err := unix.Statvfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Frsize), nil
}
