package tui

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
)

func (systemClipboard) ReadImage() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		f, err := os.CreateTemp("", "obey-clipboard-*")
		if err != nil {
			return nil, err
		}
		path := f.Name()
		_ = f.Close()
		defer os.Remove(path)
		script := `on run argv
try
 set picture to the clipboard as «class GIFf»
on error
 try
  set picture to the clipboard as «class PNGf»
 on error
  return
 end try
end try
set target to open for access POSIX file (item 1 of argv) with write permission
try
 write picture to target
 close access target
on error
 close access target
end try
end run`
		if err = exec.CommandContext(ctx, "osascript", "-e", script, path).Run(); err != nil {
			return nil, nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, chatmedia.MaxBytes+1))
	}
	for _, args := range [][]string{{"wl-paste", "--no-newline", "--type", "image/png"}, {"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}} {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			continue
		}
		if err = cmd.Start(); err != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(pipe, chatmedia.MaxBytes+1))
		_ = pipe.Close()
		err = cmd.Wait()
		if readErr == nil && err == nil && len(data) > 0 {
			return data, nil
		}
	}
	return nil, nil
}
