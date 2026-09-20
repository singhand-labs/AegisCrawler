package dsl

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const strictCatalogFixture = `{
	"version":"selector-catalog-v3",
	"catalogHash":"strict-catalog-hash",
	"candidates":[{
		"rowCandidateId":"r_exact",
		"fieldCandidates":[{
			"fieldCandidateId":"f_name",
			"supportedTypes":["text"],
			"nonEmptyText":true
		},{
			"fieldCandidateId":"f_parent",
			"supportedTypes":["text"],
			"nonEmptyText":true
		},{
			"fieldCandidateId":"f_nested",
			"parentFieldCandidateId":"f_parent",
			"supportedTypes":["text"],
			"nonEmptyText":true
		}]
	},{
		"rowCandidateId":"r_other",
		"fieldCandidates":[{
			"fieldCandidateId":"f_other",
			"supportedTypes":["text"],
			"nonEmptyText":true
		}]
	}]
}`

func TestStrictSemanticFieldsForOutputNarrowsConfirmedMDNFields(t *testing.T) {
	fields := map[string][]string{
		"text": {"f_h1", "f_p", "f_h2", "f_footer"},
	}
	selectors := map[string]string{
		"f_h1": "h1", "f_p": "p", "f_h2": "h2", "f_footer": ".article-footer",
	}
	order := map[string]int{"f_h1": 0, "f_p": 1, "f_h2": 2, "f_footer": 3}
	tests := []struct {
		name, description, want string
	}{
		{"title", "Visible guide heading", "f_h1"},
		{"introduction", "First visible introductory paragraph", "f_p"},
		{"first_section", "First visible level-two article section heading", "f_h2"},
	}
	for _, test := range tests {
		filtered := strictSemanticFieldsForOutput(fields, selectors, order, test.name, test.description)
		if got := filtered["text"]; len(got) != 1 || got[0] != test.want {
			t.Fatalf("%s semantic candidates = %v, want %s", test.name, got, test.want)
		}
	}
	firstSection := strictSemanticFieldsForOutput(
		map[string][]string{"text": {"f_reference", "f_first"}},
		map[string]string{"f_reference": "h2#reference", "f_first": "h2#grid_layout_in_action"},
		map[string]int{"f_reference": 9, "f_first": 2},
		"first_section",
		"First visible level-two article section heading",
	)
	if got := firstSection["text"]; len(got) != 1 || got[0] != "f_first" {
		t.Fatalf("first section candidates = %v, want first evidence-ordered h2", got)
	}
}

func TestStrictOutputSemanticTagUsesRequirementMeaningNotAmbiguousNames(t *testing.T) {
	tests := []struct {
		name, description, want string
	}{
		{"title", "Full title attribute of the visible book link", "a"},
		{"hero", "Visible product image", "img"},
		{"summary", "First visible introductory paragraph", "p"},
		{"section", "First visible level-two heading", "h2"},
		{"title", "Visible guide heading", "h1"},
		{"website", "Visible result citation host", "cite"},
		{"title", "Visible product title", ""},
	}
	for _, test := range tests {
		if got := strictOutputSemanticTag(test.name, test.description); got != test.want {
			t.Fatalf("%s / %s semantic tag = %q, want %q", test.name, test.description, got, test.want)
		}
	}
}

func TestStrictSemanticFieldsFailClosedWhenExplicitEvidenceIsMissing(t *testing.T) {
	filtered := strictSemanticFieldsForOutput(
		map[string][]string{"text": {"f_header"}},
		map[string]string{"f_header": ".layout__header"},
		map[string]int{"f_header": 0},
		"introduction",
		"First visible introductory paragraph",
	)
	if len(filtered) != 0 {
		t.Fatalf("explicit paragraph requirement fell back to unrelated evidence: %v", filtered)
	}
}

func TestStrictSemanticFieldsRecognizeRelationalParagraphSubject(t *testing.T) {
	filtered := strictSemanticFieldsForOutput(
		map[string][]string{"text": {"f_intro", "f_header"}},
		map[string]string{
			"f_intro":  ".layout__header > .content-section > p:not(p + p)",
			"f_header": ".layout__header",
		},
		map[string]int{"f_intro": 0, "f_header": 1},
		"introduction",
		"First visible introductory paragraph",
	)
	if got := filtered["text"]; len(got) != 1 || got[0] != "f_intro" {
		t.Fatalf("relational paragraph candidates = %v, want f_intro", got)
	}
	if strictSelectorMatchesSemanticTag("p + span:not(p)", "p") {
		t.Fatal("paragraph inside a pseudo-class changed the selector subject tag")
	}
}

