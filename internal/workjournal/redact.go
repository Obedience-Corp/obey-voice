package workjournal

import (
	"net/url"
	"regexp"
	"strings"
)

var secretPattern = regexp.MustCompile(`(?i)(["']?(?:access[_-]?token|refresh[_-]?token|token|api[_-]?key|password|secret|private[_-]?key)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^&\s,;}]+)`)
var authorizationPattern = regexp.MustCompile(`(?im)(\b(?:proxy-authorization|authorization|cookie|set-cookie)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\r\n,;]+)`)
var bearerPattern = regexp.MustCompile(`(?i)(\bbearer\s+)[^\s,"']+`)
var pemPattern = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|\z)`)
var urlPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
var credentialTokenPattern = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,})\b`)

func redactText(s string, limit int) string {
	s = pemPattern.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = urlPattern.ReplaceAllStringFunc(s, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[REDACTED URL]"
		}
		if u.User != nil {
			u.User = url.User("REDACTED")
		}
		q := u.Query()
		for key := range q {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "signature") || strings.Contains(lower, "credential") || lower == "key" || lower == "api_key" || lower == "apikey" || lower == "authorization" {
				q.Set(key, "REDACTED")
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	})
	s = authorizationPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = bearerPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = credentialTokenPattern.ReplaceAllString(s, "[REDACTED]")
	if limit > 0 && len(s) > limit {
		s = s[:limit]
	}
	return strings.ToValidUTF8(s, "")
}
