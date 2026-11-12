package config

import (
	"bytes"
	"strings"
)

// keyValue is one key of a multi-key write.
type keyValue struct {
	Key   string
	Value any
}

// setKeyFileValuesFrom writes several keys in one locked read-modify-write
// of config.yaml, so either all of them land or none do. The pairs are
// decided from the file as read under the write lock: build gets each key's
// text value in config.yaml (false when the file does not set it) and returns
// what to write. With dryRun it only checks that the edit would apply and
// writes nothing.
func setKeyFileValuesFrom(build func(fileValue func(key string) (string, bool)) []keyValue, dryRun bool) ([]keyValue, error) {
	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()
	path := ConfigFile()
	if !dryRun {
		release, err := openConfigForWrite(path, "")
		if err != nil {
			return nil, err
		}
		defer release()
	}
	data, existed, doc, err := readConfigDocument(path, "")
	if err != nil {
		return nil, err
	}
	pairs := build(func(key string) (string, bool) {
		value, found := yamlValueAt(doc.Content[0], strings.Split(key, "."))
		text, ok := value.(string)
		return text, found && ok
	})
	if len(pairs) == 0 {
		return nil, nil
	}
	specs := make([]KeySpec, len(pairs))
	values := make([]any, len(pairs))
	for i, p := range pairs {
		spec, err := resolveSpec(p.Key, false)
		if err != nil {
			return nil, err
		}
		value, err := normalizeToSpec(spec, p.Value)
		if err != nil {
			return nil, err
		}
		specs[i], values[i] = spec, value
	}
	patched := data
	for i, spec := range specs {
		segments := strings.Split(spec.Key, ".")
		if current, found := yamlValueAt(doc.Content[0], segments); found && sameValue(current, values[i]) {
			continue
		}
		next, err := patchConfigSource(patched, doc, segments, values[i])
		if err != nil {
			return nil, writeOrShapeFailed(spec.Key, "updating config", err)
		}
		if _, err := verifyPatched(next, segments, values[i]); err != nil {
			return nil, writeFailed(spec.Key, "updating config", err)
		}
		if doc, err = migrationYAMLDocument(next); err != nil {
			return nil, writeFailed(spec.Key, "updating config", err)
		}
		patched = next
	}
	refreshed := make([]any, len(specs))
	for i, spec := range specs {
		got, err := verifyPatched(patched, strings.Split(spec.Key, "."), values[i])
		if err != nil {
			return nil, writeFailed(spec.Key, "updating config", err)
		}
		refreshed[i] = got
	}
	if dryRun {
		return pairs, nil
	}
	if !bytes.Equal(patched, data) {
		if _, err := backupAndReplace(path, patched, existed, specs[0].Key); err != nil {
			return nil, err
		}
	}
	for i, spec := range specs {
		Set(spec.Key, refreshed[i])
	}
	return pairs, nil
}