func TestWorkflowStrictOutputSchemaRejectsObservedDeepSeekShapeFailures(t *testing.T) {
	baseline := dslWorkflowBaseline()
	output, err := workflowStrictOutput(dslWorkflowRequirement(), baseline, strictCatalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)
	valid := map[string]any{
		"selectorCatalogHash": "strict-catalog-hash",
		"rule": map[string]any{
			"id": baseline.ID, "version": baseline.Version, "name": baseline.Name,
			"domain": "example.com", "entry": baseline.Entry,
			"steps": []any{
				map[string]any{
					"action": "extract", "name": "items",
					"target":   map[string]any{"rowCandidateId": "r_exact", "visible": true},
					"multiple": true, "onEmpty": "fail",
					"fields": map[string]any{
						"name": map[string]any{"type": "text", "fieldCandidateId": "f_name"},
					},
				},
				map[string]any{
					"action": "filter", "from": "extracted.items", "name": "selected",
					"criteria": map[string]any{"field": "name", "op": "eq", "value": "{{keyword}}"},
				},
				map[string]any{
					"action": "loop", "type": "forEach",
					"items": "{{extracted.selected}}", "as": "item",
					"steps": []any{map[string]any{
						"action": "sendResult", "immediate": true,
						"payload": map[string]any{"name": "{{loopItem.name}}"},
					}},
				},
			},
		},
	}
	if err := resolved.Validate(valid); err != nil {
		t.Fatalf("valid strict workflow rejected: %v", err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"missing extract name": func(value map[string]any) {
			delete(strictTestSteps(value)[0].(map[string]any), "name")
		},
		"fields array": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["fields"] = []any{
				map[string]any{"name": "name", "type": "text", "fieldCandidateId": "f_name"},
			}
		},
		"unknown candidate": func(value map[string]any) {
			field := strictTestSteps(value)[0].(map[string]any)["fields"].(map[string]any)["name"].(map[string]any)
			field["fieldCandidateId"] = "f_unknown"
		},
		"cross-row candidate": func(value map[string]any) {
			field := strictTestSteps(value)[0].(map[string]any)["fields"].(map[string]any)["name"].(map[string]any)
			field["fieldCandidateId"] = "f_other"
		},
		"nested candidate used directly": func(value map[string]any) {
			field := strictTestSteps(value)[0].(map[string]any)["fields"].(map[string]any)["name"].(map[string]any)
			field["fieldCandidateId"] = "f_nested"
		},
		"candidate unsupported field type": func(value map[string]any) {
			field := strictTestSteps(value)[0].(map[string]any)["fields"].(map[string]any)["name"].(map[string]any)
			field["type"] = "number"
		},
		"row kind changed to single": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["multiple"] = false
		},
		"wrong payload key": func(value map[string]any) {
			send := strictTestSteps(value)[2].(map[string]any)["steps"].([]any)[0].(map[string]any)
			send["payload"] = map[string]any{"wrong": "{{loopItem.name}}"}
		},
		"changed entry": func(value map[string]any) {
			value["rule"].(map[string]any)["entry"] = "https://example.com/other"
		},
		"string step condition": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["condition"] = "{{loopIndex}} == 0"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := strictCloneMap(t, valid)
			mutate(candidate)
			if err := resolved.Validate(candidate); err == nil {
				t.Fatalf("invalid strict workflow %q was accepted: %#v", name, candidate)
			}
		})
	}
}

func TestWorkflowStrictOutputSchemaAcceptsIndependentTypeFlags(t *testing.T) {
	baseline := dslWorkflowBaseline()
	output, err := workflowStrictOutput(dslWorkflowRequirement(), baseline, strictCatalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)

	for name, flags := range map[string]map[string]any{
		"neither":     {},
		"append only": {"append": true},
		"submit only": {"submit": true},
		"both":        {"append": false, "submit": true},
	} {
		t.Run(name, func(t *testing.T) {
			step := map[string]any{
				"action": "type",
				"target": map[string]any{
					"family": "selector", "value": "#q", "name": "",
				},
				"value": "{{keyword}}",
			}
			for key, value := range flags {
				step[key] = value
			}
			candidate := map[string]any{
				"selectorCatalogHash": "strict-catalog-hash",
				"rule": map[string]any{
					"id": baseline.ID, "version": baseline.Version, "name": baseline.Name,
					"domain": "example.com", "entry": baseline.Entry,
					"steps": []any{step},
				},
			}
			if err := resolved.Validate(candidate); err != nil {
				t.Fatalf("valid %s type flags rejected: %v", name, err)
			}
		})
	}
}

func TestWorkflowStrictOutputSchemaExcludesScopesWithoutEnoughDirectFields(t *testing.T) {
	requirement := dslWorkflowRequirement()
	requirement.OutputFields = []models.RequirementOutputField{
		{Name: "title", Type: models.RequirementValueString},
		{Name: "introduction", Type: models.RequirementValueString},
		{Name: "first_section", Type: models.RequirementValueString},
	}
	baseline := dslWorkflowBaseline()
	catalog := `{
		"version":"selector-catalog-v3",
		"catalogHash":"three-field-catalog",
		"candidates":[{
			"targetCandidateId":"t_insufficient",
			"fieldCandidates":[{
				"fieldCandidateId":"f_only",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		},{
			"targetCandidateId":"t_article",
			"fieldCandidates":[{
				"fieldCandidateId":"f_title",
				"supportedTypes":["text"],
				"nonEmptyText":true
			},{
				"fieldCandidateId":"f_intro",
				"supportedTypes":["text"],
				"nonEmptyText":true
			},{
				"fieldCandidateId":"f_section",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		}]
	}`
	output, err := workflowStrictOutput(requirement, baseline, catalog)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)
	valid := map[string]any{
		"selectorCatalogHash": "three-field-catalog",
		"rule": map[string]any{
			"id": baseline.ID, "version": baseline.Version, "name": baseline.Name,
			"domain": "example.com", "entry": baseline.Entry,
			"steps": []any{map[string]any{
				"action": "extract", "name": "items",
				"target": map[string]any{
					"targetCandidateId": "t_article", "visible": true,
				},
				"multiple": false, "onEmpty": "fail",
				"fields": map[string]any{
					"title": map[string]any{
						"type": "text", "fieldCandidateId": "f_title",
					},
					"introduction": map[string]any{
						"type": "text", "fieldCandidateId": "f_intro",
					},
					"first_section": map[string]any{
						"type": "text", "fieldCandidateId": "f_section",
					},
				},
			}},
		},
	}
	if err := resolved.Validate(valid); err != nil {
		t.Fatalf("eligible three-field scope was rejected: %v", err)
	}
	invalid := strictCloneMap(t, valid)
	extract := strictTestSteps(invalid)[0].(map[string]any)
	extract["target"] = map[string]any{
		"targetCandidateId": "t_insufficient", "visible": true,
	}
	for _, rawField := range extract["fields"].(map[string]any) {
		rawField.(map[string]any)["fieldCandidateId"] = "f_only"
	}
	if err := resolved.Validate(invalid); err == nil {
		t.Fatal("strict schema exposed an extraction scope with too few direct fields")
	}
}

