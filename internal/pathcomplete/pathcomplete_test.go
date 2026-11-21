package pathcomplete

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testShortcuts() map[string]string {
	return map[string]string{
		"p":  "projects/",
		"f":  "festivals/",
		"w":  "workflow/",
		"d":  "docs/",
		"a":  "ai_docs/",
		"de": "workflow/design/",
		"r":  "workflow/reviews/",
		"du": "dungeon/",
		"i":  ".campaign/intents/",
	}
}

func missingRoot(t *testing.T) string {
	return filepath.Join(t.TempDir(), "nonexistent")
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, file := range files {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAtQuery(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		cursor    int
		wantQuery string
		wantAt    int
	}{
		{"no @ in line", "hello world", 5, "", -1},
		{"cursor right after @", "see @", 5, "", 4},
		{"partial prefix", "see @p", 6, "p", 4},
		{"full prefix with slash", "see @p/", 7, "p/", 4},
		{"prefix with partial name", "see @p/fest", 11, "p/fest", 4},
		{"@ at start of line", "@w/design", 9, "w/design", 0},
		{"cursor not in @ word", "@p/test and more text", 18, "", -1},
		{"@ mid-sentence", "use @f/ for festivals", 7, "f/", 4},
		{"cursor at 0", "hello", 0, "", -1},
		{"email address", "mail me at user@example.com", 27, "", -1},
		{"after newline", "first\n@de", 9, "de", 6},
		{"multibyte before token", "héllo @prø", 10, "prø", 6},
		{"cursor inside token", "@projects/obey please", 5, "proj", 0},
		{"cursor past end clamps", "@ab", 99, "ab", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, at := AtQuery(tt.line, tt.cursor)
			if query != tt.wantQuery || at != tt.wantAt {
				t.Fatalf("AtQuery(%q, %d) = %q, %d; want %q, %d", tt.line, tt.cursor, query, at, tt.wantQuery, tt.wantAt)
			}
		})
	}
}

func TestFuzzyContains(t *testing.T) {
	tests := []struct {
		haystack, needle string
		want             bool
	}{
		{"projects", "p", true},
		{"projects", "prj", true},
		{"festivals", "fest", true},
		{"festivals", "fl", true},
		{"projects", "xyz", false},
		{"abc", "abcd", false},
		{"Projects", "proj", true},
		{"", "", true},
		{"abc", "", true},
		{"Ünïcode", "ünc", true},
		{"日本語メモ", "本メ", true},
		{"é", "e", false},
	}
	for _, tt := range tests {
		t.Run(tt.haystack+"/"+tt.needle, func(t *testing.T) {
			if got := FuzzyContains(tt.haystack, tt.needle); got != tt.want {
				t.Fatalf("FuzzyContains(%q, %q) = %v, want %v", tt.haystack, tt.needle, got, tt.want)
			}
		})
	}
}

func TestCandidatesEmptyQueryListsEveryShortcutPathSorted(t *testing.T) {
	candidates := Candidates("", missingRoot(t), testShortcuts())
	want := []string{"@.campaign/intents/", "@ai_docs/", "@docs/", "@dungeon/", "@festivals/", "@projects/", "@workflow/", "@workflow/design/", "@workflow/reviews/"}
	if !slices.Equal(candidates, want) {
		t.Fatalf("candidates = %v, want %v (no cap)", candidates, want)
	}
}

func TestCandidatesFuzzyMatchKey(t *testing.T) {
	candidates := Candidates("de", missingRoot(t), testShortcuts())
	if !slices.Contains(candidates, "@workflow/design/") {
		t.Fatalf("expected @workflow/design/ for 'de', got %v", candidates)
	}
}

func TestCandidatesFuzzyMatchPath(t *testing.T) {
	candidates := Candidates("design", missingRoot(t), testShortcuts())
	if !slices.Contains(candidates, "@workflow/design/") {
		t.Fatalf("expected @workflow/design/ for 'design', got %v", candidates)
	}
}

func TestCandidatesNoMatch(t *testing.T) {
	if candidates := Candidates("zzz", missingRoot(t), testShortcuts()); len(candidates) != 0 {
		t.Fatalf("expected no matches for 'zzz', got %v", candidates)
	}
}

