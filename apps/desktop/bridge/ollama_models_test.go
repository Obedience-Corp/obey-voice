package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListOllamaModels(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
		fails      bool
	}{
		{name: "available", body: `{"models":[{"name":"qwen:latest"},{"model":"llama3"}]}`, want: 2},
		{name: "empty", body: `{"models":[]}`, want: 0},
		{name: "unreachable response", body: `unavailable`, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.fails {
					http.Error(w, tc.body, http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			models, err := listOllamaModels(t.Context(), server.URL)
			if (err != nil) != tc.fails {
				t.Fatalf("err = %v, fails = %v", err, tc.fails)
			}
			if !tc.fails && len(models.([]string)) != tc.want {
				t.Fatalf("models = %#v, want count %d", models, tc.want)
			}
		})
	}
	if _, err := listOllamaModels(t.Context(), "http://127.0.0.1:1"); err == nil {
		t.Fatal("expected unreachable Ollama error")
	}
}