func TestWorkflowStrictOutputSchemaExcludesUnsafeDirectTextTargets(t *testing.T) {
	requirement := dslWorkflowRequirement()
	baseline := dslWorkflowBaseline()
	catalog := `{
		"version":"selector-catalog-v4",
		"catalogHash":"direct-text-catalog",
		"candidates":[{
			"targetCandidateId":"t_container",
			"fieldCandidates":[{
				"fieldCandidateId":"f_container",
				"supportedTypes":["attr","boolean","exists"],
				"nonEmptyText":true
			},{
				"fieldCandidateId":"f_heading",
				"observedRelativeSelector":"h1",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		},{
			"targetCandidateId":"t_heading",
			"fieldCandidates":[{
				"fieldCandidateId":"f_heading_self",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		}]
	}`
	output, err := workflowStrictOutput(requirement, baseline, catalog)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)
	candidate := map[string]any{
		"selectorCatalogHash": "direct-text-catalog",
		"rule": map[string]any{
			"id": baseline.ID, "version": baseline.Version, "name": baseline.Name,
			"domain": "example.com", "entry": baseline.Entry,
			"steps": []any{map[string]any{
				"action": "extractText", "name": "name",
				"target": map[string]any{
					"targetCandidateId": "t_heading", "visible": true,
				},
			}},
		},
	}
	if err := resolved.Validate(candidate); err != nil {
		t.Fatalf("direct safe-text target was rejected: %v", err)
	}
	unsafe := strictCloneMap(t, candidate)
	strictTestSteps(unsafe)[0].(map[string]any)["target"] = map[string]any{
		"targetCandidateId": "t_container", "visible": true,
	}
	if err := resolved.Validate(unsafe); err == nil {
		t.Fatal("container without direct safe text was exposed to extractText")
	}
}

func TestWorkflowStrictOutputSchemaDescribesConfirmedFieldChoices(t *testing.T) {
	requirement := dslWorkflowRequirement()
	requirement.OutputFields[0].Description = "Visible guide heading"
	output, err := workflowStrictOutput(requirement, dslWorkflowBaseline(), strictCatalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output.Schema), `"description":"Visible guide heading"`) {
		t.Fatalf("strict schema omitted confirmed field semantics: %s", output.Schema)
	}
}

