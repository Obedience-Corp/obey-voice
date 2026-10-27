//go:build integration

package config

import (
	"os"
	"testing"
)

func TestAcousticNameManagedInstall(t *testing.T) {
	if os.Getenv("OBEY_KWS_INSTALL_TEST") != "1" {
		t.Skip("set OBEY_KWS_INSTALL_TEST=1 to download and verify the optional model in a temp directory")
	}
	dir := t.TempDir()
	m := AssetManifest{Schema: AssetSchema, Assets: []Asset{AcousticNameAsset()}}
	if err := ensureManifest(t.Context(), m, dir, nil); err != nil {
		t.Fatal(err)
	}
	st := m.Status(dir)
	if len(st) != 1 || !st[0].Installed {
		t.Fatalf("status=%+v", st)
	}
	if err := ensureManifest(t.Context(), m, dir, nil); err != nil {
		t.Fatal(err)
	}
}
