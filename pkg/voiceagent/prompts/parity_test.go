package prompts_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// TestSamanthaPersonaGolden pins the samantha persona against the golden
// captured from the original hard-coded brain.GetSystemPrompt before the
// loader wiring. She is no longer the default — obey is — but every install
// that was talking to her still resolves this document, so it must not drift.
// It is also no longer reachable through Default(): only by its own name.
func TestSamanthaPersonaGolden(t *testing.T) {
	got := resolveNamed(t, prompts.KindPersona, "samantha", "TestAgent")
	assertGolden(t, "testdata/persona.samantha.golden", got)
}

// TestVeronicaPersonaGolden pins the shipped companion persona. She shares
// Samantha's Her-inspired speech style and is reached by name, not Default().
func TestVeronicaPersonaGolden(t *testing.T) {
	got := resolveNamed(t, prompts.KindPersona, "veronica", "TestAgent")
	assertGolden(t, "testdata/persona.veronica.golden", got)
}

// TestJarvisPersonaGolden pins the shipped Iron Man-inspired companion.
// Reached by name, not Default(); Phonon Edwin is the matching voice.
func TestJarvisPersonaGolden(t *testing.T) {
	got := resolveNamed(t, prompts.KindPersona, "jarvis", "TestAgent")
	assertGolden(t, "testdata/persona.jarvis.golden", got)
}

// TestDylanPersonaGolden pins the shipped companion. The opening is the
// owner's own prompt, kept verbatim; the group and tool sections are Santa's.
func TestDylanPersonaGolden(t *testing.T) {
	got := resolveNamed(t, prompts.KindPersona, "dylan", "TestAgent")
	assertGolden(t, "testdata/persona.dylan.golden", got)
}

// TestDefaultPersonaGolden pins the shipped default persona: assembled and
// resolved with an agent name, it must equal the checked-in golden. Changing
// the identity every fresh install wakes up with is a product decision, so it
// takes a deliberate golden update.
func TestDefaultPersonaGolden(t *testing.T) {
	doc, err := prompts.Default(prompts.KindPersona)
	if err != nil {
		t.Fatalf("Default(persona) error = %v", err)
	}
	if doc.Prompt.Name != "obey" {
		t.Errorf("default persona name = %q, want obey", doc.Prompt.Name)
	}
	assertGolden(t, "testdata/persona.obey.golden", resolveDefault(t, prompts.KindPersona, "TestAgent"))
}

func assertGolden(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	wantText := strings.TrimSuffix(string(want), "\n")
	if got != wantText {
		t.Errorf("assembled prompt diverges from %s at byte %d:\ngot:  %q\nwant: %q", path, firstDiff(got, wantText), got, wantText)
	}
}

// TestDefaultTurnGolden pins the embedded per-turn instruction against
// accidental drift.
//
// The migration-safety contract is unchanged and still checked: the instruction
// must still OPEN with the exact literal the Claude and Grok paths appended
// before the loader wiring. What may follow it is a short, explicitly enumerated
// set of deliberate additions — so an accidental edit still fails, while an
// intentional one has to be declared here and justified in turn.yaml's metadata.
func TestDefaultTurnGolden(t *testing.T) {
	got := resolveDefault(t, prompts.KindTurn, "Obey")

	const original = "Respond as Obey. 2-3 sentences max, natural speech, NO markdown, NO formatting, NO code blocks, NO bullet points. Just talk naturally."
	if !strings.HasPrefix(got, original) {
		t.Fatalf("assembled turn instruction no longer opens with the original literal, "+
			"diverging at byte %d:\ngot:  %q\nwant prefix: %q", firstDiff(got, original), got, original)
	}

	// Every deliberate addition, newest last. Adding a line here is the point at
	// which someone has to justify changing what every provider is told.
	//
	// Empty on purpose. R-L3's short-opening instruction lived here and was
	// removed by D012 after measuring: it showed no benefit on either model
	// tested and moved the wrong way on the one actually in use. The list stays
	// so the next addition has somewhere to be declared.
	var additions []string
	want := original
	for _, add := range additions {
		want += "\n" + add
	}
	if got != want {
		t.Errorf("assembled turn instruction has an undeclared change at byte %d:\ngot:  %q\nwant: %q",
			firstDiff(got, want), got, want)
	}
}

func resolveDefault(t *testing.T, kind prompts.Kind, agentName string) string {
	t.Helper()
	doc, err := prompts.Default(kind)
	if err != nil {
		t.Fatalf("Default(%s) error = %v", kind, err)
	}
	return resolvePlaceholders(t, doc, agentName)
}

// resolveNamed goes through the resolver with no user dir, which is how a
// shipped document that is not its kind's default is reached.
func resolveNamed(t *testing.T, kind prompts.Kind, name, agentName string) string {
	t.Helper()
	doc, err := prompts.Resolver{}.Resolve(kind, name)
	if err != nil {
		t.Fatalf("Resolve(%s, %s) error = %v", kind, name, err)
	}
	return resolvePlaceholders(t, doc, agentName)
}

func resolvePlaceholders(t *testing.T, doc *prompts.Document, agentName string) string {
	t.Helper()
	got, err := prompts.ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": agentName})
	if err != nil {
		t.Fatalf("ResolvePlaceholders() error = %v", err)
	}
	return got
}

func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}
