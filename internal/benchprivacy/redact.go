// Package benchprivacy prepares benchmark JSON for sharing. It preserves
// measurements, but deliberately removes raw generated text and tool arguments.
// This is a disclosure reduction policy, not a general secret scanner.
package benchprivacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const Notice = "Paths and raw reply/spoken/error/tool-argument strings redacted for sharing; measurements describe the original run. Text-level reproduction requires a new private run. Not a secret scan or rights clearance."

var homePath = regexp.MustCompile(`/(?:Users|home)/[^\s"'<>\\]+`)
var toolName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// JSON accepts one JSON document, retains number lexemes (including integers
// above 2^53), and adds a disclosure notice to object envelopes.
func JSON(data []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode benchmark: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("benchmark must contain exactly one JSON document")
	}
	value = redact(value, "")
	if obj, ok := value.(map[string]any); ok {
		obj["privacy_redaction"] = Notice
	}
	return json.MarshalIndent(value, "", "  ")
}

func redact(value any, key string) any {
	switch v := value.(type) {
	case map[string]any:
		for k, child := range v {
			v[k] = redact(child, k)
		}
	case []any:
		for i, child := range v {
			v[i] = redact(child, key)
		}
	case string:
		switch strings.ToLower(key) {
		case "reply", "spoken", "error", "setup_error", "transcript", "expected", "response", "response_text":
			if v != "" {
				return "[redacted raw text]"
			}
		case "tool_names":
			// Historical callbacks stored name + arguments here, including run_command
			// output. Keep a bare tool name only when it is syntactically unambiguous.
			if !toolName.MatchString(v) {
				return "[redacted tool arguments]"
			}
		case "tts_worker":
			if v != "" {
				return "speech-worker.invalid"
			}
		}
		return homePath.ReplaceAllString(v, "/home/demo/[redacted-path]")
	}
	return value
}
