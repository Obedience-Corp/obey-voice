package benchprivacy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONPreservesMeasurementsAndRemovesRawDisclosure(t *testing.T) {
	input := []byte(`{"workdir":"/Users/private/Dev/PrivateCampaign","reply":"project-a project-b","spoken":"project-a","error":"private diagnostic","empty":{"error":""},"tool_names":["web_search","run_command {private arguments}"],"results":[{"n":9007199254740993,"ms":0.1234567890123456789,"passed":true}],"tts_worker":"private-host:7263"}`)
	got, err := JSON(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"PrivateCampaign", "project-a", "private diagnostic", "private arguments", "private-host"} {
		if strings.Contains(string(got), unwanted) {
			t.Fatalf("retained %q", unwanted)
		}
	}
	for _, retained := range []string{"9007199254740993", "0.1234567890123456789", "web_search", "privacy_redaction"} {
		if !strings.Contains(string(got), retained) {
			t.Fatalf("lost %q", retained)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["empty"].(map[string]any)["error"] != "" {
		t.Fatal("empty success diagnostic became an error")
	}
	again, err := JSON(got)
	if err != nil || string(again) != string(got) {
		t.Fatal("redaction is not idempotent")
	}
}

func TestJSONRejectsMalformedAndTrailingDocuments(t *testing.T) {
	for _, input := range []string{"", "{", "{} {}", "{} trailing"} {
		if _, err := JSON([]byte(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
