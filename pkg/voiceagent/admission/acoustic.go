package admission

import "context"

type acousticNameKey struct{}

type speakerKey struct{}

// WithAcousticName attaches an utterance's keyword observation to one admission
// call. It must not be interpreted as an identity or permission to answer.
func WithAcousticName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, acousticNameKey{}, name)
}

func AcousticName(ctx context.Context) string {
	name, _ := ctx.Value(acousticNameKey{}).(string)
	return name
}

// WithSpeaker attaches the admitted input's live speaker label. Typed input
// carries none, so it never inherits whoever the microphone heard last.
func WithSpeaker(ctx context.Context, speaker string) context.Context {
	return context.WithValue(ctx, speakerKey{}, speaker)
}

func Speaker(ctx context.Context) string {
	speaker, _ := ctx.Value(speakerKey{}).(string)
	return speaker
}
