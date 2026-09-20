package rule

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func workflowRule(t *testing.T) *models.Rule {
	t.Helper()
	return &models.Rule{
		ID: "products", Version: "1", Name: "Products",
		Domain: models.JSON(`"example.com"`), Entry: "https://example.com/products",
		Variables: models.JSON(`{"existing":"value"}`),
		Steps: models.JSON(`[
			{"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}"},
			{"action":"click","target":{"selector":"button.search"}},
			{"action":"extract","name":"items","target":{"selector":".product"},"multiple":true,
			 "fields":{"name":{"selector":".name","type":"text"},"price":{"selector":".price","type":"number"}}},
			{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[
			 {"action":"sendResult","payload":{"name":"{{loopItem.name}}","price":"{{loopItem.price}}"},"immediate":true}
			]}
		]`),
	}
}

func workflowRequirement() models.CollectionRequirementSpec {
	return models.CollectionRequirementSpec{
		Title: "Collect products", Description: "Collect product names and prices.",
		RequiredInputs: []models.RequirementInput{{Name: "keyword", Type: models.RequirementValueString}},
		OptionalInputs: []models.RequirementInput{{Name: "maxPages", Type: models.RequirementValueNumber, Default: 2}},
		OutputFields: []models.RequirementOutputField{
			{Name: "name", Type: models.RequirementValueString, Description: "Product name"},
			{Name: "price", Type: models.RequirementValueNumber, Description: "Product price"},
		},
		SampleOutput: map[string]any{"name": "Example", "price": 10.5},
	}
}

func cloneWorkflowRule(t *testing.T, rule *models.Rule) *models.Rule {
	t.Helper()
	encoded, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	var cloned models.Rule
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return &cloned
}

func decodeWorkflowObject(t *testing.T, raw models.JSON) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func decodeWorkflowArray(t *testing.T, raw models.JSON) []any {
	t.Helper()
	var value []any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func requireWorkflowTargetKeysAbsent(t *testing.T, target map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, exists := target[key]; exists {
			t.Fatalf("target key %q was not canonicalized: %#v", key, target)
		}
	}
}

func TestNormalizeWorkflowTargetMetadataCanonicalizesOnlyTargetPositions(t *testing.T) {
	rule := &models.Rule{
		Selectors: models.JSON(`{
			"result":{"$ref":" ","selector":"#result","xpath":"\t","text":"","ariaLabel":" ","role":"","roleName":"orphan","frame":" ","shadowPath":[" ","#host","\t"],"stableSelector":"#result"},
			"button":{"selector":"#button","ariaLabel":" Save ","role":"button","roleName":"Save"}
		}`),
		Steps: models.JSON(`[
			{"action":"if","condition":{"type":"elementExists","target":{"selector":"#condition","role":" ","stableSelector":"#condition"}},"then":[
				{"action":"handleDownload","trigger":{"action":"click","target":{"selector":"#download","ariaLabel":"","stableSelector":"#download"}}},
				{"action":"extract","name":"title","target":{"selector":".item","roleName":"orphan"},"fields":{
					"title":{"type":"text","selector":".title","condition":{"type":"elementExists","target":{"selector":".title","role":"\t"}},"fields":{
						"nested":{"type":"text","condition":{"type":"elementExists","target":{"selector":".nested","ariaLabel":" "}}}
					}}
				}}
			]},
			{"action":"sendResult","payload":{"role":"","ariaLabel":" ","stableSelector":"keep"}}
		]`),
		Hooks: models.JSON(`{"cleanup":[{"action":"click","target":{"selector":"#close","role":"","stableSelector":"#close"}}]}`),
	}

	changed, err := normalizeWorkflowTargetMetadata(rule)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected target metadata normalization")
	}

	selectors := decodeWorkflowObject(t, rule.Selectors)
	result := selectors["result"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, result, "$ref", "xpath", "text", "ariaLabel", "role", "roleName", "frame", "stableSelector")
	if path := result["shadowPath"].([]any); len(path) != 1 || path[0] != "#host" {
		t.Fatalf("unexpected normalized shadow path: %#v", path)
	}
	button := selectors["button"].(map[string]any)
	if button["ariaLabel"] != " Save " || button["role"] != "button" || button["roleName"] != "Save" {
		t.Fatalf("non-blank target metadata must be preserved: %#v", button)
	}

	steps := decodeWorkflowArray(t, rule.Steps)
	ifStep := steps[0].(map[string]any)
	conditionTarget := ifStep["condition"].(map[string]any)["target"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, conditionTarget, "role", "stableSelector")
	thenSteps := ifStep["then"].([]any)
	triggerTarget := thenSteps[0].(map[string]any)["trigger"].(map[string]any)["target"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, triggerTarget, "ariaLabel", "stableSelector")
	extractStep := thenSteps[1].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, extractStep["target"].(map[string]any), "roleName")
	titleField := extractStep["fields"].(map[string]any)["title"].(map[string]any)
	titleConditionTarget := titleField["condition"].(map[string]any)["target"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, titleConditionTarget, "role")
	nestedField := titleField["fields"].(map[string]any)["nested"].(map[string]any)
	nestedConditionTarget := nestedField["condition"].(map[string]any)["target"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, nestedConditionTarget, "ariaLabel")

	payload := steps[1].(map[string]any)["payload"].(map[string]any)
	if payload["role"] != "" || payload["ariaLabel"] != " " || payload["stableSelector"] != "keep" {
		t.Fatalf("collection payload was unexpectedly normalized: %#v", payload)
	}
	hooks := decodeWorkflowObject(t, rule.Hooks)
	hookTarget := hooks["cleanup"].([]any)[0].(map[string]any)["target"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, hookTarget, "role", "stableSelector")

	changed, err = normalizeWorkflowTargetMetadata(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("target metadata normalization must be idempotent")
	}
}