func TestWorkflowStrictOutputSchemaRejectsCapturedMixedOrdinaryTargets(t *testing.T) {
	requirement := dslWorkflowRequirement()
	requirement.RequiredInputs = []models.RequirementInput{{
		Name: "category", Type: models.RequirementValueString,
	}}
	baseline := dslWorkflowBaseline()
	output, err := workflowStrictOutput(requirement, baseline, strictCatalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)
	valid := map[string]any{
		"selectorCatalogHash": "strict-catalog-hash",
		"rule": map[string]any{
			"id": baseline.ID, "version": baseline.Version, "name": baseline.Name,
			"domain": "example.com", "entry": baseline.Entry,
			"steps": []any{
				map[string]any{
					"action": "click",
					"target": map[string]any{
						"family": "textVisible", "value": "{{category}}", "name": "",
					},
				},
				map[string]any{
					"action": "loop", "type": "fixedCount", "count": 10,
					"steps": []any{
						map[string]any{
							"action": "extract", "name": "items",
							"target":   map[string]any{"rowCandidateId": "r_exact", "visible": true},
							"multiple": true, "onEmpty": "fail",
							"fields": map[string]any{
								"name": map[string]any{"type": "text", "fieldCandidateId": "f_name"},
							},
						},
						map[string]any{
							"action": "loop", "type": "forEach",
							"items": "{{extracted.items}}", "as": "item",
							"steps": []any{map[string]any{
								"action": "sendResult", "immediate": true,
								"payload": map[string]any{"name": "{{loopItem.name}}"},
							}},
						},
						map[string]any{
							"action": "if",
							"condition": map[string]any{
								"type": "elementNotExists",
								"target": map[string]any{
									"family": "selector", "value": "li.next > a", "name": "",
								},
							},
							"then": []any{map[string]any{"action": "break"}},
							"else": []any{map[string]any{
								"action": "click",
								"target": map[string]any{
									"family": "selector", "value": "li.next > a", "name": "",
								},
							}},
						},
					},
				},
			},
		},
	}
	if err := resolved.Validate(valid); err != nil {
		t.Fatalf("canonical one-family targets were rejected: %v", err)
	}

	for name, target := range map[string]map[string]any{
		"reference":             {"family": "ref", "value": "recorded-target", "name": ""},
		"selector":              {"family": "selector", "value": "li.next > a", "name": ""},
		"selector with visible": {"family": "selectorVisible", "value": "li.next > a", "name": ""},
		"selector unfiltered":   {"family": "selectorUnfiltered", "value": "li.next > a", "name": ""},
		"text":                  {"family": "text", "value": "{{category}}", "name": ""},
		"visible text":          {"family": "textVisible", "value": "{{category}}", "name": ""},
		"aria label":            {"family": "ariaLabel", "value": "Next", "name": ""},
		"role and role name":    {"family": "role", "value": "link", "name": "Next"},
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			candidate := strictCloneMap(t, valid)
			strictTestSteps(candidate)[0].(map[string]any)["target"] = target
			if err := resolved.Validate(candidate); err != nil {
				t.Fatalf("canonical ordinary target %q was rejected: %v", name, err)
			}
		})
	}

	for name, mutate := range map[string]func(map[string]any){
		"category click role plus roleName plus text": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["target"] = map[string]any{
				"role": "link", "roleName": "{{category}}", "text": "{{category}}",
			}
		},
		"category click visible false": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["target"] = map[string]any{
				"text": "{{category}}", "visible": false,
			}
		},
		"pagination absence selector plus text": func(value map[string]any) {
			loop := strictTestSteps(value)[1].(map[string]any)
			condition := loop["steps"].([]any)[2].(map[string]any)["condition"].(map[string]any)
			condition["target"] = map[string]any{"selector": "li.next > a", "text": "next"}
		},
		"pagination click selector plus text": func(value map[string]any) {
			loop := strictTestSteps(value)[1].(map[string]any)
			branch := loop["steps"].([]any)[2].(map[string]any)["else"].([]any)
			branch[0].(map[string]any)["target"] = map[string]any{
				"selector": "li.next > a", "text": "next",
			}
		},
		"attempt 5 pagination condition and click selector plus text plus visible": func(value map[string]any) {
			loop := strictTestSteps(value)[1].(map[string]any)
			conditional := loop["steps"].([]any)[2].(map[string]any)
			mixed := map[string]any{
				"selector": "li.next > a", "text": "next", "visible": true,
			}
			conditional["condition"].(map[string]any)["target"] = mixed
			conditional["else"].([]any)[0].(map[string]any)["target"] = mixed
		},
		"empty target": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["target"] = map[string]any{}
		},
		"unknown target property": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["target"] = map[string]any{
				"family": "ariaLabel", "value": "Next", "name": "", "unknown": true,
			}
		},
		"unknown provider family": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["target"] = map[string]any{
				"family": "xpath", "value": "//a", "name": "",
			}
		},
		"missing provider name": func(value map[string]any) {
			strictTestSteps(value)[0].(map[string]any)["target"] = map[string]any{
				"family": "selector", "value": "li.next > a",
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := strictCloneMap(t, valid)
			mutate(candidate)
			if err := resolved.Validate(candidate); err == nil {
				t.Fatalf("captured mixed ordinary target %q passed strict schema: %#v", name, candidate)
			}
		})
	}
}

func TestWorkflowStrictOutputSchemaAcceptsBaiduShapedRelationalCatalog(t *testing.T) {
	const baiduCatalog = `{
		"version":"selector-catalog-v3",
		"catalogHash":"baidu-catalog-hash",
		"candidates":[{
			"rowCandidateId":"r_organic",
			"observedSelector":"#content_left > .result",
			"fieldCandidates":[{
				"fieldCandidateId":"f_title",
				"observedRelativeSelector":".t",
				"supportedTypes":["text"],
				"nonEmptyText":true
			},{
				"fieldCandidateId":"f_summary",
				"observedRelativeSelector":"[role=\"text\"]",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		},{
			"rowCandidateId":"r_special_module",
			"observedSelector":"#content_left > .result-op",
			"fieldCandidates":[{
				"fieldCandidateId":"f_special_title",
				"observedRelativeSelector":".t",
				"supportedTypes":["text"],
				"nonEmptyText":true
			},{
				"fieldCandidateId":"f_special_summary",
				"observedRelativeSelector":".c-abstract",
				"supportedTypes":["text"],
				"nonEmptyText":true
			}]
		}]
	}`
	requirement := models.CollectionRequirementSpec{
		Title:          "Baidu search results",
		Description:    "Collect visible organic search results.",
		RequiredInputs: []models.RequirementInput{{Name: "keyword", Type: models.RequirementValueString}},
		OptionalInputs: []models.RequirementInput{},
		OutputFields: []models.RequirementOutputField{
			{Name: "title", Type: models.RequirementValueString},
			{Name: "summary", Type: models.RequirementValueString},
		},
		SampleOutput: map[string]any{"title": "Example", "summary": "Example summary"},
	}
	baseline := dslWorkflowBaseline()
	output, err := workflowStrictOutput(requirement, baseline, baiduCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Schema) >= maxStrictOutputSchemaBytes {
		t.Fatalf("bounded Baidu-shaped schema is unexpectedly large: %d bytes", len(output.Schema))
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)
	valid := map[string]any{
		"selectorCatalogHash": "baidu-catalog-hash",
		"rule": map[string]any{
			"id": baseline.ID, "version": baseline.Version, "name": baseline.Name,
			"domain": "example.com", "entry": baseline.Entry,
			"steps": []any{
				map[string]any{
					"action": "extract", "name": "items",
					"target":   map[string]any{"rowCandidateId": "r_organic", "visible": true},
					"multiple": true, "onEmpty": "fail",
					"fields": map[string]any{
						"title":   map[string]any{"type": "text", "fieldCandidateId": "f_title"},
						"summary": map[string]any{"type": "text", "fieldCandidateId": "f_summary"},
					},
				},
				map[string]any{
					"action": "loop", "type": "forEach",
					"items": "{{extracted.items}}", "as": "item",
					"steps": []any{map[string]any{
						"action": "sendResult", "immediate": true,
						"payload": map[string]any{
							"title": "{{loopItem.title}}", "summary": "{{loopItem.summary}}",
						},
					}},
				},
			},
		},
	}
	if err := resolved.Validate(valid); err != nil {
		t.Fatalf("valid Baidu-shaped strict workflow rejected: %v", err)
	}
	crossScope := strictCloneMap(t, valid)
	fields := strictTestSteps(crossScope)[0].(map[string]any)["fields"].(map[string]any)
	fields["summary"].(map[string]any)["fieldCandidateId"] = "f_special_summary"
	if err := resolved.Validate(crossScope); err == nil {
		t.Fatal("Baidu-shaped cross-scope summary candidate passed strict schema")
	}
}

