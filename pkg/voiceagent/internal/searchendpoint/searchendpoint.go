// Package searchendpoint lets a test send web_search to a server of its own.
// Only the voice agent's own packages can import it, and no configuration or
// command sets it.
package searchendpoint

import "context"

type key struct{}

// With makes every web_search under ctx post to url.
func With(ctx context.Context, url string) context.Context {
	return context.WithValue(ctx, key{}, url)
}

// From is the endpoint set on ctx, or "" for the default search service.
func From(ctx context.Context) string {
	url, _ := ctx.Value(key{}).(string)
	return url
}