func TestNormalizeWorkflowVisibleExtractionTargetsTraversesGeneratedActionTrees(t *testing.T) {
	rule := &models.Rule{
		Steps: models.JSON(`[
			{"action":"group","steps":[
				{"action":"extractText","name":"title","target":{"selector":".title","visible":false}}
			]},
			{"action":"click","target":{"selector":"button"}}
		]`),
		Hooks: models.JSON(`{"afterAll":[
			{"action":"extract","name":"items","target":{"$ref":"items"},"multiple":true,"fields":{"name":{"type":"text"}}}
		]}`),
	}

	changed, err := normalizeWorkflowVisibleExtractionTargets(rule)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected visible extraction normalization")
	}
	steps := decodeWorkflowArray(t, rule.Steps)
	nestedTarget := steps[0].(map[string]any)["steps"].([]any)[0].(map[string]any)["target"].(map[string]any)
	if nestedTarget["visible"] != true {
		t.Fatalf("nested extraction target was not enforced: %#v", nestedTarget)
	}
	if _, exists := steps[1].(map[string]any)["target"].(map[string]any)["visible"]; exists {
		t.Fatalf("non-extraction target was unexpectedly changed: %s", rule.Steps)
	}
	hooks := decodeWorkflowObject(t, rule.Hooks)
	hookTarget := hooks["afterAll"].([]any)[0].(map[string]any)["target"].(map[string]any)
	if hookTarget["visible"] != true {
		t.Fatalf("hook selector reference did not get an inline visible default: %#v", hookTarget)
	}

	changed, err = normalizeWorkflowVisibleExtractionTargets(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("visible extraction normalization must be idempotent")
	}
}

func TestNormalizeWorkflowRepeatedExtractionOnEmptyTraversesGeneratedActionTrees(t *testing.T) {
	rule := &models.Rule{
		Steps: models.JSON(`[
			{"action":"switch","cases":[{"value":"ready","steps":[
				{"action":"extract","name":"items","target":{"selector":".item"},"multiple":true,"onEmpty":"sendEmpty","fields":{"name":{"type":"text"}}}
			]}]},
			{"action":"extract","name":"single","target":{"selector":".item"},"fields":{"name":{"type":"text"}}}
		]`),
		Hooks: models.JSON(`{"afterAll":[
			{"action":"group","steps":[
				{"action":"extract","name":"hookItems","target":{"selector":".hook-item"},"multiple":true,"fields":{"name":{"type":"text"}}}
			]}
		]}`),
	}

	changed, err := normalizeWorkflowRepeatedExtractionOnEmpty(rule)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected repeated extraction empty normalization")
	}
	steps := decodeWorkflowArray(t, rule.Steps)
	caseExtract := steps[0].(map[string]any)["cases"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if caseExtract["onEmpty"] != "fail" {
		t.Fatalf("nested repeated extraction did not fail closed: %#v", caseExtract)
	}
	if _, exists := steps[1].(map[string]any)["onEmpty"]; exists {
		t.Fatalf("singular extraction was unexpectedly changed: %s", rule.Steps)
	}
	hooks := decodeWorkflowObject(t, rule.Hooks)
	hookExtract := hooks["afterAll"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if hookExtract["onEmpty"] != "fail" {
		t.Fatalf("hook repeated extraction did not fail closed: %#v", hookExtract)
	}

	changed, err = normalizeWorkflowRepeatedExtractionOnEmpty(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("repeated extraction empty normalization must be idempotent")
	}
}

func TestNormalizeWorkflowRepeatedExtractionReadinessReplacesFailedBaiduFixedWait(t *testing.T) {
	rule := &models.Rule{
		Steps: models.JSON(`[
			{"action":"type","target":{"selector":"#kw"},"value":"{{keyword}}"},
			{"action":"pressKey","keys":["Enter"]},
			{"action":"waitForTimeout","ms":2000},
			{"action":"extract","name":"items","target":{"selector":"#content_left > .result","visible":true},"multiple":true,"onEmpty":"fail",
			 "fields":{"summary":{"selector":"[role=text]","type":"text"},"title":{"selector":".t","type":"text"}}},
			{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[
			 {"action":"sendResult","payload":{"summary":"{{loopItem.summary}}","title":"{{loopItem.title}}"},"immediate":true}
			]}
		]`),
	}

	changed, err := normalizeWorkflowRepeatedExtractionReadiness(rule)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the fixed-time Baidu readiness gap to be normalized")
	}
	steps := decodeWorkflowArray(t, rule.Steps)
	if len(steps) != 6 {
		t.Fatalf("observable readiness must be inserted before the settling delay: %#v", steps)
	}
	wait := steps[2].(map[string]any)
	if wait["action"] != "waitForElementVisible" {
		t.Fatalf("fixed time is not observable navigation readiness: %#v", wait)
	}
	if steps[3].(map[string]any)["action"] != "waitForTimeout" {
		t.Fatalf("the existing fixed delay may be retained only after readiness: %#v", steps)
	}
	expectedTarget := steps[4].(map[string]any)["target"]
	if !reflect.DeepEqual(wait["target"], expectedTarget) {
		t.Fatalf("readiness must use the exact trusted extraction target: wait=%#v extract=%#v", wait["target"], expectedTarget)
	}

	changed, err = normalizeWorkflowRepeatedExtractionReadiness(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("repeated-extraction readiness normalization must be idempotent")
	}
}

func TestNormalizeWorkflowRepeatedExtractionReadinessTraversesAndPreservesEquivalentWaits(t *testing.T) {
	rule := &models.Rule{
		Steps: models.JSON(`[
			{"action":"group","steps":[
				{"action":"waitForElementVisible","target":{"selector":".nested","visible":true}},
				{"action":"extract","name":"nested","target":{"selector":".nested","visible":true},"multiple":true,"onEmpty":"fail","fields":{"name":{"type":"text"}}},
				{"action":"extractText","name":"single","target":{"selector":".single","visible":true}}
			]}
		]`),
		Hooks: models.JSON(`{"afterAll":[
			{"action":"waitForTimeout","ms":1500},
			{"action":"extract","name":"hookItems","target":{"selector":".hook-item","visible":true},"multiple":true,"onEmpty":"fail","fields":{"name":{"type":"text"}}}
		]}`),
	}

	changed, err := normalizeWorkflowRepeatedExtractionReadiness(rule)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected hook readiness normalization")
	}
	steps := decodeWorkflowArray(t, rule.Steps)
	nested := steps[0].(map[string]any)["steps"].([]any)
	if len(nested) != 3 || nested[0].(map[string]any)["action"] != "waitForElementVisible" ||
		nested[1].(map[string]any)["action"] != "extract" ||
		nested[2].(map[string]any)["action"] != "extractText" {
		t.Fatalf("equivalent readiness or non-repeated extraction was changed: %#v", nested)
	}
	hooks := decodeWorkflowObject(t, rule.Hooks)
	hookSteps := hooks["afterAll"].([]any)
	if len(hookSteps) != 3 || hookSteps[0].(map[string]any)["action"] != "waitForElementVisible" ||
		hookSteps[1].(map[string]any)["action"] != "waitForTimeout" {
		t.Fatalf("nested hook readiness was not inserted before its settling delay: %#v", hookSteps)
	}
	if !reflect.DeepEqual(
		hookSteps[0].(map[string]any)["target"],
		hookSteps[2].(map[string]any)["target"],
	) {
		t.Fatalf("hook readiness target differs from its extraction: %#v", hookSteps)
	}

	changed, err = normalizeWorkflowRepeatedExtractionReadiness(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("nested readiness normalization must be idempotent")
	}
}

