package netapi

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const configPermissionsSubdir = "config-permissions"

// ConfigDevice describes a paired device without exposing its bearer token.
type ConfigDevice struct {
	DeviceInfo
	ConfigAllowed bool `json:"config_allowed"`
}

func validDeviceID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

func configPermissionPath(dir, id string) (string, error) {
	if !validDeviceID(id) {
		return "", fmt.Errorf("invalid device ID")
	}
	return filepath.Join(dir, configPermissionsSubdir, id), nil
}

func configAllowed(dir, id string) (bool, error) {
	path, err := configPermissionPath(dir, id)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read config permission: %w", err)
	}
	return info.Mode().IsRegular(), nil
}

// ListConfigDevices reads paired device metadata directly from disk. It does
// not create credentials or reveal tokens and works while the server is running.
func ListConfigDevices(dir string) ([]ConfigDevice, error) {
	s := newDeviceStore(dir)
	if _, err := os.Stat(s.dir); errors.Is(err, os.ErrNotExist) {
		return []ConfigDevice{}, nil
	} else if err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	devices := s.list()
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	out := make([]ConfigDevice, 0, len(devices))
	for _, d := range devices {
		if !validDeviceID(d.ID) {
			return nil, fmt.Errorf("invalid stored device ID")
		}
		allowed, err := configAllowed(dir, d.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, ConfigDevice{DeviceInfo: d, ConfigAllowed: allowed})
	}
	return out, nil
}

// SetDeviceConfigPermission changes a local-only permission. It is checked on
// every request, so revocation does not require restarting serve.
func SetDeviceConfigPermission(dir, id string, allowed bool) error {
	path, err := configPermissionPath(dir, id)
	if err != nil {
		return err
	}
	devices, err := ListConfigDevices(dir)
	if err != nil {
		return err
	}
	found := false
	for _, d := range devices {
		if d.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("paired device %q not found", id)
	}
	if !allowed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("revoke config permission: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create permissions directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		ok, checkErr := configAllowed(dir, id)
		if checkErr != nil {
			return checkErr
		}
		if !ok {
			return fmt.Errorf("invalid permission file")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("grant config permission: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("grant config permission: %w", err)
	}
	return nil
}

// requestConfigIdentity reports only the requesting named device. A shared
// primary token cannot receive this permission.
func (c *Credentials) requestConfigIdentity(r *http.Request) (string, bool, error) {
	token := presentedToken(r)
	if c.devices == nil || token == "" || constantTimeTokenMatch(token, c.Token) {
		return "", false, nil
	}
	c.devices.mu.RLock()
	rec := c.devices.byToken[token]
	id := ""
	if rec != nil {
		id = rec.ID
	}
	c.devices.mu.RUnlock()
	if id == "" {
		return "", false, nil
	}
	allowed, err := configAllowed(c.Dir, id)
	return id, allowed, err
}

func (s *Server) handleConfigAccess(w http.ResponseWriter, r *http.Request) {
	id, allowed, err := s.opts.Credentials.requestConfigIdentity(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not read permission"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "config_allowed": allowed})
}