func TestWorkflowStrictOutputSchemaRejectsBingResultWithNoAuthorizedFieldTypes(t *testing.T) {
	const bingCatalog = `{
		"version":"selector-catalog-v4",
		"catalogHash":"bing-catalog-hash",
		"candidates":[{
			"rowCandidateId":"r_organic",
			"observedSelector":"#b_results > .b_algo",
			"fieldCandidates":[{
				"fieldCandidateId":"f_title",
				"observedRelativeSelector":"h2",
				"supportedTypes":[]
			},{
				"fieldCandidateId":"f_host",
				"observedRelativeSelector":"cite",
				"supportedTypes":[]
			}]
		},{
			"rowCandidateId":"r_unrelated",
			"observedSelector":"#b_footerItems > ul > li",
			"fieldCandidates":[{
				"fieldCandidateId":"f_unrelated_title",
				"observedRelativeSelector":"span",
				"supportedTypes":["text"]
			},{
				"fieldCandidateId":"f_unrelated_host",
				"observedRelativeSelector":"a",
				"supportedTypes":["text"]
			}]
		}]
	}`
	requirement := models.CollectionRequirementSpec{
		Title:       "Bing result identity",
		Description: "Report one visible organic Bing result without opening it.",
		OutputFields: []models.RequirementOutputField{
			{Name: "title", Type: models.RequirementValueString, Description: "Visible result level-two heading"},
			{Name: "website", Type: models.RequirementValueString, Description: "Visible result citation host"},
		},
	}
	if _, err := workflowStrictOutput(requirement, dslWorkflowBaseline(), bingCatalog); err == nil ||
		!strings.Contains(err.Error(), "no target with supported direct fields") {
		t.Fatalf("Bing catalog without authorized organic fields must fail before provider dispatch, got %v", err)
	}
}