func TestNormalizeWorkflowRepeatedExtractionReadinessRejectsProviderWaitBypasses(t *testing.T) {
	for _, test := range []struct {
		name string
		wait map[string]any
	}{
		{
			name: "missing visible",
			wait: map[string]any{"action": "waitForElementVisible", "target": map[string]any{"selector": ".item"}},
		},
		{
			name: "explicit unfiltered",
			wait: map[string]any{"action": "waitForElementVisible", "target": map[string]any{"selector": ".item", "visible": false}},
		},
		{
			name: "continued error",
			wait: map[string]any{"action": "waitForElementVisible", "target": map[string]any{"selector": ".item", "visible": true}, "onError": "continue"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := []any{
				test.wait,
				map[string]any{
					"action": "extract", "name": "items", "multiple": true, "onEmpty": "fail",
					"target": map[string]any{"selector": ".item", "visible": true},
					"fields": map[string]any{"name": map[string]any{"type": "text"}},
				},
			}
			encoded, err := json.Marshal(steps)
			if err != nil {
				t.Fatal(err)
			}
			rule := &models.Rule{Steps: encoded}
			changed, err := normalizeWorkflowRepeatedExtractionReadiness(rule)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("provider-authored non-equivalent wait suppressed trusted readiness")
			}
			normalized := decodeWorkflowArray(t, rule.Steps)
			if len(normalized) != 3 {
				t.Fatalf("trusted readiness was not inserted: %#v", normalized)
			}
			trusted := normalized[1].(map[string]any)
			if trusted["action"] != "waitForElementVisible" ||
				trusted["target"].(map[string]any)["visible"] != true ||
				trusted["onError"] != nil {
				t.Fatalf("inserted readiness is not visible and fail-closed: %#v", trusted)
			}
		})
	}
}

func TestNormalizeWorkflowRepeatedExtractionReadinessWrapsSingleActionTrigger(t *testing.T) {
	rule := &models.Rule{Steps: models.JSON(`[
		{"action":"handleDownload","trigger":{
			"action":"extract","name":"items","multiple":true,"onEmpty":"fail",
			"target":{"selector":".item","visible":true},
			"fields":{"name":{"type":"text"}}
		}}
	]`)}
	changed, err := normalizeWorkflowRepeatedExtractionReadiness(rule)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("single-action trigger repeated extraction was not normalized")
	}
	steps := decodeWorkflowArray(t, rule.Steps)
	trigger := steps[0].(map[string]any)["trigger"].(map[string]any)
	if trigger["action"] != "group" {
		t.Fatalf("multi-step trigger readiness must be represented as a group: %#v", trigger)
	}
	children := trigger["steps"].([]any)
	if len(children) != 2 ||
		children[0].(map[string]any)["action"] != "waitForElementVisible" ||
		children[1].(map[string]any)["action"] != "extract" {
		t.Fatalf("trigger group does not sequence readiness before extraction: %#v", children)
	}
	if !reflect.DeepEqual(
		children[0].(map[string]any)["target"],
		children[1].(map[string]any)["target"],
	) {
		t.Fatalf("trigger readiness target differs from extraction: %#v", children)
	}
	changed, err = normalizeWorkflowRepeatedExtractionReadiness(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("single-action trigger readiness normalization must be idempotent")
	}
}

func TestValidateProvisionalRuleCanonicalizesBlankTargetMetadata(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	requirement := workflowRequirement()
	requirement.RequiredInputs = nil
	generated.Selectors = models.JSON(`{"products":{"selector":".product","ariaLabel":" ","role":"","roleName":"","stableSelector":".product"}}`)
	generated.Steps = models.JSON(`[
		{"action":"click","target":{"selector":"button.search","role":"","stableSelector":"button.search"}},
		{"action":"extract","name":"items","target":{"$ref":"products"},"multiple":true,
		 "fields":{"name":{"selector":".name","type":"text"},"price":{"selector":".price","type":"number"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[
		 {"action":"sendResult","payload":{"name":"{{loopItem.name}}","price":"{{loopItem.price}}"},"immediate":true}
		]}
	]`)

	flags, err := ValidateProvisionalRule(generated, baseline, requirement)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 8 || flags[3] != targetMetadataNormalizedFlag || flags[4] != visibleExtractionNormalizedFlag ||
		flags[5] != repeatedExtractionEmptyNormalizedFlag || flags[6] != repeatedExtractionReadinessNormalizedFlag ||
		flags[7] != "result-contract:passed" {
		t.Fatalf("unexpected validation flags: %v", flags)
	}
	selector := decodeWorkflowObject(t, generated.Selectors)["products"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, selector, "ariaLabel", "role", "roleName", "stableSelector")
	clickTarget := decodeWorkflowArray(t, generated.Steps)[0].(map[string]any)["target"].(map[string]any)
	requireWorkflowTargetKeysAbsent(t, clickTarget, "role", "stableSelector")
	normalizedSteps := decodeWorkflowArray(t, generated.Steps)
	readinessStep := normalizedSteps[1].(map[string]any)
	if readinessStep["action"] != "waitForElementVisible" {
		t.Fatalf("repeated extraction readiness was not inserted: %#v", readinessStep)
	}
	extractTarget := normalizedSteps[2].(map[string]any)["target"].(map[string]any)
	if extractTarget["visible"] != true {
		t.Fatalf("selector reference extraction did not get an inline visible default: %#v", extractTarget)
	}
	if !reflect.DeepEqual(readinessStep["target"], extractTarget) {
		t.Fatalf("readiness target differs from repeated extraction target: %#v", normalizedSteps)
	}
	extractStep := normalizedSteps[2].(map[string]any)
	if extractStep["onEmpty"] != "fail" {
		t.Fatalf("repeated extraction did not fail closed: %#v", extractStep)
	}
}

