package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/internal/searchendpoint"
)

const (
	duckDuckGoHTMLURL = "https://html.duckduckgo.com/html/"
	maxWebBodyBytes   = 1 << 20
	maxWebTextBytes   = 32 << 10
)

type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

func webHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported URL scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}

func toolWebSearch(ctx context.Context, args map[string]any) (string, toolEnd) {
	endpoint := searchendpoint.From(ctx)
	if endpoint == "" {
		endpoint = duckDuckGoHTMLURL
	}
	return toolWebSearchWithClient(ctx, args, webHTTPClient(), endpoint)
}

// webFailed is the "error <reason>" text a web tool returns, and its failed
// end. status is the HTTP status received, zero when there was no response.
func webFailed(reason string, status int) (string, toolEnd) {
	end := toolFailed(reason)
	end.httpStatus = status
	return "error " + reason, end
}

// webCut is webFailed for a request or body read that returned err, which
// is a cancel when err is the call's cancellation.
func webCut(err error, reason string, status int) (string, toolEnd) {
	text, end := webFailed(reason, status)
	if errors.Is(err, context.Canceled) {
		end.status = events.ToolOutcomeCanceled
	}
	return text, end
}

// redirectStatus is the status of the response a refused redirect returned
// with err, zero without one. Its body is already closed.
func redirectStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func webSucceeded(status int) toolEnd {
	end := toolSucceeded()
	end.httpStatus = status
	return end
}

func toolWebSearchWithClient(ctx context.Context, args map[string]any, client *http.Client, endpoint string) (string, toolEnd) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return failWith("query is required")
	}

	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return webFailed(fmt.Sprintf("creating web search request: %v", err), 0)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ObeyVoice/1.0; +https://github.com/Obedience-Corp/obey-voice)")

	resp, err := client.Do(req)
	if err != nil {
		return webCut(err, fmt.Sprintf("searching web: %v", err), redirectStatus(resp))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return webFailed(fmt.Sprintf("searching web: HTTP %d", resp.StatusCode), resp.StatusCode)
	}

	limited := &io.LimitedReader{R: resp.Body, N: maxWebBodyBytes}
	doc, err := html.Parse(limited)
	if err != nil {
		return webCut(err, fmt.Sprintf("parsing web search results: %v", err), resp.StatusCode)
	}
	end := webSucceeded(resp.StatusCode)
	end.truncated = cutAtCap(resp, limited.N <= 0)
	results := parseDuckDuckGoResults(doc, 5)
	if len(results) == 0 {
		return fmt.Sprintf(`{"query":%s,"results":[],"message":"No search results found."}`, strconv.Quote(query)), end
	}
	out, _ := json.Marshal(map[string]any{"query": query, "results": results})
	return string(out), end
}

// cutAtCap reports whether a body read up to the raw cap lost anything there.
// A declared length settles it. Without one, reaching the cap counts as cut:
// reading on for proof would hold a ready result on a stream that stays open.
func cutAtCap(resp *http.Response, reachedCap bool) bool {
	if !reachedCap {
		return false
	}
	return resp.ContentLength < 0 || resp.ContentLength > maxWebBodyBytes
}

func parseDuckDuckGoResults(doc *html.Node, limit int) []webSearchResult {
	var results []webSearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && hasClass(n, "result") {
			link := findDescendantByClass(n, "result__a")
			if link != nil {
				title := nodeText(link)
				href := attr(link, "href")
				if title != "" && href != "" {
					snippet := nodeText(findDescendantByClass(n, "result__snippet"))
					results = append(results, webSearchResult{Title: title, URL: unwrapDuckDuckGoURL(href), Snippet: snippet})
				}
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return results
}

func toolFetchURL(ctx context.Context, args map[string]any) (string, toolEnd) {
	return toolFetchURLWithClient(ctx, args, webHTTPClient())
}

func toolFetchURLWithClient(ctx context.Context, args map[string]any, client *http.Client) (string, toolEnd) {
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return failWith("url must be an absolute HTTP or HTTPS URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return webFailed(fmt.Sprintf("creating fetch request: %v", err), 0)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ObeyVoice/1.0; +https://github.com/Obedience-Corp/obey-voice)")
	resp, err := client.Do(req)
	if err != nil {
		return webCut(err, fmt.Sprintf("fetching URL: %v", err), redirectStatus(resp))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return webFailed(fmt.Sprintf("fetching URL: HTTP %d", resp.StatusCode), resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBodyBytes))
	if err != nil {
		return webCut(err, fmt.Sprintf("reading URL: %v", err), resp.StatusCode)
	}
	capped := cutAtCap(resp, len(data) == maxWebBodyBytes)
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	content := string(data)
	title := ""
	if strings.Contains(contentType, "html") || strings.Contains(strings.ToLower(content), "<html") {
		doc, parseErr := html.Parse(strings.NewReader(content))
		if parseErr != nil {
			return webFailed(fmt.Sprintf("parsing fetched page: %v", parseErr), resp.StatusCode)
		}
		title = nodeText(findElement(doc, "title"))
		content = readableText(doc)
	}
	content = strings.TrimSpace(content)
	end := webSucceeded(resp.StatusCode)
	end.truncated = capped || len(content) > maxWebTextBytes
	content = truncateWebText(content)
	out, _ := json.Marshal(map[string]any{
		"url":     resp.Request.URL.String(),
		"status":  resp.StatusCode,
		"title":   title,
		"content": content,
	})
	return string(out), end
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findDescendantByClass(n *html.Node, class string) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && hasClass(n, class) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findDescendantByClass(child, class); found != nil {
			return found
		}
	}
	return nil
}

func findElement(n *html.Node, name string) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, name) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, name); found != nil {
			return found
		}
	}
	return nil
}

func nodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
			b.WriteByte(' ')
		}
		for child := cur.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func readableText(doc *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript", "svg":
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

func unwrapDuckDuckGoURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	return raw
}

func truncateWebText(text string) string {
	if len(text) <= maxWebTextBytes {
		return text
	}
	return text[:maxWebTextBytes] + "\n... (truncated)"
}
