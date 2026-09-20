package rule

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestValidateMissingName(t *testing.T) {
	err := Validate(map[string]any{"entry": "http://example.com", "steps": []any{map[string]any{"action": "navigate", "url": "http://example.com"}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateMissingEntry(t *testing.T) {
	err := Validate(map[string]any{"name": "test", "steps": []any{map[string]any{"action": "navigate"}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateInvalidEntryProtocol(t *testing.T) {
	err := Validate(map[string]any{"name": "test", "entry": "ftp://example.com", "steps": []any{map[string]any{"action": "navigate"}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateShortEntry(t *testing.T) {
	err := Validate(map[string]any{"name": "test", "entry": "x", "steps": []any{map[string]any{"action": "navigate"}}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateMissingSteps(t *testing.T) {
	err := Validate(map[string]any{"name": "test", "entry": "http://example.com"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateEmptySteps(t *testing.T) {
	err := Validate(map[string]any{"name": "test", "entry": "http://example.com", "steps": []any{}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateValidRule(t *testing.T) {
	err := Validate(map[string]any{
		"name":  "test",
		"entry": "http://example.com",
		"steps": []any{map[string]any{"action": "navigate", "url": "http://example.com"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmbeddedActionSchemaMatchesGeneratedSource(t *testing.T) {
	generated, err := os.ReadFile("../../../src/rule-engine/schemas/action.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := embeddedActionSchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(embedded, generated) {
		t.Fatal("embedded server action schema drifted; run npm run generate-schema")
	}
}

func TestGeneratedActionSchemaValidatesNamedDSLMaps(t *testing.T) {
	raw, err := embeddedActionSchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schema["definitions"].(map[string]any)
	extractMap := definitions["ExtractFieldMap"].(map[string]any)
	if extractMap["minProperties"] != float64(1) {
		t.Fatalf("extract field maps must be non-empty: %#v", extractMap)
	}
	extractValues := extractMap["additionalProperties"].(map[string]any)
	if extractValues["$ref"] != "#/definitions/ExtractField" {
		t.Fatalf("extract field map values are not schema validated: %#v", extractMap)
	}
	extractField := definitions["ExtractField"].(map[string]any)
	required := extractField["required"].([]any)
	if len(required) != 1 || required[0] != "type" {
		t.Fatalf("extract field type is not mandatory: %#v", extractField)
	}
	targetMap := definitions["TargetMap"].(map[string]any)
	targetValues := targetMap["additionalProperties"].(map[string]any)
	if targetValues["$ref"] != "#/definitions/Target" {
		t.Fatalf("selector alias values are not schema validated: %#v", targetMap)
	}
}