func TestValidateProvisionalRuleKeepsOutputSchemaInVersionContract(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	generated.Output = models.JSON(`{"type":"object","required":["providerControlled"]}`)
	flags, err := ValidateProvisionalRule(generated, baseline, workflowRequirement())
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 7 || flags[3] != visibleExtractionNormalizedFlag ||
		flags[4] != repeatedExtractionEmptyNormalizedFlag ||
		flags[5] != repeatedExtractionReadinessNormalizedFlag ||
		flags[6] != "result-contract:passed" {
		t.Fatalf("unexpected validation flags: %v", flags)
	}
	if flags[2] != "output-contract:validated" {
		t.Fatalf("expected separately validated output contract, got %v", flags)
	}
	if len(generated.Output) != 0 {
		t.Fatalf("workflow rule must not overload engine-level output semantics: %s", generated.Output)
	}
	var schema map[string]any
	outputSchema, err := BuildRequirementOutputSchema(workflowRequirement())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(outputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if schema["type"] != "object" || len(properties) != 2 || properties["price"].(map[string]any)["type"] != "number" {
		t.Fatalf("unexpected version output contract: %#v", schema)
	}
	var variables map[string]any
	if err := json.Unmarshal(generated.Variables, &variables); err != nil {
		t.Fatal(err)
	}
	if variables["existing"] != "value" || variables["keyword"] != "" || variables["maxPages"] != float64(2) {
		t.Fatalf("unexpected variables: %#v", variables)
	}
}

func TestValidateProvisionalRuleCanonicalizesConfirmedInputNamespaces(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	generated.Steps = models.JSON(strings.ReplaceAll(
		string(generated.Steps),
		"{{keyword}}",
		"{{inputs.keyword}}",
	))

	flags, err := ValidateProvisionalRule(generated, baseline, workflowRequirement())
	if err != nil {
		t.Fatalf("confirmed input alias was not canonicalized: %v", err)
	}
	if !slices.Contains(flags, requirementInputReferencesNormalizedFlag) {
		t.Fatalf("missing input canonicalization flag: %v", flags)
	}
	steps := string(generated.Steps)
	if strings.Contains(steps, "{{inputs.keyword}}") || !strings.Contains(steps, "{{keyword}}") {
		t.Fatalf("input alias was not rewritten to the runtime variable: %s", steps)
	}

	unknown := cloneWorkflowRule(t, baseline)
	unknown.Steps = models.JSON(strings.ReplaceAll(
		string(unknown.Steps),
		"{{keyword}}",
		"{{inputs.query}}",
	))
	if _, err := ValidateProvisionalRule(unknown, baseline, workflowRequirement()); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), `undeclared requirement input "query"`) {
		t.Fatalf("unknown input alias must fail before replay: %v", err)
	}
}

func TestValidateProvisionalRuleRejectsUnusedRequiredInput(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	requirement := workflowRequirement()
	requirement.RequiredInputs = append(requirement.RequiredInputs, models.RequirementInput{
		Name: "target_title", Type: models.RequirementValueString,
	})

	if _, err := ValidateProvisionalRule(generated, baseline, requirement); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "target_title") {
		t.Fatalf("unused required input must fail before replay: %v", err)
	}

	steps := strings.TrimSpace(string(generated.Steps))
	generated.Steps = models.JSON(strings.TrimSuffix(steps, "]") +
		`,{"action":"sendLog","level":"info","message":"{{target_title}}"}]`)
	if _, err := ValidateProvisionalRule(generated, baseline, requirement); err != nil {
		t.Fatalf("operational required input reference should pass: %v", err)
	}
}

func TestWorkflowBaselineMayOmitResultButProvisionalRuleMayNot(t *testing.T) {
	baseline := workflowRule(t)
	baseline.Steps = models.JSON(`[{"action":"click","target":{"selector":"button.search"}}]`)
	validated := cloneWorkflowRule(t, baseline)
	if flags, err := ValidateWorkflowBaseline(validated, baseline, workflowRequirement()); err != nil || len(flags) != 3 {
		t.Fatalf("recording-derived baseline should not require sendResult: flags=%v err=%v", flags, err)
	}
	if _, err := ValidateProvisionalRule(validated, baseline, workflowRequirement()); !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "sendResult") {
		t.Fatalf("provisional rule without sendResult should be rejected, got %v", err)
	}
}

func TestWorkflowBaselineAcceptsRecordedBingCrossDomainActionSequence(t *testing.T) {
	baseline := &models.Rule{
		ID: "ext-bing", Version: "1.0.0", Name: "Recorded Bing search",
		Domain:    models.JSON(`["www.bing.com","science.nasa.gov"]`),
		Entry:     "https://www.bing.com/",
		Variables: models.JSON(`{"keyword":"why do eclipses happen"}`),
		Selectors: models.JSON(`{"search":{"selector":"textarea[name=q]"},"result":{"role":"link","roleName":"Why Do Eclipses Happen? - Science@NASA"}}`),
		Steps: models.JSON(`[
			{"action":"navigate","url":"https://www.bing.com/","waitUntil":"load"},
			{"action":"type","target":{"$ref":"search"},"value":"why do "},
			{"action":"type","target":{"$ref":"search"},"value":"eclipses happen"},
			{"action":"pressKey","keys":["Enter"]},
			{"action":"navigate","url":"https://www.bing.com/search?q={{keyword}}","waitUntil":"load"},
			{"action":"scrollBy","direction":"down","distance":180},
			{"action":"click","target":{"$ref":"result"}},
			{"action":"navigate","url":"https://science.nasa.gov/eclipses/","waitUntil":"load"},
			{"action":"scrollBy","direction":"down","distance":1,"unit":"pages"},
			{"action":"navigate","url":"https://www.bing.com/search?q={{keyword}}","waitUntil":"load"}
		]`),
	}
	requirement := models.CollectionRequirementSpec{
		Title: "Find selected Bing result", Description: "Find one recorded result.",
		RequiredInputs: []models.RequirementInput{
			{Name: "keyword", Type: models.RequirementValueString},
			{Name: "target_title", Type: models.RequirementValueString},
			{Name: "target_host", Type: models.RequirementValueString},
		},
		OutputFields: []models.RequirementOutputField{
			{Name: "title", Type: models.RequirementValueString},
			{Name: "website", Type: models.RequirementValueString},
		},
	}
	validated := cloneWorkflowRule(t, baseline)
	if _, err := ValidateWorkflowBaseline(validated, baseline, requirement); err != nil {
		t.Fatalf("safe recorded Bing action sequence was rejected: %v", err)
	}
}