func TestCandidatesFuzzyMatchMultiple(t *testing.T) {
	candidates := Candidates("w", missingRoot(t), testShortcuts())
	if !slices.Contains(candidates, "@workflow/") {
		t.Fatalf("expected @workflow/ for 'w', got %v", candidates)
	}
}

func TestCandidatesDirectoryListing(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "projects/fest", "projects/camp")
	writeFiles(t, root, "projects/README.md")
	candidates := Candidates("projects/", root, testShortcuts())
	want := []string{"@projects/camp/", "@projects/fest/", "@projects/README.md"}
	if !slices.Equal(candidates, want) {
		t.Fatalf("candidates = %v, want %v", candidates, want)
	}
}

func TestCandidatesFuzzyFilter(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "projects/fest", "projects/camp", "projects/obey-daemon")
	candidates := Candidates("projects/fe", root, testShortcuts())
	if !slices.Equal(candidates, []string{"@projects/fest/"}) {
		t.Fatalf("candidates = %v, want [@projects/fest/]", candidates)
	}
}

func TestCandidatesHiddenFilesExcluded(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "projects/.hidden", "projects/visible", ".git")
	writeFiles(t, root, ".env")
	for _, query := range []string{"projects/", "", "./"} {
		for _, c := range Candidates(query, root, nil) {
			if strings.Contains(c, "/.") || strings.HasPrefix(c, "@.") && !strings.HasPrefix(c, "@./") {
				t.Fatalf("query %q listed hidden entry %q", query, c)
			}
		}
	}
}

func TestCandidatesExplicitHiddenShortcutPathStillLists(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, ".campaign/intents/inbox")
	if got := Candidates(".campaign/intents/", root, testShortcuts()); !slices.Equal(got, []string{"@.campaign/intents/inbox/"}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestCandidatesNestedPath(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "workflow/design/architecture", "workflow/design/mockups")
	candidates := Candidates("workflow/design/", root, testShortcuts())
	if !slices.Equal(candidates, []string{"@workflow/design/architecture/", "@workflow/design/mockups/"}) {
		t.Fatalf("candidates = %v", candidates)
	}
}

func TestCandidatesNestedPathFilter(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "workflow/design/architecture", "workflow/design/mockups")
	candidates := Candidates("workflow/design/arch", root, testShortcuts())
	if !slices.Equal(candidates, []string{"@workflow/design/architecture/"}) {
		t.Fatalf("candidates = %v", candidates)
	}
}

func TestCandidatesDeduplicatesPaths(t *testing.T) {
	candidates := Candidates("", missingRoot(t), map[string]string{"d": "docs/", "docs": "docs/"})
	if !slices.Equal(candidates, []string{"@docs/"}) {
		t.Fatalf("candidates = %v", candidates)
	}
}

