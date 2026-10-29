package campdirs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	look := func(string) (string, error) { return "camp", nil }
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Registry updated\n[{\"name\":\"local\",\"path\":\"" + root + "\"},{\"name\":\"remote\",\"path\":\"\"},{\"name\":\"missing\",\"path\":\"/no-such-camp\"}]"), nil
	}
	got := Discover(t.Context(), run, look)
	if len(got.Campaigns) != 1 || got.Campaigns[0].Name != "local" || got.Campaigns[0].Path != root || got.Notice != "" {
		t.Fatalf("%+v", got)
	}
	absent := func(string) (string, error) { return "", os.ErrNotExist }
	if got := Discover(t.Context(), run, absent); len(got.Campaigns) != 0 || got.Notice != "" {
		t.Fatalf("no camp executable must be an empty list without a notice: %+v", got)
	}
	for _, run := range []func(context.Context, string, ...string) ([]byte, error){
		func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("failed") },
		func(context.Context, string, ...string) ([]byte, error) { return []byte("[{broken]"), nil },
		func(context.Context, string, ...string) ([]byte, error) { return []byte("no json here"), nil },
	} {
		if got := Discover(t.Context(), run, look); got.Notice == "" || len(got.Campaigns) != 0 {
			t.Fatalf("%+v", got)
		}
	}
}
