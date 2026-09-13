package brain

import "strings"

const historicalAssistantMarkerOpen = "[Historical assistant: "

// historicalAssistantMarkerFilter removes only complete legacy attribution
// markers. It holds a possible marker across stream chunks, but releases it
// unchanged when the candidate becomes invalid or the stream ends.
type historicalAssistantMarkerFilter struct {
	hold string
}

func (f *historicalAssistantMarkerFilter) Feed(chunk string) string {
	if f.hold != "" {
		chunk = f.hold + chunk
		f.hold = ""
	}
	return f.scan(chunk)
}

func (f *historicalAssistantMarkerFilter) Flush() string {
	hold := f.hold
	f.hold = ""
	return hold
}

func (f *historicalAssistantMarkerFilter) scan(s string) string {
	var out strings.Builder
	for len(s) > 0 {
		start := strings.Index(s, historicalAssistantMarkerOpen)
		if start < 0 {
			keep := markerPrefixSuffix(s)
			out.WriteString(s[:len(s)-len(keep)])
			f.hold = keep
			return out.String()
		}
		out.WriteString(s[:start])
		candidate := s[start:]
		end := strings.IndexByte(candidate[len(historicalAssistantMarkerOpen):], ']')
		if end < 0 {
			if strings.ContainsAny(candidate, "\r\n") {
				// A newline makes this an ordinary bracketed phrase.
				out.WriteByte(candidate[0])
				s = candidate[1:]
				continue
			}
			f.hold = candidate
			return out.String()
		}
		end += len(historicalAssistantMarkerOpen)
		if strings.ContainsAny(candidate[:end], "\r\n") {
			out.WriteString(candidate[:end+1])
			s = candidate[end+1:]
			continue
		}
		if end == len(historicalAssistantMarkerOpen) {
			// An attribution name is required; preserve an empty bracketed phrase.
			out.WriteString(candidate[:end+1])
		}
		s = candidate[end+1:]
	}
	return out.String()
}

func markerPrefixSuffix(s string) string {
	max := len(historicalAssistantMarkerOpen)
	if len(s) < max {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(s, historicalAssistantMarkerOpen[:n]) {
			return s[len(s)-n:]
		}
	}
	return ""
}

func stripHistoricalAssistantMarkers(s string) string {
	var f historicalAssistantMarkerFilter
	return f.Feed(s) + f.Flush()
}
