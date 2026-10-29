package prompts

import (
	"fmt"
	"sort"
)

// RenderSystem resolves a named system document through the normal user-file
// override layer and substitutes runtime data in one pass. Invalid overrides
// return the packaged YAML text plus an advisory error, so an incomplete edit
// cannot remove the application's conversation context.
func RenderSystem(dir, name string, values map[string]string) (string, error) {
	render := func(doc *Document) (string, error) {
		allowed := make([]string, 0, len(values))
		for key := range values {
			allowed = append(allowed, key)
		}
		sort.Strings(allowed)
		return ResolvePlaceholders(doc.Assemble(), allowed, values)
	}
	doc, err := (Resolver{UserDir: dir}).Resolve(KindSystem, name)
	if err == nil {
		var text string
		text, err = render(doc)
		if err == nil {
			return text, nil
		}
	}
	overrideErr := fmt.Errorf("system prompt %q: %w", name, err)
	doc, err = resolveEmbedded(KindSystem, name)
	if err != nil {
		return "", fmt.Errorf("%v; packaged prompt: %w", overrideErr, err)
	}
	text, err := render(doc)
	if err != nil {
		return "", fmt.Errorf("%v; packaged prompt: %w", overrideErr, err)
	}
	return text, fmt.Errorf("%w; using packaged YAML", overrideErr)
}
