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

// setKeyFileValues writes every pair in one locked read-modify-write of
// config.yaml, so either all of them land or none do. With dryRun it only
// checks that the edit would apply and writes nothing.
func setKeyFileValues(pairs []keyValue, dryRun bool) error {
	if len(pairs) == 0 {
		return nil
	}
	specs := make([]KeySpec, len(pairs))
	values := make([]any, len(pairs))
	for i, p := range pairs {
		spec, err := resolveSpec(p.Key, false)
		if err != nil {
			return err
		}
		value, err := normalizeToSpec(spec, p.Value)
		if err != nil {
			return err
		}
		specs[i], values[i] = spec, value
	}

	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()
	path := ConfigFile()
	if !dryRun {
		release, err := openConfigForWrite(path, specs[0].Key)
		if err != nil {
			return err
		}
		defer release()
	}
	data, existed, doc, err := readConfigDocument(path, specs[0].Key)
	if err != nil {
		return err
	}
	patched := data
	for i, spec := range specs {
		segments := strings.Split(spec.Key, ".")
		if current, found := yamlValueAt(doc.Content[0], segments); found && sameValue(current, values[i]) {
			continue
		}
		next, err := patchConfigSource(patched, doc, segments, values[i])
		if err != nil {
			return writeOrShapeFailed(spec.Key, "updating config", err)
		}
		if _, err := verifyPatched(next, segments, values[i]); err != nil {
			return writeFailed(spec.Key, "updating config", err)
		}
		if doc, err = migrationYAMLDocument(next); err != nil {
			return writeFailed(spec.Key, "updating config", err)
		}
		patched = next
	}
	refreshed := make([]any, len(specs))
	for i, spec := range specs {
		got, err := verifyPatched(patched, strings.Split(spec.Key, "."), values[i])
		if err != nil {
			return writeFailed(spec.Key, "updating config", err)
		}
		refreshed[i] = got
	}
	if dryRun {
		return nil
	}
	if !bytes.Equal(patched, data) {
		if _, err := backupAndReplace(path, patched, existed, specs[0].Key); err != nil {
			return err
		}
	}
	for i, spec := range specs {
		Set(spec.Key, refreshed[i])
	}
	return nil
}
