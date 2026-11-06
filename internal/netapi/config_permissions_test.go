package netapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigPermissionRequiresGrantAndRevokesImmediately(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := creds.MintDeviceToken("phone")
	if err != nil {
		t.Fatal(err)
	}
	second, err := creds.MintDeviceToken("tablet")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &Server{opts: Options{Credentials: creds, ConfigManagement: &ConfigManagement{
		Settings:   func() (any, error) { calls++; return []string{"settings"}, nil },
		SetSetting: func(key, value string) (any, error) { calls++; return map[string]string{"key": key}, nil },
	}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/config-access", s.handleConfigAccess)
	mux.HandleFunc("GET /v1/manage/settings", s.handleManagedSettings)
	mux.HandleFunc("PATCH /v1/manage/settings", s.handleManagedSettingEdit)
	handler := s.authMiddleware(mux)
	check := func(method, path, token, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	check("GET", "/v1/manage/settings", creds.Token, "", 403)
	check("GET", "/v1/manage/settings", first.Token, "", 403)
	check("GET", "/v1/manage/settings", "invalid", "", 401)
	if calls != 0 {
		t.Fatal("denied requests invoked callback")
	}
	if err := SetDeviceConfigPermission(dir, first.ID, true); err != nil {
		t.Fatal(err)
	}
	check("GET", "/v1/manage/settings", first.Token, "", 200)
	check("GET", "/v1/manage/settings", second.Token, "", 403)
	check("GET", "/v1/manage/settings", creds.Token, "", 403)
	check("PATCH", "/v1/manage/settings", first.Token, `{"field":"x","value":"v","extra":true}`, 400)
	check("PATCH", "/v1/manage/settings", first.Token, `{"field":"x","value":"v"} {}`, 400)
	check("PATCH", "/v1/manage/settings", first.Token, `{"field":"x","value":"v"}`, 200)
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
	reloaded, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/config-access", nil)
	req.Header.Set("Authorization", "Bearer "+first.Token)
	id, allowed, err := reloaded.requestConfigIdentity(req)
	if err != nil || !allowed || id != first.ID {
		t.Fatalf("after restart id=%q allowed=%v err=%v", id, allowed, err)
	}
	list, err := ListConfigDevices(dir)
	if err != nil || len(list) != 2 {
		t.Fatalf("devices %v: %v", list, err)
	}
	raw, _ := json.Marshal(list)
	if bytes.Contains(raw, []byte(first.Token)) || bytes.Contains(raw, []byte(second.Token)) {
		t.Fatal("device list leaked token")
	}
	if err := SetDeviceConfigPermission(dir, first.ID, false); err != nil {
		t.Fatal(err)
	}
	check("GET", "/v1/manage/settings", first.Token, "", 403)
	if err := SetDeviceConfigPermission(dir, "../../elsewhere", true); err == nil {
		t.Fatal("accepted invalid ID")
	}
	if err := SetDeviceConfigPermission(dir, "0123456789abcdef", true); err == nil {
		t.Fatal("granted unknown device")
	}
}

func TestTouchCannotRestoreDeletedDevice(t *testing.T) {
	dir := t.TempDir()
	creds, err := LoadOrCreateCredentials(dir)
	if err != nil {
		t.Fatal(err)
	}
	device, err := creds.MintDeviceToken("phone")
	if err != nil {
		t.Fatal(err)
	}
	creds.devices.mu.Lock()
	creds.devices.byID[device.ID].LastSeen = time.Time{}
	creds.devices.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); creds.TouchToken(device.Token) }()
	}
	if _, ok, err := creds.DeleteDevice(device.ID); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	wg.Wait()
	if _, err := os.Stat(filepath.Join(dir, "tokens", device.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("deleted token restored: %v", err)
	}
}
