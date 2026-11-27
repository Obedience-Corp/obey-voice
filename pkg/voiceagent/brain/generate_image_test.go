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

func TestTurnsAlwaysOfferGenerateImage(t *testing.T) {
	direct := StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsDirect}
	off := &OllamaBrain{cfg: &config.Config{ComfyUIURL: "http://127.0.0.1:1"}}
	sess := off.turnToolSession(t.Context(), direct)
	if !hasTool(sess.tools(), generateImageToolName) {
		t.Fatal("not offered with the picture maker off; the model could not learn why")
	}
	result := sess.execute(t.Context(), t.TempDir(), toolCallFor(generateImageToolName, map[string]any{"prompt": "a fox"}))
	if !strings.HasPrefix(result, "error: ") || !strings.Contains(result, "picture making is off") || !strings.Contains(result, "obey-voice config set image_generation comfyui") {
		t.Fatalf("result with the picture maker off = %q", result)
	}
	cfg := &config.Config{ImageGeneration: config.ImageGenerationComfyUI, ComfyUIURL: "http://127.0.0.1:1"}
	live := &OllamaBrain{cfg: cfg}
	if hasTool(live.turnToolSession(t.Context(), StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly}).tools(), generateImageToolName) {
		t.Fatal("offered to a route-only live turn")
	}
	worker := &OllamaBrain{cfg: config.WorkerBrainConfig(cfg)}
	if !hasTool(worker.turnToolSession(t.Context(), StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsLegacy}).tools(), generateImageToolName) {
		t.Fatal("not offered to a worker brain built from the persona's config")
	}
}

func TestImageGeneratorFollowsConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"off", &config.Config{ComfyUIURL: "http://127.0.0.1:1"}, "picture making is off"},
		{"bad address", &config.Config{ImageGeneration: "comfyui", ComfyUIURL: "not a url"}, "config set comfyui_url"},
	}
	for _, tc := range cases {
		_, err := imageGenerator(tc.cfg).Generate(t.Context(), imagegen.Request{Prompt: "x"})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", tc.name, err, tc.want)
		}
	}
	if _, ok := imageGenerator(&config.Config{ImageGeneration: "ComfyUI", ComfyUIURL: "http://127.0.0.1:1"}).(*imagegen.ComfyUI); !ok {
		t.Fatal("no ComfyUI generator with ComfyUI selected")
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
