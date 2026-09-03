package talkhotkey

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Where the control endpoint lives, and how a client finds it. Unix names it
// by path inside a 0700 directory, which is the whole access check. Windows
// has no such path, so the listener takes a loopback port and records it —
// with a token, because every process on the machine can reach loopback.

// controlInfoFile is the Windows side channel. Unix names the endpoint by
// path, so the 0700 directory is the whole access check; a loopback port is
// reachable by every process on the machine, so it carries a token as well.
type controlInfoFile struct {
	Address string `json:"address"`
	Token   string `json:"token"`
}

// ControlEndpoint locates the per-user control endpoint a client would dial.
// On Windows that means reading the info file the listener wrote, so a missing
// file is already ErrNoListener.
func ControlEndpoint() (network, address string, err error) {
	network, address, _, err = controlTarget()
	return network, address, err
}

func controlTarget() (network, address, token string, err error) {
	if runtime.GOOS != "windows" {
		return "unix", controlSocketPath(), "", nil
	}
	path, err := controlInfoPath()
	if err != nil {
		return "tcp", "", "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "tcp", "", "", fmt.Errorf("%w (no endpoint recorded at %s)", ErrNoListener, path)
	}
	var info controlInfoFile
	if err := json.Unmarshal(raw, &info); err != nil || info.Address == "" {
		return "tcp", "", "", fmt.Errorf("talk control: %s is not readable as an endpoint record", path)
	}
	return "tcp", info.Address, info.Token, nil
}

// controlSocketPath prefers the session's runtime directory, which the OS
// already cleans up at logout; os.TempDir needs the uid in the name because it
// is shared between users.
func controlSocketPath() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); dir != "" {
		return filepath.Join(dir, controlDirName, controlSocketName)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", controlDirName, os.Getuid()), controlSocketName)
}

func controlInfoPath() (string, error) {
	dir := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if dir == "" {
		var err error
		if dir, err = os.UserConfigDir(); err != nil {
			return "", fmt.Errorf("locate the talk control record directory: %w", err)
		}
	}
	return filepath.Join(dir, controlDirName, controlInfoName), nil
}

func controlToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate the talk control token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func writeControlInfo(path string, info controlInfoFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the talk control directory: %w", err)
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encode the talk control record: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write the talk control record %s: %w", path, err)
	}
	return nil
}
