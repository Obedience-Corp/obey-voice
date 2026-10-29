package config

import "fmt"

// SetConversationMemory changes the floor and ceiling in one locked, atomic
// replacement. The existing surgical writer preserves comments and other keys.
func SetConversationMemory(tokens int) error {
	if tokens != 8192 && tokens != 16384 && tokens != 32768 {
		return fmt.Errorf("conversation memory must be 8192, 16384 or 32768")
	}
	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()
	path := ConfigFile()
	release, err := openConfigForWrite(path, "ollama_num_ctx")
	if err != nil {
		return err
	}
	defer release()
	data, existed, doc, err := readConfigDocument(path, "ollama_num_ctx")
	if err != nil {
		return err
	}
	keys := []string{"ollama_num_ctx", "continuity_num_ctx_ceil"}
	for _, key := range keys {
		data, err = patchConfigSource(data, doc, []string{key}, tokens)
		if err != nil {
			return writeOrShapeFailed(key, "updating conversation memory", err)
		}
		if _, err = verifyPatched(data, []string{key}, tokens); err != nil {
			return writeFailed(key, "verifying conversation memory", err)
		}
		doc, err = migrationYAMLDocument(data)
		if err != nil {
			return writeFailed(key, "parsing conversation memory", err)
		}
	}
	if _, err := backupAndReplace(path, data, existed, "ollama_num_ctx"); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	for _, key := range keys {
		v.Set(key, tokens)
	}
	return nil
}
