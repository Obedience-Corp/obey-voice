package brain

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/imagegen"
)

type fakeGenerator struct {
	got imagegen.Request
	err error
}

func (f *fakeGenerator) Generate(_ context.Context, req imagegen.Request) ([]byte, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func TestGenerateImageIsOfferedOnlyWhereItCanRun(t *testing.T) {
	gen := &fakeGenerator{}
	if names := toolNames((&toolSession{routerPolicy: RouterToolsLegacy}).tools()); slices.Contains(names, generateImageToolName) {
		t.Fatal("offered with no picture maker configured")
	}
	if names := toolNames((&toolSession{routerPolicy: RouterToolsLegacy, imageGen: gen}).tools()); !slices.Contains(names, generateImageToolName) {
		t.Fatalf("not offered to a direct-tools turn: %v", names)
	}
	if names := toolNames((&toolSession{routerPolicy: RouterToolsRouteOnly, imageGen: gen}).tools()); slices.Contains(names, generateImageToolName) {
		t.Fatal("offered on the route-only live router")
	}
	strict := NewWorkerBudget(BudgetLimits{StrictTools: true})
	if names := toolNames((&toolSession{routerPolicy: RouterToolsLegacy, imageGen: gen, budget: strict}).tools()); slices.Contains(names, generateImageToolName) {
		t.Fatal("offered under a strict worker budget")
	}
}

func TestTurnsOfferGenerateImageWhenComfyUIIsSet(t *testing.T) {
	direct := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}
	off := &OllamaBrain{cfg: &config.Config{}}
	if hasTool(off.turnToolSession(t.Context(), direct).tools(), generateImageToolName) {
		t.Fatal("offered with comfyui_url empty")
	}
	cfg := &config.Config{ComfyUIURL: "http://127.0.0.1:1"}
	live := &OllamaBrain{cfg: cfg}
	if !hasTool(live.turnToolSession(t.Context(), direct).tools(), generateImageToolName) {
		t.Fatal("not offered to a direct live turn")
	}
	if hasTool(live.turnToolSession(t.Context(), StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}).tools(), generateImageToolName) {
		t.Fatal("offered to a route-only live turn")
	}
	worker := &OllamaBrain{cfg: config.WorkerBrainConfig(cfg)}
	if !hasTool(worker.turnToolSession(t.Context(), StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsLegacy}).tools(), generateImageToolName) {
		t.Fatal("not offered to a worker brain built from the persona's config")
	}
}

func TestImageGeneratorFollowsConfig(t *testing.T) {
	if imageGenerator(&config.Config{}) != nil {
		t.Fatal("generator built with no ComfyUI address")
	}
	if imageGenerator(&config.Config{ComfyUIURL: "not a url"}) != nil {
		t.Fatal("generator built from an unusable address")
	}
	if imageGenerator(&config.Config{ComfyUIURL: "http://127.0.0.1:1"}) == nil {
		t.Fatal("no generator for a configured address")
	}
}

var imageLine = regexp.MustCompile(`(?m)^!\[[^\]]*\]\(<([^>]+)>\)$`)

func TestGenerateImageSavesAPictureTheReplyCanAttach(t *testing.T) {
	// A space and parentheses in the path must not break the attach line.
	workDir := filepath.Join(t.TempDir(), "my work (1)")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gen := &fakeGenerator{}
	s := &toolSession{routerPolicy: RouterToolsLegacy, imageGen: gen}
	result := s.execute(t.Context(), workDir, toolCallFor(generateImageToolName, map[string]any{
		"prompt": "A [small] red boat (on) a calm lake, early morning mist, watercolor",
		"shape":  "Landscape",
	}))
	if toolResultError(result) != "" {
		t.Fatalf("result = %q", result)
	}
	if gen.got.Width != 1216 || gen.got.Height != 832 || !strings.HasPrefix(gen.got.Prompt, "A [small] red boat") {
		t.Fatalf("request = %+v", gen.got)
	}
	m := imageLine.FindStringSubmatch(result)
	if m == nil {
		t.Fatalf("no standalone image line in %q", result)
	}
	path := m[1]
	if filepath.Dir(path) != workDir || !strings.HasPrefix(filepath.Base(path), "a-small-red-boat-on-") {
		t.Fatalf("saved at %q, want the top of %q", path, workDir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	store := chatmedia.Store{Dir: t.TempDir()}
	if err := store.CheckReplyFiles("Here it is.\n"+m[0], workDir); err != nil {
		t.Fatalf("the line the tool gave does not attach: %v", err)
	}
}

func TestGenerateImageFailures(t *testing.T) {
	workDir := t.TempDir()
	cases := []struct {
		name string
		args map[string]any
		gen  *fakeGenerator
		dir  string
		want string
	}{
		{"no prompt", map[string]any{"prompt": "  "}, &fakeGenerator{}, workDir, "prompt is required"},
		{"bad shape", map[string]any{"prompt": "x", "shape": "wide"}, &fakeGenerator{}, workDir, `shape "wide"`},
		{"no work dir", map[string]any{"prompt": "x"}, &fakeGenerator{}, "", "no work directory"},
		{"server error", map[string]any{"prompt": "x"}, &fakeGenerator{err: errors.New("ComfyUI at http://h is not answering")}, workDir, "the picture was not made: ComfyUI at http://h is not answering"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &toolSession{routerPolicy: RouterToolsLegacy, imageGen: tc.gen}
			result := s.execute(t.Context(), tc.dir, toolCallFor(generateImageToolName, tc.args))
			if !strings.HasPrefix(result, "error: ") || !strings.Contains(result, tc.want) {
				t.Fatalf("result = %q, want error containing %q", result, tc.want)
			}
		})
	}
	if entries, _ := os.ReadDir(workDir); len(entries) != 0 {
		t.Fatalf("failed calls left files: %v", entries)
	}
	s := &toolSession{routerPolicy: RouterToolsLegacy}
	if result := s.execute(t.Context(), workDir, toolCallFor(generateImageToolName, map[string]any{"prompt": "x"})); !strings.Contains(result, "not available") {
		t.Fatalf("result without a picture maker = %q", result)
	}
}
