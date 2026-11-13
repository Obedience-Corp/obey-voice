//go:build !windows

package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

func terminalCellPixels(f *os.File) (int, int) {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 0, 0
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
}
