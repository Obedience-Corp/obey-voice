package decider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Keep JSON object order: Python's reference renderer preserves the input
// order, which is part of the model prompt despite JSON's unordered semantics.
type stateMember struct {
	key   string
	value any
}
type stateObject []stateMember

func renderMapikaState(raw json.RawMessage) (string, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := readState(d)
	if err != nil {
		return "", fmt.Errorf("decision state: %w", err)
	}
	if _, err = d.Token(); err != io.EOF {
		return "", fmt.Errorf("trailing decision state")
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	return writeState(annotateState(v)), nil
}
func readState(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if t == json.Delim('{') {
		out := stateObject{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			v, err := readState(d)
			if err != nil {
				return nil, err
			}
			out = append(out, stateMember{k.(string), v})
		}
		_, err = d.Token()
		return out, err
	}
	if t == json.Delim('[') {
		out := []any{}
		for d.More() {
			v, err := readState(d)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		_, err = d.Token()
		return out, err
	}
	return t, nil
}
func annotateState(v any) any {
	switch x := v.(type) {
	case stateObject:
		for i := range x {
			x[i].value = annotateState(x[i].value)
		}
		return x
	case []any:
		for i := range x {
			v := annotateState(x[i])
			if len(x) >= 8 {
				o := stateObject{{"_index", json.Number(strconv.Itoa(i))}}
				if obj, ok := v.(stateObject); ok {
					for _, p := range obj {
						if p.key == "_index" {
							o[0].value = p.value
						} else {
							o = append(o, p)
						}
					}
				} else {
					o = append(o, stateMember{"value", v})
				}
				v = o
			}
			x[i] = v
		}
		return x
	}
	return v
}
func writeState(v any) string {
	switch x := v.(type) {
	case stateObject:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = writeState(p.key) + ": " + writeState(p.value)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			parts[i] = writeState(p)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		_ = e.Encode(x)
		return strings.TrimSuffix(b.String(), "\n")
	}
}
