package workjournal

import (
	"testing"
	"unicode/utf8"
)

func TestRedactTextAdversarialCredentialsAndUTF8(t *testing.T) {
	in := "Authorization: Bearer abc token=xyz https://user:pass@example.test/x?api_key=secret\xff"
	got := redactText(in, 4096)
	for _, leak := range []string{"abc", "xyz", "pass", "secret"} {
		if contains(got, leak) {
			t.Fatalf("redacted text leaked %q: %q", leak, got)
		}
	}
	if !utf8.ValidString(got) {
		t.Fatal("output is not valid UTF-8")
	}
}

func TestRedactTextRedactsEveryCookiePair(t *testing.T) {
	in := "Cookie: harmless=one; session=second-secret; csrf=third-secret\nSet-Cookie: sid=fourth-secret; theme=dark"
	got := redactText(in, 4096)
	for _, leak := range []string{"one", "second-secret", "third-secret", "fourth-secret", "dark"} {
		if contains(got, leak) {
			t.Fatalf("cookie value leaked %q: %q", leak, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
