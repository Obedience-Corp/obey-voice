package brain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/imagegen"
	"github.com/ollama/ollama/api"
)

const generateImageToolName = "generate_image"

// imageShapes are the sizes generate_image offers, in pixels.
var imageShapes = map[string][2]int{
	"square":    {1024, 1024},
	"portrait":  {832, 1216},
	"landscape": {1216, 832},
}

func generateImageTool() api.Tool {
	return api.Tool{
		Type: "function",
		Function: api.ToolFunction{
			Name:        generateImageToolName,
			Description: "Create a new picture from a description and save it as a PNG in your work directory. Use it when the user asks you to draw, make, generate or send a picture of something that does not exist as a file. Describe the picture in detail: subject, setting, style, lighting. The result gives the file's path and the exact Markdown line that shows it in the chat; put that line in your reply. A picture can take minutes to make.",
			Parameters: api.ToolFunctionParameters{
				Type:     "object",
				Required: []string{"prompt"},
				Properties: orderedProps(map[string]api.ToolProperty{
					"prompt": {Type: api.PropertyType{"string"}, Description: "What the picture shows, in detail."},
					"shape":  {Type: api.PropertyType{"string"}, Enum: []any{"square", "portrait", "landscape"}, Description: "Picture shape. Default square."},
				}),
			},
		},
	}
}

// imageGenerator is the picture maker the config names, or nil when none is
// configured or the address is unusable.
func imageGenerator(cfg *config.Config) imagegen.Generator {
	if cfg == nil || strings.TrimSpace(cfg.ComfyUIURL) == "" {
		return nil
	}
	gen, err := imagegen.NewComfyUI(cfg.ComfyUIURL, cfg.ImageWorkflow)
	if err != nil {
		return nil
	}
	return gen
}

// generateImage makes the picture args describe and saves it at the top of
// workDir, where a reply can attach it and image repair finds it.
func generateImage(ctx context.Context, gen imagegen.Generator, workDir string, args map[string]any) (string, toolEnd) {
	prompt, _ := args["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return failWith("prompt is required")
	}
	shape, _ := args["shape"].(string)
	shape = strings.ToLower(strings.TrimSpace(shape))
	if shape == "" {
		shape = "square"
	}
	size, ok := imageShapes[shape]
	if !ok {
		return failWith(fmt.Sprintf("shape %q is not one of square, portrait, landscape", shape))
	}
	dir, err := filepath.Abs(workDir)
	if strings.TrimSpace(workDir) == "" || err != nil {
		return failWith("there is no work directory to save the picture in")
	}
	png, err := gen.Generate(ctx, imagegen.Request{Prompt: prompt, Width: size[0], Height: size[1]})
	if err != nil {
		return failWith("the picture was not made: " + err.Error())
	}
	path := filepath.Join(dir, pictureFileName(prompt))
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return failWith("the picture was made but not saved: " + err.Error())
	}
	return fmt.Sprintf("Created %s (%dx%d PNG). To show it in the chat, put this line on its own line in your reply:\n![%s](<%s>)", path, size[0], size[1], generatedCaption(prompt), path), toolSucceeded()
}

// pictureFileName is a short name from the prompt's first words and a random
// suffix, so pictures made from one prompt never overwrite each other.
func pictureFileName(prompt string) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)))
	}) {
		words = append(words, w)
		if len(words) == 5 {
			break
		}
	}
	if len(words) == 0 {
		words = []string{"picture"}
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	return strings.Join(words, "-") + "-" + hex.EncodeToString(suffix) + ".png"
}

// generatedCaption is the prompt's first few words, safe inside ![...].
func generatedCaption(prompt string) string {
	words := strings.Fields(strings.NewReplacer("[", "", "]", "", "(", "", ")", "").Replace(prompt))
	if len(words) > 8 {
		words = words[:8]
	}
	return strings.TrimRight(strings.Join(words, " "), ".,;:")
}
