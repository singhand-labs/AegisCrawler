package rule

import (
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestNormalizeWorkflowRequirementInputReferencesAcrossRuntimeJSON(t *testing.T) {
	rule := &models.Rule{
		Steps: models.JSON(`[
			{"action":"type","value":"query={{inputs.keyword}}"},
			{"action":"type","value":"{{taskInputs.filters.query}}"}
		]`),
		Hooks:     models.JSON(`{"beforeAll":[{"action":"sendLog","message":"{{input.keyword}}"}]}`),
		Selectors: models.JSON(`{"dynamic":{"selector":"[data-query='{{inputs.keyword}}']"}}`),
	}
	requirement := models.CollectionRequirementSpec{
		RequiredInputs: []models.RequirementInput{
			{Name: "keyword", Type: models.RequirementValueString},
			{Name: "filters", Type: models.RequirementValueObject},
		},
	}

	changed, err := normalizeWorkflowRequirementInputReferences(rule, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected provider-created input namespaces to be canonicalized")
	}
	for name, value := range map[string]string{
		"steps":     string(rule.Steps),
		"hooks":     string(rule.Hooks),
		"selectors": string(rule.Selectors),
	} {
		if strings.Contains(value, "{{inputs.") ||
			strings.Contains(value, "{{input.") ||
			strings.Contains(value, "{{taskInputs.") {
			t.Fatalf("%s retained an unsupported input namespace: %s", name, value)
		}
	}
	if !strings.Contains(string(rule.Steps), "{{keyword}}") ||
		!strings.Contains(string(rule.Steps), "{{filters.query}}") ||
		!strings.Contains(string(rule.Hooks), "{{keyword}}") {
		t.Fatalf("declared input paths were not preserved: steps=%s hooks=%s", rule.Steps, rule.Hooks)
	}
}

func TestNormalizeWorkflowRequirementInputReferencesPreservesDeclaredAliasName(t *testing.T) {
	rule := &models.Rule{Steps: models.JSON(`[{"action":"type","value":"{{inputs.keyword}}"}]`)}
	requirement := models.CollectionRequirementSpec{
		RequiredInputs: []models.RequirementInput{{
			Name: "inputs", Type: models.RequirementValueObject,
		}},
	}

	changed, err := normalizeWorkflowRequirementInputReferences(rule, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !strings.Contains(string(rule.Steps), "{{inputs.keyword}}") {
		t.Fatalf("a declared input named inputs must retain normal property access: %s", rule.Steps)
	}
}

func TestValidateRequiredWorkflowInputReferencesRejectsDeclaredButUnusedInputs(t *testing.T) {
	rule := &models.Rule{
		Variables: models.JSON(`{"keyword":"","target_title":"","target_host":""}`),
		Steps: models.JSON(`[
			{"action":"type","value":"{{keyword}}"},
			{"action":"if","condition":{"type":"valueEquals","left":"{{target_title}}","right":"{{loopItem.title}}"}}
		]`),
	}
	requirement := models.CollectionRequirementSpec{RequiredInputs: []models.RequirementInput{
		{Name: "keyword", Type: models.RequirementValueString},
		{Name: "target_title", Type: models.RequirementValueString},
		{Name: "target_host", Type: models.RequirementValueString},
	}}
	if err := validateRequiredWorkflowInputReferences(rule, requirement); err == nil ||
		!strings.Contains(err.Error(), "target_host") {
		t.Fatalf("unused required input must fail closed: %v", err)
	}
	rule.Hooks = models.JSON(`{"beforeAll":[{"action":"sendLog","message":"{{target_host}}"}]}`)
	if err := validateRequiredWorkflowInputReferences(rule, requirement); err != nil {
		t.Fatalf("steps and hooks should satisfy required input bindings: %v", err)
	}
}
