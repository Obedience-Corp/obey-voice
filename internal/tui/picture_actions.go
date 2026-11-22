package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	tea "github.com/charmbracelet/bubbletea"
)

// The desktop calls are package variables so the test binary can replace them
// with a guard that refuses to open a viewer or fill the real clipboard.
var (
	systemOpenPicture         = openPictureFile
	systemClipboardWriteImage = writeImageClipboard
)

// imageClipboard is a clipboard that can hold a picture as well as text.
type imageClipboard interface {
	WriteImage(ctx context.Context, path, format string) error
}

func (systemClipboard) WriteImage(ctx context.Context, path, format string) error {
	return systemClipboardWriteImage(ctx, path, format)
}

// openPictureFile starts the desktop viewer and reports whether it started.
// Some xdg-open fallbacks stay running until the viewer closes; that viewer
// is the user's and must outlive the conversation, so it is never killed.
func openPictureFile(ctx context.Context, path string) error {
	if runtime.GOOS == "windows" {
		return errors.New("open the attachment through your desktop file viewer")
	}
	if path == "" {
		return errors.New("this picture has no file in the attachments folder")
	}
	name, args := desktopOpener(path)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not open %s: %w", path, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("could not open %s: %w", path, err)
		}
	case <-ctx.Done():
	}
	return nil
}

func (m conversationModel) lifetime() context.Context {
	if m.deps.ctx != nil {
		return m.deps.ctx
	}
	return context.Background()
}

// openPictureRun opens the original in the desktop viewer.
func (m conversationModel) openPictureRun(path string) func() error {
	open := m.openPicture
	if open == nil {
		open = systemOpenPicture
	}
	ctx := m.lifetime()
	return func() error { return open(ctx, path) }
}

func (m conversationModel) openPictureCmd(path string) tea.Cmd {
	run := m.openPictureRun(path)
	return func() tea.Msg { return mediaOpenError{run()} }
}

// pictureFormats is every format a picture is copied in, in the order a paste
// asks the clipboard for them: an animation before a still.
var pictureFormats = []string{"gif", "png", "jpeg"}

// AppleScript reads the file as the pasteboard type of its own format, so a
// JPEG goes on the clipboard as a JPEG rather than re-encoded.
var appleScriptPictureClass = map[string]string{
	"png":  "«class PNGf»",
	"jpeg": "«class JPEG»",
	"gif":  "«class GIFf»",
}

var pictureMIME = map[string]string{
	"png":  "image/png",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
}

func clipboardWriteScript(class string) string {
	return "on run argv\nset the clipboard to (read (POSIX file (item 1 of argv)) as " + class + ")\nend run"
}

func writeImageClipboard(ctx context.Context, path, format string) error {
	return writeImageClipboardOn(ctx, runtime.GOOS, path, format)
}

func writeImageClipboardOn(ctx context.Context, goos, path, format string) error {
	class, ok := appleScriptPictureClass[format]
	if !ok || path == "" {
		return fmt.Errorf("cannot copy a %q picture", format)
	}
	switch goos {
	case "darwin":
		if out, err := exec.CommandContext(ctx, "osascript", "-e", clipboardWriteScript(class), path).CombinedOutput(); err != nil {
			return fmt.Errorf("copy the picture with osascript: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "windows":
		return errors.New("copying a picture is not supported on Windows; copy its path instead")
	}
	mime := pictureMIME[format]
	var errs []error
	for _, args := range [][]string{{"wl-copy", "--type", mime}, {"xclip", "-selection", "clipboard", "-t", mime, "-i"}} {
		err := pipeFileTo(ctx, path, args)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	return fmt.Errorf("copy the picture (install wl-clipboard or xclip): %w", errors.Join(errs...))
}

// Both tools fork to keep serving the selection, so the file is their input
// and their output is left unconnected: a pipe would wait on the fork.
func pipeFileTo(ctx context.Context, path string, args []string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = f
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	return nil
}

func (m conversationModel) copyPictureRun(r chatmedia.Ref) func() error {
	board, ok := m.clipboard().(imageClipboard)
	path, ctx := chatmedia.DefaultStore().Path(r), m.lifetime()
	return func() error {
		if !ok {
			return errors.New("this clipboard holds only text")
		}
		return board.WriteImage(ctx, path, r.Format)
	}
}

func pictureFileName(r chatmedia.Ref) string {
	name := filepath.Base(strings.TrimSpace(r.Name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "picture." + r.Format
	}
	return name
}

func defaultSaveTarget(r chatmedia.Ref) string {
	return "~/Downloads/" + pictureFileName(r)
}

// savePictureCopy writes the stored original to target, or into target under
// the picture's own name when target is a folder. It never replaces a file
// that is already there.
func savePictureCopy(store chatmedia.Store, r chatmedia.Ref, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("type where to save the copy")
	}
	path := target
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find your home folder: %w", err)
		}
		path = expandHome(path, home)
	}
	// Abs drops a trailing separator, which is how a folder is typed.
	folder := strings.HasSuffix(path, "/") || strings.HasSuffix(path, string(filepath.Separator))
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", target, err)
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, pictureFileName(r))
		target = filepath.Join(target, pictureFileName(r))
	} else if folder {
		return "", fmt.Errorf("folder %s does not exist", target)
	}
	data, err := store.Read(r)
	if err != nil {
		return "", fmt.Errorf("read the picture: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	switch {
	case errors.Is(err, fs.ErrExist):
		return "", fmt.Errorf("%s already exists; choose another name", target)
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("folder %s does not exist", filepath.Dir(target))
	case err != nil:
		return "", fmt.Errorf("save to %s: %w", target, err)
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("save to %s: %w", target, err)
	}
	return path, nil
}
