package config

// Where an effective value came from. There is no "flag" source: flags are
// per-run overrides that never reach the config surface.
const (
	SourceDefault = "default"
	SourceFile    = "file"
	SourceEnv     = "env"
)

// KeyValue is one key's effective value and where it came from.
type KeyValue struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// Values returns the effective value and source of every key in the schema.
// It reads the loaded state, so callers that must not trigger the persona
// migration should reach it after LoadRaw rather than Load.
func Values() map[string]KeyValue {
	schema := Schema()
	out := make(map[string]KeyValue, len(schema))
	for _, spec := range schema {
		out[spec.Key] = valueFor(spec)
	}
	return out
}

// ValueFor returns one key's effective value and source.
func ValueFor(key string) (KeyValue, ValueType, bool) {
	spec, ok := SpecFor(key)
	if !ok {
		return KeyValue{}, "", false
	}
	return valueFor(spec), spec.Type, true
}

func valueFor(spec KeySpec) KeyValue {
	value := Get(spec.Key)
	if spec.Secret {
		// A secret is reported as set-or-not, never rendered: `config get` output
		// lands in logs, support bundles and a front end's memory.
		value = ""
	}
	return KeyValue{Value: value, Source: Source(spec.Key)}
}

// Source names where key's effective value comes from: the environment when a
// variable bound to the key decides it, otherwise the config file when it holds
// the key, otherwise the built-in default.
//
// It answers through envValueFor, so it reports what viper actually resolved.
// Aliases count — a key answered by its compatibility name is still answered by
// the environment, and reporting "file" there is the lie work_dir's bare
// WORK_DIR was telling. An exported-but-empty variable does not count, because
// AllowEmptyEnv is off and viper never reads it.
func Source(key string) string {
	if _, _, ok := envValueFor(key); ok {
		return SourceEnv
	}
	if InConfig(key) {
		return SourceFile
	}
	return SourceDefault
}

// InConfig reports whether the config file holds key, as opposed to the value
// coming from a default or an environment binding.
func InConfig(key string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return v.InConfig(key)
}
