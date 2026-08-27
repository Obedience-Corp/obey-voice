package scene

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const sceneSchemaPath = "../../docs/schemas/scene.schema.json"

// schemaObject is the slice of JSON Schema this test walks. Anything else in
// the file (descriptions, patterns) is documentation the drift check
// deliberately does not police.
type schemaObject struct {
	Type                 string                  `json:"type"`
	Required             []string                `json:"required"`
	AdditionalProperties *bool                   `json:"additionalProperties"`
	Properties           map[string]schemaObject `json:"properties"`
	Items                *schemaObject           `json:"items"`
	MinItems             *int                    `json:"minItems"`
	Enum                 []string                `json:"enum"`
	Const                *string                 `json:"const"`
}

func loadSceneSchema(t *testing.T) schemaObject {
	t.Helper()
	data, err := os.ReadFile(sceneSchemaPath)
	if err != nil {
		t.Fatalf("reading scene.schema.json: %v", err)
	}
	var schema schemaObject
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("scene.schema.json is not valid JSON: %v", err)
	}
	return schema
}

// yamlFields returns the yaml key of every serialized field of a struct type,
// paired with the field type so nested objects can be walked. Fields tagged
// yaml:"-" are not on disk and so are not in the schema.
func yamlFields(t *testing.T, typ reflect.Type) map[string]reflect.Type {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("yamlFields(%s): not a struct", typ)
	}
	out := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = field.Type
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// nestedObject unwraps a field type down to the struct the schema describes: a
// pointer's element, or a slice's element through the schema's items. It
// reports false for anything that is not an object, so scalars and []string
// end the walk.
func nestedObject(typ reflect.Type, prop schemaObject) (reflect.Type, schemaObject, bool) {
	for {
		switch typ.Kind() {
		case reflect.Pointer:
			typ = typ.Elem()
		case reflect.Slice:
			if prop.Items == nil {
				return nil, schemaObject{}, false
			}
			typ, prop = typ.Elem(), *prop.Items
		case reflect.Struct:
			return typ, prop, true
		default:
			return nil, schemaObject{}, false
		}
	}
}

// assertObjectMatches compares one struct against one schema object in both
// directions. A field with no property means the schema is stale; a property
// with no field means the schema documents something that cannot be written.
// additionalProperties must stay false so a typo in a script is caught by an
// editor as well as by the strict YAML decoder.
func assertObjectMatches(t *testing.T, path string, typ reflect.Type, obj schemaObject) {
	t.Helper()
	if obj.AdditionalProperties == nil || *obj.AdditionalProperties {
		t.Errorf("%s: additionalProperties must be false so unknown keys are a hard failure", path)
	}
	fields := yamlFields(t, typ)
	for name, fieldType := range fields {
		prop, ok := obj.Properties[name]
		if !ok {
			t.Errorf("%s: struct field %q has no schema property (schema has %v)", path, name, sortedKeys(obj.Properties))
			continue
		}
		if child, childSchema, ok := nestedObject(fieldType, prop); ok {
			assertObjectMatches(t, path+"."+name, child, childSchema)
		}
	}
	for name := range obj.Properties {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s: schema property %q has no struct field (struct has %v)", path, name, sortedKeys(fields))
		}
	}
}

// TestSceneSchemaMatchesDocument fails on any divergence in either direction
// between internal/scene.Document and docs/schemas/scene.schema.json, so an
// editor validating a script sees the same shape the loader enforces.
func TestSceneSchemaMatchesDocument(t *testing.T) {
	assertObjectMatches(t, "scene", reflect.TypeOf(Document{}), loadSceneSchema(t))
}

// The document is versioned; the schema has to name the version it describes.
func TestSceneSchemaPinsSchemaConst(t *testing.T) {
	prop, ok := loadSceneSchema(t).Properties["schema"]
	if !ok {
		t.Fatal("schema property missing")
	}
	if prop.Const == nil || *prop.Const != Schema {
		t.Fatalf("schema const = %v, want %q", prop.Const, Schema)
	}
}

// Every required key must be a real field, or a valid document fails schema
// validation for a property nothing can supply.
func TestSceneSchemaRequiredKeysExist(t *testing.T) {
	fields := yamlFields(t, reflect.TypeOf(Document{}))
	for _, name := range loadSceneSchema(t).Required {
		if _, ok := fields[name]; !ok {
			t.Errorf("schema requires %q, which is not a Document field", name)
		}
	}
}

// A mode the schema allows but the loader rejects would send an editor's user
// down a path the loader refuses, and vice versa.
func TestSceneSchemaModeEnumMatchesLoader(t *testing.T) {
	enum := loadSceneSchema(t).Properties["mode"].Enum
	if len(enum) == 0 {
		t.Fatal("mode has no enum")
	}
	for _, value := range enum {
		if _, err := parseMode("mode", value); err != nil {
			t.Errorf("schema allows mode %q but the loader rejects it: %v", value, err)
		}
	}
	for _, value := range []string{"", string(ModeVerbatim), string(ModeGenerate)} {
		if !containsString(enum, value) {
			t.Errorf("the loader accepts mode %q but the schema enum %v does not list it", value, enum)
		}
	}
}

// The schema stays N-cast ready (minItems 2) while ValidateV1 records exactly
// two. Dropping minItems would let a one-voice script reach the renderer.
func TestSceneSchemaCastMinItems(t *testing.T) {
	cast := loadSceneSchema(t).Properties["cast"]
	if cast.MinItems == nil || *cast.MinItems != castSize {
		t.Fatalf("cast minItems = %v, want %d", cast.MinItems, castSize)
	}
}

// This package is compiled into the cgo-free integration binary, so it must not
// reach the sherpa-backed audio and tts packages even transitively. Synthesis,
// WAV writing and resampling are injected by the command layer instead.
func TestSceneHasNoAudioOrTTSDependency(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch dep {
		case "github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio",
			"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts":
			t.Errorf("internal/scene depends on %s; inject the capability instead", dep)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