func TestWorkflowStrictOutputSchemaAcceptsBooksShapedAttrFields(t *testing.T) {
	const booksCatalog = `{
		"version":"selector-catalog-v4",
		"catalogHash":"books-catalog-hash",
		"candidates":[{
			"rowCandidateId":"r_product",
			"fieldCandidates":[{
				"fieldCandidateId":"f_title",
				"supportedTypes":["attr"]
			},{
				"fieldCandidateId":"f_price",
				"supportedTypes":["text"]
			},{
				"fieldCandidateId":"f_availability",
				"supportedTypes":["text"]
			},{
				"fieldCandidateId":"f_rating",
				"supportedTypes":["attr"]
			},{
				"fieldCandidateId":"f_product_url",
				"supportedTypes":["attr"]
			}]
		},{
			"rowCandidateId":"r_other",
			"fieldCandidates":[{
				"fieldCandidateId":"f_other_product_url",
				"supportedTypes":["attr"]
			}]
		}]
	}`
	requirement := models.CollectionRequirementSpec{
		Title:       "Books by category",
		Description: "Collect one category's visible books.",
		RequiredInputs: []models.RequirementInput{{
			Name: "category", Type: models.RequirementValueString,
		}},
		OutputFields: []models.RequirementOutputField{
			{Name: "title", Type: models.RequirementValueString},
			{Name: "price", Type: models.RequirementValueString},
			{Name: "availability", Type: models.RequirementValueString},
			{Name: "rating", Type: models.RequirementValueString},
			{Name: "product_url", Type: models.RequirementValueString},
		},
		SampleOutput: map[string]any{
			"title": "Example", "price": "£10.00", "availability": "In stock",
			"rating": "Three", "product_url": "https://example.com/catalogue/example_1/index.html",
		},
	}
	baseline := dslWorkflowBaseline()
	output, err := workflowStrictOutput(requirement, baseline, booksCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Schema) >= maxStrictOutputSchemaBytes {
		t.Fatalf("bounded Books-shaped schema is unexpectedly large: %d bytes", len(output.Schema))
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)
	valid := map[string]any{
		"selectorCatalogHash": "books-catalog-hash",
		"rule": map[string]any{
			"id": baseline.ID, "version": baseline.Version, "name": baseline.Name,
			"domain": "example.com", "entry": baseline.Entry,
			"steps": []any{
				map[string]any{
					"action": "click",
					"target": map[string]any{
						"family": "textVisible", "value": "{{category}}", "name": "",
					},
				},
				map[string]any{
					"action": "loop", "type": "fixedCount", "count": 10,
					"steps": []any{
						map[string]any{
							"action": "extract", "name": "items",
							"target":   map[string]any{"rowCandidateId": "r_product", "visible": true},
							"multiple": true, "onEmpty": "fail",
							"fields": map[string]any{
								"title": map[string]any{
									"type": "attr", "fieldCandidateId": "f_title", "attr": "title",
								},
								"price": map[string]any{
									"type": "text", "fieldCandidateId": "f_price",
								},
								"availability": map[string]any{
									"type": "text", "fieldCandidateId": "f_availability",
								},
								"rating": map[string]any{
									"type": "attr", "fieldCandidateId": "f_rating", "attr": "class",
								},
								"product_url": map[string]any{
									"type": "attr", "fieldCandidateId": "f_product_url",
									"attr": "href", "resolve": true,
								},
							},
						},
						map[string]any{
							"action": "loop", "type": "forEach",
							"items": "{{extracted.items}}", "as": "item",
							"steps": []any{map[string]any{
								"action": "sendResult", "immediate": true,
								"payload": map[string]any{
									"title":        "{{loopItem.title}}",
									"price":        "{{loopItem.price}}",
									"availability": "{{loopItem.availability}}",
									"rating":       "{{loopItem.rating}}",
									"product_url":  "{{loopItem.product_url}}",
								},
							}},
						},
						map[string]any{
							"action": "if",
							"condition": map[string]any{
								"type": "elementNotExists",
								"target": map[string]any{
									"family": "selector", "value": "li.next > a", "name": "",
								},
							},
							"then": []any{map[string]any{"action": "break"}},
							"else": []any{map[string]any{
								"action": "click",
								"target": map[string]any{
									"family": "selector", "value": "li.next > a", "name": "",
								},
							}},
						},
					},
				},
			},
		},
	}
	if err := resolved.Validate(valid); err != nil {
		t.Fatalf("valid Books-shaped strict workflow rejected: %v", err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"resolve false": func(value map[string]any) {
			strictTestExtractFields(value)["product_url"].(map[string]any)["resolve"] = false
		},
		"resolve null": func(value map[string]any) {
			strictTestExtractFields(value)["product_url"].(map[string]any)["resolve"] = nil
		},
		"resolve string": func(value map[string]any) {
			strictTestExtractFields(value)["product_url"].(map[string]any)["resolve"] = "true"
		},
		"cross cohort candidate": func(value map[string]any) {
			strictTestExtractFields(value)["product_url"].(map[string]any)["fieldCandidateId"] =
				"f_other_product_url"
		},
		"unsupported candidate field type": func(value map[string]any) {
			strictTestExtractFields(value)["price"].(map[string]any)["type"] = "attr"
			strictTestExtractFields(value)["price"].(map[string]any)["attr"] = "title"
		},
		"unknown descriptor property": func(value map[string]any) {
			strictTestExtractFields(value)["title"].(map[string]any)["selector"] = "h3 > a"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := strictCloneMap(t, valid)
			mutate(candidate)
			if err := resolved.Validate(candidate); err == nil {
				t.Fatalf("invalid Books-shaped strict workflow %q was accepted: %#v", name, candidate)
			}
		})
	}
}

