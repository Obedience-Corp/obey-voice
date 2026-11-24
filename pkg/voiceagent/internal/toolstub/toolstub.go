// Package toolstub lets a measurement answer a live turn's tool calls without
// running them. Only the voice agent's own packages can import it, and no
// configuration or command sets it.
package toolstub

import "context"

// Answer returns the result a tool call gets in place of running it.
type Answer func(name string, args map[string]any) string

type key struct{}

// With makes every tool call under ctx get answer's result instead of running.
func With(ctx context.Context, answer Answer) context.Context {
	return context.WithValue(ctx, key{}, answer)
}

// From is the answer set on ctx, or nil when tools run.
func From(ctx context.Context) Answer {
	answer, _ := ctx.Value(key{}).(Answer)
	return answer
}