func TestWorkflowBaselinePreservesRepeatedExtractionEmptyBehavior(t *testing.T) {
	baseline := workflowRule(t)
	validated := cloneWorkflowRule(t, baseline)
	if _, err := ValidateWorkflowBaseline(validated, baseline, workflowRequirement()); err != nil {
		t.Fatal(err)
	}
	extract := decodeWorkflowArray(t, validated.Steps)[2].(map[string]any)
	if _, exists := extract["onEmpty"]; exists {
		t.Fatalf("recording-derived baseline must retain legacy empty behavior: %#v", extract)
	}
}

func TestValidateProvisionalRuleRequiresExactConfirmedResultFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "missing field", payload: `{"name":"{{extracted.name}}"}`, want: `missing confirmed output field "price"`},
		{name: "undeclared field", payload: `{"name":"{{extracted.name}}","price":"{{extracted.price}}","secret":"no"}`, want: `undeclared output field "secret"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseline := workflowRule(t)
			generated := cloneWorkflowRule(t, baseline)
			generated.Steps = models.JSON(`[
				{"action":"extractText","name":"name","target":{"selector":".name"}},
				{"action":"sendResult","payload":` + test.payload + `}
			]`)
			if _, err := ValidateProvisionalRule(generated, baseline, workflowRequirement()); !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestValidateProvisionalRuleRejectsFlatReferencesToNamedObjectFields(t *testing.T) {
	baseline := workflowRule(t)
	requirement := workflowRequirement()
	requirement.RequiredInputs = nil
	invalid := cloneWorkflowRule(t, baseline)
	invalid.Steps = models.JSON(`[
		{"action":"extract","name":"items","target":{"selector":"main","visible":true},"multiple":false,"onEmpty":"fail","fields":{
			"name":{"selector":"h1","type":"text"},"price":{"selector":"p","type":"number"}
		}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}","price":"{{extracted.price}}"},"immediate":true}
	]`)
	if _, err := ValidateProvisionalRule(invalid, baseline, requirement); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), `no extraction or transform produces that exact path`) {
		t.Fatalf("expected unknown flat extraction reference failure, got %v", err)
	}

	valid := cloneWorkflowRule(t, baseline)
	valid.Steps = models.JSON(`[
		{"action":"extract","name":"items","target":{"selector":"main","visible":true},"multiple":false,"onEmpty":"fail","fields":{
			"name":{"selector":"h1","type":"text"},"price":{"selector":"p","type":"number"}
		}},
		{"action":"sendResult","payload":{"name":"{{extracted.items.name}}","price":"{{extracted.items.price}}"},"immediate":true}
	]`)
	if _, err := ValidateProvisionalRule(valid, baseline, requirement); err != nil {
		t.Fatalf("valid named object field references rejected: %v", err)
	}
}

func TestValidateProvisionalRuleRequiresForEachForRepeatedScalarRows(t *testing.T) {
	baseline := workflowRule(t)
	requirement := workflowRequirement()
	requirement.RequiredInputs = nil
	for _, test := range []struct {
		name       string
		payload    string
		loopResult bool
	}{
		{name: "whole repeated extraction", payload: `{"name":"{{extracted.items}}","price":"{{loopItem.price}}"}`},
		{name: "property projected across repeated extraction", payload: `{"name":"{{extracted.items.name}}","price":"{{loopItem.price}}"}`},
		{name: "current loop item", payload: `{"name":"{{loopItem.name}}","price":"{{loopItem.price}}"}`, loopResult: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			generated := cloneWorkflowRule(t, baseline)
			resultStep := `{"action":"sendResult","payload":` + test.payload + `,"immediate":true}`
			if test.loopResult {
				resultStep = `{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[` + resultStep + `]}`
			}
			generated.Output = models.JSON(`{"type":"object","required":["name","price"]}`)
			generated.Steps = models.JSON(`[
				{"action":"extract","name":"items","target":{"selector":".product"},"multiple":true,
				 "fields":{"name":{"selector":".name","type":"text"},"price":{"selector":".price","type":"number"}}},
				` + resultStep + `
			]`)
			_, err := ValidateProvisionalRule(generated, baseline, requirement)
			if !test.loopResult {
				if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "iterate it with loop.forEach") {
					t.Fatalf("expected repeated scalar projection failure, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid repeated-row loop rejected: %v", err)
			}
			if len(generated.Output) != 0 {
				t.Fatalf("per-row transport schema leaked into engine output: %s", generated.Output)
			}
		})
	}
}

func TestValidateProvisionalRuleRequiresCompatibleRuntimeOutputTypes(t *testing.T) {
	requirement := models.CollectionRequirementSpec{
		Title:       "Collect count",
		Description: "Collect the displayed result count.",
		OutputFields: []models.RequirementOutputField{{
			Name: "count", Type: models.RequirementValueNumber,
			Description: "Displayed result count.",
		}},
		SampleOutput: map[string]any{"count": float64(2)},
	}
	baseline := workflowRule(t)

	invalid := cloneWorkflowRule(t, baseline)
	invalid.Steps = models.JSON(`[
		{"action":"extractText","name":"count","target":{"selector":"#result-count","visible":true}},
		{"action":"sendResult","payload":{"count":"{{extracted.count}}"},"immediate":true}
	]`)
	if _, err := ValidateProvisionalRule(invalid, baseline, requirement); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "confirmed as number") || !strings.Contains(err.Error(), "produced as string") {
		t.Fatalf("expected static runtime type failure, got %v", err)
	}

	valid := cloneWorkflowRule(t, baseline)
	valid.Steps = models.JSON(`[
		{"action":"extractText","name":"raw_count","target":{"selector":"#result-count","visible":true}},
		{"action":"transform","from":"extracted.raw_count","name":"count","operations":[
			{"type":"regex","params":{"pattern":"[0-9]+","group":0}},
			{"type":"number"}
		]},
		{"action":"sendResult","payload":{"count":"{{extracted.count}}"},"immediate":true}
	]`)
	if _, err := ValidateProvisionalRule(valid, baseline, requirement); err != nil {
		t.Fatalf("valid numeric transform rejected: %v", err)
	}
}

func TestValidateProvisionalRuleInfersStructuralExtractFieldsAsObjects(t *testing.T) {
	requirement := models.CollectionRequirementSpec{
		Title: "Collect details", Description: "Collect the structured details object.",
		OutputFields: []models.RequirementOutputField{{
			Name: "details", Type: models.RequirementValueObject,
			Description: "Structured details.",
		}},
		SampleOutput: map[string]any{"details": map[string]any{"name": "Example"}},
	}
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	generated.Steps = models.JSON(`[
		{"action":"extract","name":"record","target":{"selector":".result"},"fields":{
			"details":{"type":"text","selector":".details","fields":{
				"name":{"type":"text","selector":".name"}
			}}
		}},
		{"action":"sendResult","payload":{"details":"{{extracted.record.details}}"},"immediate":true}
	]`)
	if _, err := ValidateProvisionalRule(generated, baseline, requirement); err != nil {
		t.Fatalf("runtime structural object was inferred from its ignored scalar marker type: %v", err)
	}

	stringRequirement := requirement
	stringRequirement.OutputFields = []models.RequirementOutputField{{
		Name: "details", Type: models.RequirementValueString,
		Description: "Incorrect scalar contract.",
	}}
	stringRequirement.SampleOutput = map[string]any{"details": "Example"}
	stringGenerated := cloneWorkflowRule(t, baseline)
	stringGenerated.Steps = append(models.JSON(nil), generated.Steps...)
	if _, err := ValidateProvisionalRule(stringGenerated, baseline, stringRequirement); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "produced as object") {
		t.Fatalf("structural object was incorrectly inferred as its scalar marker type: %v", err)
	}
}

func TestValidateProvisionalRuleRequiresArrayCollectionReference(t *testing.T) {
	requirement := models.CollectionRequirementSpec{
		Title: "Collect variants", Description: "Collect every available variant.",
		OutputFields: []models.RequirementOutputField{{
			Name: "variants", Type: models.RequirementValueArray,
			Description: "Every observed product variant.",
		}},
		SampleOutput: map[string]any{"variants": []any{map[string]any{"name": "Example"}}},
	}
	baseline := workflowRule(t)

	for _, test := range []struct {
		name    string
		payload string
		wantErr bool
		want    string
	}{
		{name: "direct extracted collection", payload: `{"variants":"{{extracted.variants}}"}`},
		{name: "unproduced nested extracted collection", payload: `{"variants":"  {{extracted.groups.variants}}  "}`, wantErr: true, want: "no extraction or transform produces"},
		{name: "literal one item array", payload: `{"variants":[{"name":"{{extracted.name}}"}]}`, wantErr: true},
		{name: "scalar wrapper", payload: `{"variants":"prefix {{extracted.variants}}"}`, wantErr: true},
		{name: "variable reference", payload: `{"variants":"{{variables.variants}}"}`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			generated := cloneWorkflowRule(t, baseline)
			generated.Steps = models.JSON(`[
				{"action":"extract","name":"variants","target":{"selector":".variant"},"multiple":true,
				 "fields":{"name":{"selector":".name","type":"text"}}},
				{"action":"sendResult","payload":` + test.payload + `}
			]`)
			_, err := ValidateProvisionalRule(generated, baseline, requirement)
			if test.wantErr {
				want := test.want
				if want == "" {
					want = "directly reference an extracted collection"
				}
				if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), want) {
					t.Fatalf("expected array collection contract failure, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid extracted collection reference rejected: %v", err)
			}
		})
	}
}

func TestValidateProvisionalRuleRejectsUnsafeOrUnreplayableDSL(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*models.Rule)
		target error
	}{
		{name: "identity", mutate: func(rule *models.Rule) { rule.Entry = "https://example.com/other" }, target: ErrInvalidProvisionalRule},
		{name: "version", mutate: func(rule *models.Rule) { rule.Version = "2" }, target: ErrInvalidProvisionalRule},
		{name: "evaluate", mutate: func(rule *models.Rule) {
			rule.Steps = models.JSON(`[{"action":"evaluate","code":"return document.cookie"}]`)
		}, target: ErrUnsafeProvisionalRule},
		{name: "captcha", mutate: func(rule *models.Rule) { rule.Steps = models.JSON(`[{"action":"solveCaptcha"}]`) }, target: ErrUnsafeProvisionalRule},
		{name: "credentials", mutate: func(rule *models.Rule) {
			rule.Steps = models.JSON(`[{"action":"setCookie","name":"session","value":"secret"}]`)
		}, target: ErrUnsafeProvisionalRule},
		{name: "unknown action", mutate: func(rule *models.Rule) { rule.Steps = models.JSON(`[{"action":"invented"}]`) }, target: ErrInvalidProvisionalRule},
		{name: "javascript condition", mutate: func(rule *models.Rule) {
			rule.Steps = models.JSON(`[{"action":"click","condition":{"type":"jsTruthy","expression":"true"}}]`)
		}, target: ErrUnsafeProvisionalRule},
		{name: "cross domain", mutate: func(rule *models.Rule) {
			rule.Steps = models.JSON(`[{"action":"navigate","url":"https://evil.example/path"}]`)
		}, target: ErrUnsafeProvisionalRule},
		{name: "non-http navigation", mutate: func(rule *models.Rule) {
			rule.Steps = models.JSON(`[{"action":"navigate","url":"file:///tmp/private"}]`)
		}, target: ErrUnsafeProvisionalRule},
		{name: "hook navigation", mutate: func(rule *models.Rule) {
			rule.Hooks = models.JSON(`{"beforeAll":[{"action":"navigate","url":"/login"}]}`)
		}, target: ErrInvalidProvisionalRule},
		{name: "while element exists navigation", mutate: func(rule *models.Rule) {
			rule.Steps = models.JSON(`[
				{"action":"loop","type":"whileElementExists","target":{"selector":"a.next"},"steps":[
					{"action":"if","condition":{"type":"elementExists","target":{"selector":"body"}},"then":[
						{"action":"click","target":{"selector":"a.next"}}
					]}
				]}
			]`)
		}, target: ErrInvalidProvisionalRule},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseline := workflowRule(t)
			generated := cloneWorkflowRule(t, baseline)
			test.mutate(generated)
			if _, err := ValidateProvisionalRule(generated, baseline, workflowRequirement()); !errors.Is(err, test.target) {
				t.Fatalf("expected %v, got %v", test.target, err)
			}
		})
	}
}

func TestValidateProvisionalRuleRejectsDuplicateAppendedTaskInput(t *testing.T) {
	baseline := workflowRule(t)
	requirement := workflowRequirement()
	validTail := `
		{"action":"extract","name":"items","target":{"selector":".product","visible":true},"multiple":true,
		 "fields":{"name":{"selector":".name","type":"text"},"price":{"selector":".price","type":"number"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[
		 {"action":"sendResult","payload":{"name":"{{loopItem.name}}","price":"{{loopItem.price}}"},"immediate":true}
		]}`
	for _, test := range []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{
			name: "same complete binding appended to same target",
			prefix: `
				{"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}","append":false},
				{"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}","append":true,"submit":true},`,
			wantErr: true,
		},
		{
			name: "clear resets binding",
			prefix: `
				{"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}","append":false},
				{"action":"clear","target":{"selector":"input[name=q]"}},
				{"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}","append":true,"submit":true},`,
		},
		{
			name: "different target remains independent",
			prefix: `
				{"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}","append":false},
				{"action":"type","target":{"selector":"input[name=confirm]"},"value":"{{keyword}}","append":true},`,
		},
		{
			name: "loop iteration state is independent",
			prefix: `
				{"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}","append":false},
				{"action":"loop","type":"fixedCount","count":2,"steps":[
				 {"action":"type","target":{"selector":"input[name=q]"},"value":"{{keyword}}","append":true}
				]},`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			generated := cloneWorkflowRule(t, baseline)
			generated.Steps = models.JSON(`[` + test.prefix + validTail + `]`)
			_, err := ValidateProvisionalRule(generated, baseline, requirement)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "appends complete task input") {
					t.Fatalf("expected duplicate task-input failure, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid task-input sequence rejected: %v", err)
			}
		})
	}
}

func TestValidateReplayOutputChecksEveryDeclaredFieldAndRow(t *testing.T) {
	requirement := workflowRequirement()
	validRows := []any{
		map[string]any{"name": "A", "price": 10.5},
		map[string]any{"name": "B", "price": 20},
	}
	if err := ValidateReplayOutput(validRows, requirement); err != nil {
		t.Fatal(err)
	}
	invalid := []any{
		[]any{},
		map[string]any{"name": "A"},
		map[string]any{"name": "A", "price": "10"},
		map[string]any{"name": "A", "price": 10, "secret": "unexpected"},
		"not an object",
	}
	for _, value := range invalid {
		if err := ValidateReplayOutput(value, requirement); !errors.Is(err, ErrInvalidReplayOutput) {
			t.Fatalf("expected invalid replay output for %#v, got %v", value, err)
		}
	}
}

func TestValidateReplayOutputRejectsAbove5000Rows(t *testing.T) {
	rows := make([]any, 5001)
	for i := range rows {
		rows[i] = map[string]any{"name": "x", "price": 1.0}
	}
	requirement := workflowRequirement()
	if err := ValidateReplayOutput(rows, requirement); !errors.Is(err, ErrInvalidReplayOutput) {
		t.Fatalf("expected ErrInvalidReplayOutput for 5001 rows, got %v", err)
	}
}

func TestBuildRequirementOutputSchemaRejectsInvalidFields(t *testing.T) {
	requirement := workflowRequirement()
	requirement.OutputFields = append(requirement.OutputFields, requirement.OutputFields[0])
	if _, err := BuildRequirementOutputSchema(requirement); !errors.Is(err, ErrInvalidProvisionalRule) {
		t.Fatalf("expected duplicate field rejection, got %v", err)
	}
	requirement.OutputFields = nil
	if _, err := BuildRequirementOutputSchema(requirement); !errors.Is(err, ErrInvalidProvisionalRule) {
		t.Fatalf("expected empty schema rejection, got %v", err)
	}
}

func TestValidateProvisionalRuleAllowsWaitForElementVisible(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	requirement := workflowRequirement()
	requirement.RequiredInputs = nil
	generated.Steps = models.JSON(`[
		{"action":"click","target":{"selector":"button.search"}},
		{"action":"waitForElementVisible","target":{"selector":".price"}},
		{"action":"extract","name":"items","target":{"selector":".product"},"multiple":true,
		 "fields":{"name":{"selector":".name","type":"text"},"price":{"selector":".price","type":"number"}}},
		{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[
		 {"action":"sendResult","payload":{"name":"{{loopItem.name}}","price":"{{loopItem.price}}"},"immediate":true}
		]}
	]`)
	if _, err := ValidateProvisionalRule(generated, baseline, requirement); err != nil {
		t.Fatalf("waitForElementVisible step must validate: %v", err)
	}
}

func TestValidateProvisionalRuleEnforcesVisibleExtractionByDefault(t *testing.T) {
	baseline := workflowRule(t)
	requirement := workflowRequirement()

	missingVisible := cloneWorkflowRule(t, baseline)
	flags, err := ValidateProvisionalRule(missingVisible, baseline, requirement)
	if err != nil {
		t.Fatalf("missing visible default was not normalized: %v", err)
	}
	if !slices.Contains(flags, visibleExtractionNormalizedFlag) {
		t.Fatalf("visible normalization was not audited: %v", flags)
	}
	missingSteps := decodeWorkflowArray(t, missingVisible.Steps)
	if missingSteps[2].(map[string]any)["target"].(map[string]any)["visible"] != true {
		t.Fatalf("missing extraction visibility was not defaulted: %s", missingVisible.Steps)
	}

	explicitFalse := cloneWorkflowRule(t, baseline)
	falseSteps := decodeWorkflowArray(t, explicitFalse.Steps)
	falseSteps[2].(map[string]any)["target"].(map[string]any)["visible"] = false
	explicitFalse.Steps, _ = json.Marshal(falseSteps)
	if _, err := ValidateProvisionalRule(explicitFalse, baseline, requirement); err != nil {
		t.Fatalf("explicit false extraction visibility was not overridden: %v", err)
	}
	normalizedFalseSteps := decodeWorkflowArray(t, explicitFalse.Steps)
	if normalizedFalseSteps[2].(map[string]any)["target"].(map[string]any)["visible"] != true {
		t.Fatalf("explicit false extraction visibility was not enforced: %s", explicitFalse.Steps)
	}

	hidden := cloneWorkflowRule(t, baseline)
	hiddenSteps := decodeWorkflowArray(t, hidden.Steps)
	hiddenSteps[2].(map[string]any)["target"].(map[string]any)["selector"] = ".product-selection-area.noscript .item"
	hidden.Steps, _ = json.Marshal(hiddenSteps)
	if _, err := ValidateProvisionalRule(hidden, baseline, requirement); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "hidden or noscript fallback") {
		t.Fatalf("visible requirement accepted hidden fallback extraction: %v", err)
	}
}

func TestValidateProvisionalRuleResolvesVisibleExtractionSelectorRefs(t *testing.T) {
	baseline := workflowRule(t)
	requirement := workflowRequirement()
	requirement.Description = "Collect visible products."
	generated := cloneWorkflowRule(t, baseline)
	steps := decodeWorkflowArray(t, generated.Steps)
	steps[2].(map[string]any)["target"] = map[string]any{"$ref": "products"}
	generated.Steps, _ = json.Marshal(steps)
	generated.Selectors = models.JSON(`{"products":{"selector":".product","visible":true}}`)
	if _, err := ValidateProvisionalRule(generated, baseline, requirement); err != nil {
		t.Fatalf("visible selector alias was rejected: %v", err)
	}

	generated.Selectors = models.JSON(`{"products":{"selector":".product-selection-area.noscript .item","visible":true}}`)
	if _, err := ValidateProvisionalRule(generated, baseline, requirement); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "hidden or noscript fallback") {
		t.Fatalf("hidden selector alias was accepted: %v", err)
	}
}

func TestValidateProvisionalRuleRejectsReplayUnsupportedWait(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	generated.Steps = models.JSON(`[
		{"action":"waitForNetworkIdle"},
		{"action":"extractText","name":"name","target":{"selector":".name"}},
		{"action":"extractText","name":"price","target":{"selector":".price"}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}","price":"{{extracted.price}}"},"immediate":true}
	]`)
	_, err := ValidateProvisionalRule(generated, baseline, workflowRequirement())
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "cannot prove that already-started requests have finished") {
		t.Fatalf("expected precise replay-unsupported validation error, got %v", err)
	}
}

func TestSetTagWorkflowPolicy(t *testing.T) {
	if !allowedWorkflowActions["setTag"] {
		t.Fatal("setTag must remain a recognized legacy engine action")
	}
	reason, unsupported := replayUnsupportedWorkflowActions["setTag"]
	if !unsupported || !strings.Contains(reason, "legacy log-only metadata") ||
		!strings.Contains(reason, "tag scope") || !strings.Contains(reason, "resume semantics") {
		t.Fatalf("setTag must be rejected with a precise replay policy reason: %q", reason)
	}
}

func TestValidateProvisionalRuleRejectsSetTag(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	generated.Steps = models.JSON(`[
		{"action":"setTag","tags":{"source":"legacy"},"scope":"task"},
		{"action":"extractText","name":"name","target":{"selector":".name"}},
		{"action":"extractText","name":"price","target":{"selector":".price"}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}","price":"{{extracted.price}}"},"immediate":true}
	]`)
	_, err := ValidateProvisionalRule(generated, baseline, workflowRequirement())
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), `action "setTag"`) ||
		!strings.Contains(err.Error(), "legacy log-only metadata") || !strings.Contains(err.Error(), "resume semantics") {
		t.Fatalf("expected precise replay-unsupported setTag error, got %v", err)
	}
}

func TestHistoryNavigationWorkflowPolicy(t *testing.T) {
	for _, action := range []string{"goBack", "goForward"} {
		t.Run(action, func(t *testing.T) {
			if !allowedWorkflowActions[action] {
				t.Fatalf("%s must remain a recognized engine action", action)
			}
			reason, unsupported := replayUnsupportedWorkflowActions[action]
			if !unsupported || !strings.Contains(reason, "cannot validate the history destination") {
				t.Fatalf("%s must be rejected with a precise replay policy reason: %q", action, reason)
			}
			if !actionMayNavigateMap(map[string]any{"action": action}) {
				t.Fatalf("%s must remain classified as navigation", action)
			}
		})
	}
}

func TestValidateProvisionalRuleRejectsHistoryNavigation(t *testing.T) {
	for _, action := range []string{"goBack", "goForward"} {
		t.Run(action, func(t *testing.T) {
			baseline := workflowRule(t)
			generated := cloneWorkflowRule(t, baseline)
			generated.Steps = models.JSON(`[
				{"action":"` + action + `"},
				{"action":"extractText","name":"name","target":{"selector":".name"}},
				{"action":"extractText","name":"price","target":{"selector":".price"}},
				{"action":"sendResult","payload":{"name":"{{extracted.name}}","price":"{{extracted.price}}"},"immediate":true}
			]`)
			_, err := ValidateProvisionalRule(generated, baseline, workflowRequirement())
			if !errors.Is(err, ErrInvalidProvisionalRule) ||
				!strings.Contains(err.Error(), action) ||
				!strings.Contains(err.Error(), "cannot validate the history destination") {
				t.Fatalf("expected precise replay-unsupported %s error, got %v", action, err)
			}
		})
	}
}

func TestValidateProvisionalRuleRejectsMissingActionSchemaField(t *testing.T) {
	baseline := workflowRule(t)
	generated := cloneWorkflowRule(t, baseline)
	generated.Steps = models.JSON(`[
		{"action":"waitForTimeout"},
		{"action":"extractText","name":"name","target":{"selector":".name"}},
		{"action":"extractText","name":"price","target":{"selector":".price"}},
		{"action":"sendResult","payload":{"name":"{{extracted.name}}","price":"{{extracted.price}}"},"immediate":true}
	]`)
	_, err := ValidateProvisionalRule(generated, baseline, workflowRequirement())
	if !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), `action "waitForTimeout"`) ||
		!strings.Contains(err.Error(), `"ms"`) {
		t.Fatalf("expected actionable retryable action-schema error, got %v", err)
	}
}

func TestValidateProvisionalRuleRejectsMalformedExtractFields(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{
			name:   "qwen action target field descriptor",
			fields: `{"title":{"action":"extractText","target":{"selector":".title","visible":true}}}`,
		},
		{name: "empty fields", fields: `{}`},
		{name: "nested field missing type", fields: `{"item":{"type":"text","fields":{"nested":{"selector":".nested"}}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseline := workflowRule(t)
			generated := cloneWorkflowRule(t, baseline)
			generated.Steps = models.JSON(`[
				{"action":"extract","name":"items","target":{"selector":".result"},"multiple":true,"fields":` + test.fields + `},
				{"action":"loop","type":"forEach","items":"{{extracted.items}}","as":"item","steps":[
					{"action":"sendResult","payload":{"name":"{{loopItem.title}}","price":1},"immediate":true}
				]}
			]`)
			_, err := ValidateProvisionalRule(generated, baseline, workflowRequirement())
			if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), `action "extract"`) {
				t.Fatalf("expected retryable extract field schema error, got %v", err)
			}
		})
	}
}
