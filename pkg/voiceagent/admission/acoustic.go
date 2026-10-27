package admission

import "context"

type acousticNameKey struct{}

// WithAcousticName attaches an utterance's keyword observation to one admission
// call. It must not be interpreted as an identity or permission to answer.
func WithAcousticName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, acousticNameKey{}, name)
}

func AcousticName(ctx context.Context) string {
	name, _ := ctx.Value(acousticNameKey{}).(string)
	return name
}