func TestWorkflowStrictOutputSchemaUsesBranchFreeOrdinaryTargetIR(t *testing.T) {
	output, err := workflowStrictOutput(
		dslWorkflowRequirement(),
		dslWorkflowBaseline(),
		strictCatalogFixture,
	)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(output.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schema["$defs"].(map[string]any)
	actionBranches := definitions["action"].(map[string]any)["anyOf"].([]any)
	expectedActionBranches := map[string]int{
		"click":                 1,
		"type":                  4,
		"select":                1,
		"waitForElementVisible": 1,
		"waitForElementHidden":  1,
		"waitForText":           1,
	}
	actualActionBranches := map[string]int{}
	conditionBranches := 0
	conditionTypes := map[string]int{}
	for index, rawBranch := range actionBranches {
		branch := rawBranch.(map[string]any)
		properties := branch["properties"].(map[string]any)
		action := strictTestSchemaEnum(properties["action"])
		if expectedActionBranches[action] > 0 {
			actualActionBranches[action]++
			strictAssertBranchFreeProviderTarget(
				t,
				properties["target"],
				fmt.Sprintf("action.%s[%d].target", action, index),
			)
		}
		if action != "if" {
			continue
		}
		condition := properties["condition"].(map[string]any)
		conditionBranches++
		conditionProperties := condition["properties"].(map[string]any)
		for _, rawType := range conditionProperties["type"].(map[string]any)["enum"].([]any) {
			conditionTypes[fmt.Sprint(rawType)]++
		}
		strictAssertBranchFreeProviderTarget(
			t,
			conditionProperties["target"],
			fmt.Sprintf("condition[%d].target", index),
		)
	}
	for action, expected := range expectedActionBranches {
		if actualActionBranches[action] != expected {
			t.Fatalf(
				"ordinary action %q has %d direct target branches, want %d",
				action,
				actualActionBranches[action],
				expected,
			)
		}
	}
	if conditionBranches != 1 ||
		conditionTypes["elementExists"] != 1 ||
		conditionTypes["elementNotExists"] != 1 {
		t.Fatalf(
			"element condition did not use one branch-free target: total=%d types=%v",
			conditionBranches,
			conditionTypes,
		)
	}
}

func TestWorkflowStrictOutputSchemaHonorsSizeBoundAfterTargetFlattening(t *testing.T) {
	requirement := dslWorkflowRequirement()
	requirement.OutputFields = []models.RequirementOutputField{
		{Name: "title", Type: models.RequirementValueString},
		{Name: "price", Type: models.RequirementValueString},
		{Name: "availability", Type: models.RequirementValueString},
		{Name: "rating", Type: models.RequirementValueString},
		{Name: "product_url", Type: models.RequirementValueString},
	}
	boundedCatalog := strictTestCatalog(t, 64, len(requirement.OutputFields))
	output, err := workflowStrictOutput(requirement, dslWorkflowBaseline(), boundedCatalog)
	if err != nil {
		t.Fatalf("bounded 64-target schema failed closed: %v", err)
	}
	if len(output.Schema) >= maxStrictOutputSchemaBytes {
		t.Fatalf(
			"bounded 64-target schema is %d bytes, must stay under %d",
			len(output.Schema),
			maxStrictOutputSchemaBytes,
		)
	}
	t.Logf("bounded 64-target strict schema size: %d bytes", len(output.Schema))

	oversized := dslWorkflowRequirement()
	oversized.OutputFields = make([]models.RequirementOutputField, 2048)
	for index := range oversized.OutputFields {
		oversized.OutputFields[index] = models.RequirementOutputField{
			Name: fmt.Sprintf("field_%04d", index),
			Type: models.RequirementValueString,
		}
	}
	if _, err := workflowStrictOutput(
		oversized,
		dslWorkflowBaseline(),
		strictTestCatalog(t, 1, len(oversized.OutputFields)),
	); err == nil || !strings.Contains(err.Error(), "exceeds the 524288-byte bound") {
		t.Fatalf("oversized strict schema did not fail at the 512-KiB ceiling: %v", err)
	}
}

func TestWorkflowStrictOutputSchemaUsesDeepSeekStrictObjectSubset(t *testing.T) {
	output, err := workflowStrictOutput(
		dslWorkflowRequirement(),
		dslWorkflowBaseline(),
		strictCatalogFixture,
	)
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(output.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	var walk func(any, string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case map[string]any:
			if typed["type"] == "object" {
				properties, _ := typed["properties"].(map[string]any)
				required, _ := typed["required"].([]any)
				if typed["additionalProperties"] != false {
					t.Fatalf("%s object permits additional properties: %#v", path, typed)
				}
				if len(required) != len(properties) {
					t.Fatalf("%s does not require every property: %#v", path, typed)
				}
				requiredSet := map[string]bool{}
				for _, rawName := range required {
					requiredSet[rawName.(string)] = true
				}
				for property := range properties {
					if !requiredSet[property] {
						t.Fatalf("%s property %q is optional", path, property)
					}
				}
			}
			for key, child := range typed {
				walk(child, path+"."+key)
			}
		case []any:
			for _, child := range typed {
				walk(child, path+"[]")
			}
		}
	}
	walk(schema, "$")
}

func TestWorkflowStrictOutputSchemaAllowsTargetFreeURLGuardedExtractions(t *testing.T) {
	requirement := dslWorkflowRequirement()
	requirement.RequiredInputs = []models.RequirementInput{{
		Name: "section_number", Type: models.RequirementValueString,
	}}
	catalog := `{
		"version":"selector-catalog-v4",
		"catalogHash":"guarded-catalog",
		"candidates":[
			{"targetCandidateId":"t_section_2","fieldCandidates":[{"fieldCandidateId":"f_2","supportedTypes":["text"],"nonEmptyText":true}]},
			{"targetCandidateId":"t_section_3","fieldCandidates":[{"fieldCandidateId":"f_3","supportedTypes":["text"],"nonEmptyText":true}]},
			{"targetCandidateId":"t_section_4","fieldCandidates":[{"fieldCandidateId":"f_4","supportedTypes":["text"],"nonEmptyText":true}]}
		]
	}`
	baseline := dslWorkflowBaseline()
	output, err := workflowStrictOutput(requirement, baseline, catalog)
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolveStrictOutputSchema(t, output.Schema)
	steps := []any{map[string]any{
		"action": "navigate", "url": "https://example.com/products#section-{{section_number}}",
	}}
	for _, section := range []string{"2", "3", "4"} {
		steps = append(steps, map[string]any{
			"action": "extractText", "name": "name",
			"target": map[string]any{
				"targetCandidateId": "t_section_" + section, "visible": true,
			},
			"condition": map[string]any{
				"type": "urlMatches", "pattern": "#section-" + section + "$",
			},
		})
	}
	steps = append(steps, map[string]any{
		"action": "sendResult", "payload": map[string]any{"name": "{{extracted.name}}"},
		"immediate": true,
	})
	value := map[string]any{
		"selectorCatalogHash": "guarded-catalog",
		"rule": map[string]any{
			"id": baseline.ID, "version": baseline.Version, "name": "guarded",
			"domain": "example.com", "entry": baseline.Entry, "steps": steps,
		},
	}
	if err := resolved.Validate(value); err != nil {
		t.Fatalf("target-free URL-guarded extractions were rejected: %v", err)
	}

	invalid := strictCloneMap(t, value)
	condition := strictTestSteps(invalid)[1].(map[string]any)["condition"].(map[string]any)
	condition["target"] = map[string]any{
		"family": "selector", "value": "#section-2", "name": "",
	}
	if err := resolved.Validate(invalid); err == nil {
		t.Fatal("target-bearing extraction condition was accepted")
	}
}

func TestDSLWorkflowAttachesStrictOutputOnlyWhenEnabled(t *testing.T) {
	for name, enabled := range map[string]bool{"disabled": false, "enabled": true} {
		t.Run(name, func(t *testing.T) {
			var captured llm.CompletionRequest
			fake := &fakeWorkflowCompleter{respond: func(request llm.CompletionRequest) (*llm.CompletionResult, error) {
				captured = request
				return completion(ruleEnvelope(t, dslWorkflowBaseline())), nil
			}}
			cfg := &config.Config{LLMEnabled: true, LLMModel: "fake-model"}
			if enabled {
				cfg.LLMOpenAIStrictToolOutput = boolPointerDSL(true)
			}
			_, _, err := NewDSLWorkflow(cfg, fake).Generate(
				context.Background(),
				map[string]any{
					"events":    []any{map[string]any{"action": "click"}},
					"snapshots": []any{map[string]any{"phase": "initial"}},
				},
				dslWorkflowRequirement(),
				dslWorkflowBaseline(),
				nil,
				"",
				strictCatalogFixture,
			)
			if err != nil {
				t.Fatal(err)
			}
			if enabled {
				if captured.StructuredOutput == nil ||
					captured.StructuredOutput.Name != dslStrictOutputName ||
					!strings.Contains(string(captured.StructuredOutput.Schema), "r_exact") {
					t.Fatalf("strict workflow request omitted its schema: %+v", captured.StructuredOutput)
				}
			} else if captured.StructuredOutput != nil {
				t.Fatalf("ordinary workflow unexpectedly enabled strict output: %+v", captured.StructuredOutput)
			}
		})
	}
}

func resolveStrictOutputSchema(t *testing.T, raw json.RawMessage) *jsonschema.Resolved {
	t.Helper()
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func strictCloneMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func strictTestSteps(value map[string]any) []any {
	return value["rule"].(map[string]any)["steps"].([]any)
}

func strictTestExtractFields(value map[string]any) map[string]any {
	loop := strictTestSteps(value)[1].(map[string]any)
	return loop["steps"].([]any)[0].(map[string]any)["fields"].(map[string]any)
}

func strictAssertBranchFreeProviderTarget(t *testing.T, raw any, path string) {
	t.Helper()
	target, ok := raw.(map[string]any)
	if !ok || target["type"] != "object" {
		t.Fatalf("%s is not a direct object schema: %#v", path, raw)
	}
	if _, nested := target["anyOf"]; nested {
		t.Fatalf("%s retains a nested ordinary-target anyOf: %#v", path, target)
	}
	if target["additionalProperties"] != false {
		t.Fatalf("%s permits additional target properties: %#v", path, target)
	}
	properties, ok := target["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no target properties: %#v", path, target)
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if family := strings.Join(keys, ","); family != "family,name,value" {
		t.Fatalf("%s has provider target properties %q: %#v", path, family, target)
	}
	familyValues, _ := properties["family"].(map[string]any)["enum"].([]any)
	gotFamilies := make([]string, 0, len(familyValues))
	for _, rawFamily := range familyValues {
		gotFamilies = append(gotFamilies, fmt.Sprint(rawFamily))
	}
	wantFamilies := []string{
		"ariaLabel", "ref", "role", "selector", "selectorUnfiltered", "selectorVisible",
		"text", "textVisible",
	}
	sort.Strings(gotFamilies)
	if !reflect.DeepEqual(gotFamilies, wantFamilies) {
		t.Fatalf("%s has provider families %v, want %v", path, gotFamilies, wantFamilies)
	}
	required := target["required"].([]any)
	if len(required) != len(properties) {
		t.Fatalf("%s does not require its selected family: %#v", path, target)
	}
}

func strictTestSchemaEnum(raw any) string {
	schema, _ := raw.(map[string]any)
	values, _ := schema["enum"].([]any)
	if len(values) != 1 {
		return ""
	}
	return fmt.Sprint(values[0])
}

func strictTestCatalog(t *testing.T, targetCount, fieldCount int) string {
	t.Helper()
	candidates := make([]any, targetCount)
	for index := range candidates {
		fields := make([]any, fieldCount)
		for fieldIndex := range fields {
			fields[fieldIndex] = map[string]any{
				"fieldCandidateId": fmt.Sprintf("f_%02d_%04d", index, fieldIndex),
				"supportedTypes":   []any{"text"},
			}
		}
		candidates[index] = map[string]any{
			"rowCandidateId":  fmt.Sprintf("r_%02d", index),
			"fieldCandidates": fields,
		}
	}
	encoded, err := json.Marshal(map[string]any{
		"version":     "selector-catalog-v4",
		"catalogHash": "bounded-catalog-hash",
		"candidates":  candidates,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func boolPointerDSL(value bool) *bool {
	return &value
}
