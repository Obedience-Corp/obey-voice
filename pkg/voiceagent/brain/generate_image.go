package brain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
			Description: "Create a new picture from a description and save it as a PNG in your work directory. Use it when the user asks you to draw, make, generate or send a picture of something that does not exist as a file. Describe the picture in detail: subject, setting, style, lighting. The result gives the file's path and the exact Markdown line that shows it in the chat; put that line in your reply. A picture can take minutes to make. If it fails, tell the user the reason it gives; never claim a picture you did not get.",
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

// pictureMakerOff is what generate_image answers while no picture maker is
// selected, so the model can tell the user what to set up instead of
// guessing why it cannot make pictures.
const pictureMakerOff = "picture making is off in Obey Voice (image_generation: off). " +
	"It needs a ComfyUI server running with an image model; Obey Voice does not install either. " +
	"Once ComfyUI is running, turn it on with `obey-voice config set image_generation comfyui` " +
	"(and `obey-voice config set comfyui_url <address>` if it is not at " + config.DefaultComfyUIURL + "), " +
	"then start a new conversation"

// unavailable answers every request with why no picture can be made.
type unavailable struct{ reason string }

func (u unavailable) Generate(context.Context, imagegen.Request) ([]byte, error) {
	return nil, errors.New(u.reason)
}

// imageGenerator is the picture maker the config selects. With none selected,
// or an unusable address, it is one that says why.
func imageGenerator(cfg *config.Config) imagegen.Generator {
	if config.ImageGenerationOf(cfg) != config.ImageGenerationComfyUI {
		return unavailable{pictureMakerOff}
	}
	gen, err := imagegen.NewComfyUI(cfg.ComfyUIURL, cfg.ImageWorkflow)
	if err != nil {
		return unavailable{err.Error() + "; fix it with `obey-voice config set comfyui_url <address>`"}
	}
	return gen
}

// generateImage makes the picture args describe and saves it at the top of
// workDir, where a reply can attach it and image repair finds it.
func generateImage(ctx context.Context, gen imagegen.Generator, workDir string, args map[string]any) (string, toolEnd) {
	// A picture maker that cannot run says so before any argument is checked:
	// fixing the call would only lead to the same refusal.
	if off, ok := gen.(unavailable); ok {
		return failWith("the picture was not made: " + off.reason)
	}
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