func TestCandidatesRootEntriesFollowShortcuts(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "projects", "notes", ".campaign")
	writeFiles(t, root, "README.md")
	got := Candidates("", root, map[string]string{"p": "projects/", "w": "workflow/"})
	want := []string{"@projects/", "@workflow/", "@notes/", "@./README.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestCandidatesNoSlashQueryMatchesShortcutsThenRootEntries(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "designs", "old-design")
	writeFiles(t, root, "DESIGN.md")
	got := Candidates("de", root, testShortcuts())
	want := []string{"@workflow/design/", "@dungeon/", "@./DESIGN.md", "@designs/", "@old-design/"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestCandidatesWithoutCampaignListRootOnly(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "src")
	writeFiles(t, root, "go.mod")
	if got := Candidates("", root, nil); !slices.Equal(got, []string{"@./go.mod", "@src/"}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestCandidatesDotSlashListsRoot(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "src", "reports")
	writeFiles(t, root, "README.md", "main.go")
	got := Candidates("./RE", root, testShortcuts())
	want := []string{"@./README.md", "@./reports/"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	if got := Candidates("./", root, nil); len(got) != 4 {
		t.Fatalf("./ listed %v", got)
	}
}

func TestCandidatesPrefixRanksBeforeFuzzy(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "projects/a-obey", "projects/obey-voice", "projects/xoxbxexy", "projects/OBEY-app")
	got := Candidates("projects/obey", root, nil)
	want := []string{"@projects/OBEY-app/", "@projects/obey-voice/", "@projects/a-obey/", "@projects/xoxbxexy/"}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestCandidatesHaveNoCap(t *testing.T) {
	root := t.TempDir()
	for i := range 30 {
		mkdirs(t, root, filepath.Join("projects", "p"+strings.Repeat("x", i)))
	}
	if got := Candidates("projects/", root, nil); len(got) != 30 {
		t.Fatalf("got %d candidates, want 30", len(got))
	}
}

func TestCandidatesMissingOrFileDirectoryGivesNothing(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "README.md")
	for _, query := range []string{"missing/", "README.md/", "missing/deeper/x"} {
		if got := Candidates(query, root, nil); len(got) != 0 {
			t.Fatalf("query %q listed %v", query, got)
		}
	}
}

func TestCandidatesStayInsideRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	mkdirs(t, parent, "root/inner", "sibling")
	for _, query := range []string{"../", "/", "/etc/", "inner/../../", "../sibling/"} {
		if got := Candidates(query, root, nil); len(got) != 0 {
			t.Fatalf("query %q escaped the root: %v", query, got)
		}
	}
}

func TestCandidatesRuneSafe(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "проекты/данные", "日本/メモ")
	writeFiles(t, root, "проекты/отчёт.md")
	if got := Candidates("проекты/дн", root, nil); !slices.Equal(got, []string{"@проекты/данные/"}) {
		t.Fatalf("candidates = %v", got)
	}
	if got := Candidates("日", root, nil); !slices.Equal(got, []string{"@日本/"}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestCandidatesFollowSymlinkedDirectories(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "real")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if got := Candidates("li", root, nil); !slices.Equal(got, []string{"@linked/"}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestEveryCandidateContainsSlash(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "projects/app", "docs")
	writeFiles(t, root, "README.md", "projects/app/main.go", "notes.txt")
	for _, query := range []string{"", "r", "no", "./", "./R", "projects/", "projects/app/", "p", "de"} {
		for _, c := range Candidates(query, root, testShortcuts()) {
			if !strings.HasPrefix(c, "@") || !strings.Contains(c[1:], "/") {
				t.Fatalf("query %q produced %q without a slash", query, c)
			}
		}
	}
}

func TestExpandShortcut(t *testing.T) {
	sc := testShortcuts()
	if got, ok := ExpandShortcut("p", sc); !ok || got != "projects/" {
		t.Fatalf("p expanded to %q, %v", got, ok)
	}
	for _, key := range []string{"", "nope", "p/x"} {
		if got, ok := ExpandShortcut(key, sc); ok {
			t.Fatalf("%q expanded to %q", key, got)
		}
	}
	if _, ok := ExpandShortcut("p", nil); ok {
		t.Fatal("expanded without shortcuts")
	}
}

func writeJumps(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".campaign", "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jumps.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadShortcutsUsesTheBlockAsIs(t *testing.T) {
	root := t.TempDir()
	writeJumps(t, root, `paths:
    projects: projects/
shortcuts:
    a:
        path: ai_docs/
        description: Jump to AI docs directory
    cfg:
        description: Config commands
        concept: config
    cr:
        path: workflow/code_reviews
    p:
        path: projects/
        concept: project
`)
	got := LoadShortcuts(root)
	want := map[string]string{"a": "ai_docs/", "cr": "workflow/code_reviews/", "p": "projects/"}
	if len(got) != len(want) {
		t.Fatalf("shortcuts = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("shortcuts = %v, want %v", got, want)
		}
	}
}

func TestLoadShortcutsFallsBackToDefaults(t *testing.T) {
	missing := t.TempDir()
	noBlock := t.TempDir()
	writeJumps(t, noBlock, "paths:\n    projects: projects/\n")
	malformed := t.TempDir()
	writeJumps(t, malformed, "shortcuts: [unterminated\n")
	for name, root := range map[string]string{"missing": missing, "no block": noBlock, "malformed": malformed} {
		got := LoadShortcuts(root)
		if got["p"] != "projects/" || got["de"] != "workflow/design/" || got["i"] != ".campaign/intents/" {
			t.Fatalf("%s: shortcuts = %v", name, got)
		}
	}
}

func TestLoadShortcutsEmptyBlockMeansNone(t *testing.T) {
	root := t.TempDir()
	writeJumps(t, root, "shortcuts: {}\n")
	if got := LoadShortcuts(root); len(got) != 0 {
		t.Fatalf("shortcuts = %v", got)
	}
}
