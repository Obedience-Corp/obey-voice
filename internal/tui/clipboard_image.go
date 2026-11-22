package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

func (systemClipboard) ReadImage() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return readImageClipboard(ctx, runtime.GOOS)
}

// clipboardReadScript writes the clipboard's picture to the file named by its
// argument, in the first of pictureFormats the clipboard can give.
func clipboardReadScript() string {
	var b strings.Builder
	b.WriteString("on run argv\nset found to missing value\n")
	for _, format := range pictureFormats {
		fmt.Fprintf(&b, "if found is missing value then\n try\n  set found to the clipboard as %s\n end try\nend if\n", appleScriptPictureClass[format])
	}
	b.WriteString(`if found is missing value then return
set target to open for access POSIX file (item 1 of argv) with write permission
try
 write found to target
 close access target
on error
 close access target
end try
end run`)
	return b.String()
}

func readImageClipboard(ctx context.Context, goos string) ([]byte, error) {
	if goos == "darwin" {
		f, err := os.CreateTemp("", "obey-clipboard-*")
		if err != nil {
			return nil, err
		}
		path := f.Name()
		_ = f.Close()
		defer os.Remove(path)
		if err = exec.CommandContext(ctx, "osascript", "-e", clipboardReadScript(), path).Run(); err != nil {
			return nil, nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, chatmedia.MaxSourceBytes+1))
	}
	for _, format := range pictureFormats {
		mime := pictureMIME[format]
		for _, args := range [][]string{{"wl-paste", "--no-newline", "--type", mime}, {"xclip", "-selection", "clipboard", "-t", mime, "-o"}} {
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				continue
			}
			if err = cmd.Start(); err != nil {
				continue
			}
			data, readErr := io.ReadAll(io.LimitReader(pipe, chatmedia.MaxSourceBytes+1))
			_ = pipe.Close()
			err = cmd.Wait()
			if readErr == nil && err == nil && len(data) > 0 {
				return data, nil
			}
		}
	}
	return nil, nil
}
