package netapi

import "testing"

func TestIsPublicPathIncludesStage(t *testing.T) {
	for _, path := range []string{"/", "/index.html", "/app.js", "/stage.html", "/stage.js", "/v1/pair"} {
		if !isPublicPath(path) {
			t.Fatalf("expected public path %q", path)
		}
	}
	if isPublicPath("/v1/stream") {
		t.Fatal("/v1/stream must stay authenticated")
	}
}
