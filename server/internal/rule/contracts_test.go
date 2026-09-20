package rule

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestRequirementInputContractAppliesDefaultsAndConstraints(t *testing.T) {
	requirement := models.CollectionRequirementSpec{
		RequiredInputs: []models.RequirementInput{{
			Name: "query", Type: models.RequirementValueString,
			Constraints: map[string]any{"minLength": 2, "pattern": "^[a-z]+$"},
		}},
		OptionalInputs: []models.RequirementInput{
			{Name: "limit", Type: models.RequirementValueNumber, Default: 10,
				Constraints: map[string]any{"minimum": 1, "maximum": 100}},
			{Name: "mode", Type: models.RequirementValueString, Default: "fast",
				Constraints: map[string]any{"enum": []any{"fast", "complete"}}},
			{Name: "credential", Type: models.RequirementValueString, Secret: true},
		},
	}
	schema, err := BuildRequirementInputSchema(requirement)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["additionalProperties"] != false {
		t.Fatalf("input contract must reject undeclared values: %s", schema)
	}
	properties := decoded["properties"].(map[string]any)
	if properties["credential"].(map[string]any)["x-secret"] != true {
		t.Fatalf("secret marker missing from schema: %s", schema)
	}

	normalized, err := ApplyAndValidateTaskInputs(schema, models.JSON(`{"query":"books"}`))
	if err != nil {
		t.Fatal(err)
	}
	var inputs map[string]any
	if err := json.Unmarshal(normalized, &inputs); err != nil {
		t.Fatal(err)
	}
	if inputs["limit"] != float64(10) || inputs["mode"] != "fast" || inputs["query"] != "books" {
		t.Fatalf("defaults were not applied to immutable input snapshot: %v", inputs)
	}

	invalid := []models.JSON{
		models.JSON(`{}`),
		models.JSON(`{"query":"A"}`),
		models.JSON(`{"query":"books","limit":101}`),
		models.JSON(`{"query":"books","unknown":true}`),
		models.JSON(`{"query":"books","credential":"plaintext"}`),
	}
	for _, supplied := range invalid {
		if _, err := ApplyAndValidateTaskInputs(schema, supplied); !errors.Is(err, ErrInvalidTaskInput) {
			t.Fatalf("expected invalid input for %s, got %v", supplied, err)
		}
	}
}

func TestRequirementInputContractRejectsInvalidDefinitions(t *testing.T) {
	tests := []models.CollectionRequirementSpec{
		{RequiredInputs: []models.RequirementInput{{Name: "", Type: models.RequirementValueString}}},
		{RequiredInputs: []models.RequirementInput{{Name: "x", Type: models.RequirementValueString}}, OptionalInputs: []models.RequirementInput{{Name: "x", Type: models.RequirementValueString}}},
		{OptionalInputs: []models.RequirementInput{{Name: "x", Type: models.RequirementValueNumber, Default: 0, Constraints: map[string]any{"minimum": 1}}}},
		{OptionalInputs: []models.RequirementInput{{Name: "x", Type: models.RequirementValueString, Constraints: map[string]any{"format": "email"}}}},
	}
	for _, requirement := range tests {
		if _, err := BuildRequirementInputSchema(requirement); !errors.Is(err, ErrInvalidTaskInput) {
			t.Fatalf("expected invalid contract for %+v, got %v", requirement, err)
		}
	}
}

func TestValidateTaskResultAcceptsRowsAndRejectsInvalidBatches(t *testing.T) {
	schema := models.JSON(`{
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"price":{"type":"number","minimum":0},
			"tags":{"type":"array","items":{"type":"string"},"maxItems":2}
		},
		"required":["name","price"],
		"additionalProperties":false
	}`)
	valid := []any{
		map[string]any{"name": "A", "price": float64(1)},
		[]any{
			map[string]any{"name": "A", "price": float64(1)},
			map[string]any{"name": "B", "price": float64(2), "tags": []any{"new"}},
		},
	}
	for _, payload := range valid {
		if err := ValidateTaskResult(schema, payload); err != nil {
			t.Fatalf("valid result rejected: %v", err)
		}
	}

	invalid := []any{
		[]any{},
		map[string]any{"name": "missing price"},
		map[string]any{"name": "negative", "price": float64(-1)},
		map[string]any{"name": "extra", "price": float64(1), "unknown": true},
		map[string]any{"name": "tags", "price": float64(1), "tags": []any{"a", "b", "c"}},
	}
	for _, payload := range invalid {
		if err := ValidateTaskResult(schema, payload); !errors.Is(err, ErrInvalidTaskResult) {
			t.Fatalf("invalid result accepted: %#v (%v)", payload, err)
		}
	}
	if err := ValidateTaskResult(models.JSON(`{}`), map[string]any{}); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("expected invalid root schema error, got %v", err)
	}
}
