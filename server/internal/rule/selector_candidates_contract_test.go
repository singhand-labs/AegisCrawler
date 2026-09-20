package rule

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestOpaqueSelectorCandidatesResolveNestedActionsHooksAndIgnoreBusinessFieldNames(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("span", map[string]string{"id": "result"}, selectorTextForTest("Result")),
		selectorElementForTest("span", map[string]string{"id": "hook-result"}, selectorTextForTest("Hook")),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	rule := &models.Rule{
		Steps: models.JSON(`[
			{"action":"group","steps":[
				{"action":"extractText","name":"result","target":{"selector":"#result","visible":true}},
				{"action":"sendResult","payload":{
					"rowCandidateId":"business-row",
					"targetCandidateId":"business-target",
					"fieldCandidateId":"business-field"
				}}
			]}
		]`),
		Hooks: models.JSON(`{
			"cleanup":[{"action":"extractText","name":"hook","target":{"selector":"#hook-result"}}]
		}`),
	}
	prompt, provider, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	report, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets != 2 {
		t.Fatalf("nested/hook extraction targets were not both resolved: %+v", report)
	}
	var steps []any
	if err := json.Unmarshal(provider.Steps, &steps); err != nil {
		t.Fatal(err)
	}
	group := steps[0].(map[string]any)
	children := group["steps"].([]any)
	payload := children[1].(map[string]any)["payload"].(map[string]any)
	want := map[string]any{
		"rowCandidateId": "business-row", "targetCandidateId": "business-target", "fieldCandidateId": "business-field",
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("business payload names were treated as provider IR: %#v", payload)
	}
}

func TestOpaqueSelectorCandidatesRejectNonCanonicalizedMixedTargetLocators(t *testing.T) {
	catalog, trusted := singletonTextCandidateForContractTest(t)
	prompt, _, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	cases := map[string]any{
		"$ref": "alias", "xpath": "//*[@id='result']", "text": "Result",
		"ariaLabel": "Result", "role": "status", "roleName": "Result", "position": map[string]any{"x": 1, "y": 2},
		"frame": "#frame", "shadowPath": []any{"#host"}, "index": float64(0), "multiple": true,
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			_, provider, prepareErr := catalog.PrepareProviderPrompt(trusted)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
				step["target"].(map[string]any)[key] = value
			})
			if _, resolveErr := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(resolveErr, ErrInvalidProvisionalRule) ||
				!strings.Contains(resolveErr.Error(), "forbidden in provider extraction output") {
				t.Fatalf("mixed target key %q was not rejected: %v", key, resolveErr)
			}
		})
	}
}

func TestOpaqueSelectorCandidatesRejectUnsupportedFieldModifiersAndTargetBearingActionConditions(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		})),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target":    map[string]any{"selector": "#results > .card"},
		"condition": map[string]any{"type": "valueEquals", "value": "ready"},
		"fields": map[string]any{
			"title": map[string]any{
				"type": "text", "selector": ".title",
			},
		},
	})
	prompt, _, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatalf("variable-only action conditions should remain eligible: %v", err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	_, provider, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
		step["condition"] = map[string]any{
			"type":   "elementExists",
			"target": map[string]any{"selector": "#gate"},
		}
	})
	if _, resolveErr := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(resolveErr, ErrInvalidProvisionalRule) ||
		!strings.Contains(resolveErr.Error(), "condition.target") {
		t.Fatalf("target-bearing action condition was not rejected: %v", resolveErr)
	}

	for _, modifier := range []string{"condition", "required", "transform"} {
		t.Run("field_"+modifier, func(t *testing.T) {
			_, provider, prepareErr := catalog.PrepareProviderPrompt(trusted)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
				field := step["fields"].(map[string]any)["title"].(map[string]any)
				switch modifier {
				case "condition":
					field[modifier] = map[string]any{"type": "valueEquals", "value": "ready"}
				case "required":
					field[modifier] = true
				case "transform":
					field[modifier] = "value.trim()"
				}
			})
			if _, resolveErr := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(resolveErr, ErrInvalidProvisionalRule) ||
				!strings.Contains(resolveErr.Error(), "."+modifier+" is not supported") {
				t.Fatalf("unsupported field modifier %q was not rejected: %v", modifier, resolveErr)
			}
		})
	}

	invalid := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "title",
		"target":    map[string]any{"selector": "#results"},
		"condition": map[string]any{"type": "elementExists", "target": map[string]any{"selector": "#gate"}},
	})
	if _, _, err := catalog.PrepareProviderPrompt(invalid); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("trusted target-bearing condition was not a terminal source contract error: %v", err)
	}

	unsupportedField := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card"},
		"fields": map[string]any{
			"title": map[string]any{
				"type": "text", "selector": ".title",
				"condition": map[string]any{"type": "valueEquals", "value": "ready"},
			},
		},
	})
	if _, _, err := catalog.PrepareProviderPrompt(unsupportedField); !errors.Is(err, ErrSelectorSourceUnavailable) ||
		!strings.Contains(err.Error(), ".condition is not supported") {
		t.Fatalf("trusted unsupported field condition entered provider IR: %v", err)
	}
}

func TestTrustedSelectorMappingRejectsUnsupportedInlineAndAliasedScope(t *testing.T) {
	catalog, _ := singletonTextCandidateForContractTest(t)
	cases := map[string]any{
		"ariaLabel": "Result", "role": "status", "roleName": "Result", "index": float64(0),
		"multiple": true, "frame": "#frame", "shadowPath": []any{"#host"},
	}
	for key, value := range cases {
		t.Run("inline_"+key, func(t *testing.T) {
			target := map[string]any{"selector": "#result", key: value}
			rule := selectorRuleForTest(map[string]any{
				"action": "extractHtml", "name": "result", "target": target,
			})
			if _, _, err := catalog.PrepareProviderPrompt(rule); !errors.Is(err, ErrSelectorSourceUnavailable) ||
				!strings.Contains(err.Error(), key) {
				t.Fatalf("unsupported trusted inline %q was not preserved as a source error: %v", key, err)
			}
		})
		t.Run("alias_"+key, func(t *testing.T) {
			alias, _ := json.Marshal(map[string]any{
				"result": map[string]any{"selector": "#result", key: value},
			})
			rule := selectorRuleForTest(map[string]any{
				"action": "extractHtml", "name": "result", "target": map[string]any{"$ref": "result"},
			})
			rule.Selectors = models.JSON(alias)
			if _, _, err := catalog.PrepareProviderPrompt(rule); !errors.Is(err, ErrSelectorSourceUnavailable) ||
				!strings.Contains(err.Error(), key) {
				t.Fatalf("unsupported trusted alias %q was not preserved as a source error: %v", key, err)
			}
		})
	}
}

func TestOnlyExtractMayUseRepeatedCandidateCardinality(t *testing.T) {
	catalog, trusted := singletonTextCandidateForContractTest(t)
	prompt, _, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	_, provider, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
		step["multiple"] = false
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "only by action extract") {
		t.Fatalf("standalone extraction accepted step.multiple: %v", err)
	}

	trustedWithMultiple := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "result", "multiple": false,
		"target": map[string]any{"selector": "#result"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(trustedWithMultiple); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("trusted standalone extraction accepted step.multiple: %v", err)
	}
}

func TestStandaloneExtractionCandidatesRequireActionSpecificEvidence(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("div", map[string]string{"id": "empty"}),
		selectorElementForTest("div", map[string]string{"id": "plain"}, selectorTextForTest("plain text")),
		selectorElementForTest("a", map[string]string{"id": "link", "href": "/item"}, selectorTextForTest("Link")),
		selectorElementForTest("pre", map[string]string{"id": "json"}, selectorTextForTest(`{"ok":true}`)),
		selectorElementForTest("pre", map[string]string{"id": "bad-json"}, selectorTextForTest("not-json")),
		selectorElementForTest("table", map[string]string{"id": "table"},
			selectorElementForTest("tr", nil, selectorElementForTest("th", nil, selectorTextForTest("Name"))),
			selectorElementForTest("tr", nil, selectorElementForTest("td", nil, selectorTextForTest("Cell"))),
		),
		selectorElementForTest("div", map[string]string{"id": "not-table"},
			selectorElementForTest("div", nil, selectorTextForTest("Cell")),
		),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	valid := []map[string]any{
		{"action": "extractText", "name": "text", "target": map[string]any{"selector": "#plain"}},
		{"action": "extractAttribute", "name": "href", "attr": "href", "target": map[string]any{"selector": "#link"}},
		{"action": "extractJson", "name": "json", "target": map[string]any{"selector": "#json"}},
		{"action": "extractTable", "name": "table", "target": map[string]any{"selector": "#table"}},
		{"action": "extractHtml", "name": "html", "target": map[string]any{"selector": "#empty"}},
	}
	for _, step := range valid {
		t.Run("valid_"+step["action"].(string), func(t *testing.T) {
			prompt, provider, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step))
			if err != nil {
				t.Fatal(err)
			}
			var scope SelectorPromptCatalog
			if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); err != nil {
				t.Fatal(err)
			}
			_, repeatedProvider, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step))
			if err != nil {
				t.Fatal(err)
			}
			mutateSelectorCandidateRuleStep(t, repeatedProvider, func(providerStep map[string]any) {
				providerStep["multiple"] = false
			})
			if _, err := catalog.ResolveProviderExtractionCandidates(repeatedProvider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
				!strings.Contains(err.Error(), "only by action extract") {
				t.Fatalf("standalone action %s accepted step.multiple: %v", step["action"], err)
			}
		})
	}
	invalid := []map[string]any{
		{"action": "extractText", "name": "text", "target": map[string]any{"selector": "#empty"}},
		{"action": "extractAttribute", "name": "href", "attr": "href", "target": map[string]any{"selector": "#plain"}},
		{"action": "extractJson", "name": "json", "target": map[string]any{"selector": "#bad-json"}},
		{"action": "extractTable", "name": "table", "target": map[string]any{"selector": "#not-table"}},
	}
	for _, step := range invalid {
		t.Run("trusted_invalid_"+step["action"].(string), func(t *testing.T) {
			if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(err, ErrSelectorSourceUnavailable) {
				t.Fatalf("ungrounded trusted standalone action was not terminal: %v", err)
			}
		})
		t.Run("provider_invalid_"+step["action"].(string), func(t *testing.T) {
			htmlStep := map[string]any{
				"action": "extractHtml", "name": "value", "target": step["target"],
			}
			prompt, provider, err := catalog.PrepareProviderPrompt(selectorRuleForTest(htmlStep))
			if err != nil {
				t.Fatal(err)
			}
			var scope SelectorPromptCatalog
			if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
				t.Fatal(err)
			}
			mutateSelectorCandidateRuleStep(t, provider, func(providerStep map[string]any) {
				providerStep["action"] = step["action"]
				if attr, ok := step["attr"]; ok {
					providerStep["attr"] = attr
				}
			})
			if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) {
				t.Fatalf("provider switched to ungrounded standalone action: %v", err)
			}
		})
	}
}

func TestExtractJSONPathMustBeStaticAndResolveInEveryRecordedNode(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("pre", map[string]string{"id": "json"}, selectorTextForTest(
			`{"data":{"value":42},"items":[{"name":"one"}]}`,
		)),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	valid := selectorRuleForTest(map[string]any{
		"action": "extractJson", "name": "value", "path": "data.value",
		"target": map[string]any{"selector": "#json"},
	})
	prompt, provider, err := catalog.PrepareProviderPrompt(valid)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"data.missing", "{{requestedPath}}", "items.9.name", "items..name",
		"items.01.name", "items.+1.name", "items.-0.name", "items.9007199254740992.name",
	} {
		t.Run(path, func(t *testing.T) {
			candidate := selectorRuleForTest(map[string]any{
				"action": "extractJson", "name": "value", "path": path,
				"target": map[string]any{"selector": "#json"},
			})
			if _, _, err := catalog.PrepareProviderPrompt(candidate); !errors.Is(err, ErrSelectorSourceUnavailable) {
				t.Fatalf("ungrounded JSON path %q was not rejected before provider work: %v", path, err)
			}
			_, provider, err := catalog.PrepareProviderPrompt(valid)
			if err != nil {
				t.Fatal(err)
			}
			mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
				step["path"] = path
			})
			if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) {
				t.Fatalf("provider JSON path %q was not rejected as retryable output: %v", path, err)
			}
		})
	}
}

func TestExtractAttributeRejectsCredentialLikeAndLiveFormValuePaths(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("input", map[string]string{
			"id": "secret", "class": "record", "type": "hidden",
			"value": "[REDACTED]", "data-token": "[REDACTED]",
		}),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	prompt, _, err := catalog.PrepareProviderPrompt(nil)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	secretTargetID := ""
	for id, evidence := range catalog.promptTargets {
		if evidence.selector == "#secret" {
			secretTargetID = id
			break
		}
	}
	if secretTargetID == "" {
		t.Fatal("sensitive singleton was not represented as a capability-limited target candidate")
	}
	for _, attribute := range []string{"value", "data-token", "authorization", "session_cookie", "class"} {
		t.Run(attribute, func(t *testing.T) {
			rule := selectorRuleForTest(map[string]any{
				"action": "extractAttribute", "name": "secret", "attr": attribute,
				"target": map[string]any{"selector": "#secret"},
			})
			if _, _, err := catalog.PrepareProviderPrompt(rule); !errors.Is(err, ErrSelectorSourceUnavailable) {
				t.Fatalf("credential-like attribute %q was not rejected before provider work: %v", attribute, err)
			}
			provider := selectorRuleForTest(map[string]any{
				"action": "extractAttribute", "name": "secret", "attr": attribute,
				"target": map[string]any{"targetCandidateId": secretTargetID},
			})
			if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) {
				t.Fatalf("provider credential-like attribute %q was not rejected: %v", attribute, err)
			}
		})
	}
}

func TestTypedAttributeFieldsCannotBypassCredentialGuard(t *testing.T) {
	row := func() map[string]any {
		return selectorElementForTest("article", map[string]string{"class": "row"},
			selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("Safe title")),
			selectorElementForTest("input", map[string]string{
				"class": "secret", "data-kind": "record", "type": "hidden", "value": "[REDACTED]",
			}),
		)
	}
	root := selectorElementForTest("main", map[string]string{"id": "items"}, row(), row())
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	for _, attribute := range []string{"value", "data-kind"} {
		t.Run(attribute, func(t *testing.T) {
			invalidTrusted := selectorRuleForTest(map[string]any{
				"action": "extract", "name": "rows", "multiple": true,
				"target": map[string]any{"selector": "#items > .row"},
				"fields": map[string]any{
					"secret": map[string]any{"type": "attr", "attr": attribute, "selector": ".secret"},
				},
			})
			if _, _, err := catalog.PrepareProviderPrompt(invalidTrusted); !errors.Is(err, ErrSelectorSourceUnavailable) {
				t.Fatalf("trusted typed attribute %q bypassed credential guard: %v", attribute, err)
			}

			safeShape := selectorRuleForTest(map[string]any{
				"action": "extract", "name": "rows", "multiple": true,
				"target": map[string]any{"selector": "#items > .row"},
				"fields": map[string]any{
					"title": map[string]any{"type": "text", "selector": ".title"},
				},
			})
			prompt, provider, err := catalog.PrepareProviderPrompt(safeShape)
			if err != nil {
				t.Fatal(err)
			}
			var scope SelectorPromptCatalog
			if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
				t.Fatal(err)
			}
			secretFieldID := ""
			for id, evidence := range catalog.promptFields {
				if evidence.selector == ".secret" {
					secretFieldID = id
					break
				}
			}
			if secretFieldID == "" {
				t.Fatal("sensitive field source was not represented as a capability-limited field candidate")
			}
			mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
				field := step["fields"].(map[string]any)["title"].(map[string]any)
				field["type"] = "attr"
				field["attr"] = attribute
				field["fieldCandidateId"] = secretFieldID
			})
			if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) {
				t.Fatalf("provider typed attribute %q bypassed credential guard: %v", attribute, err)
			}
		})
	}
}

func TestSensitiveContentSourcesFailClosedWithoutPoisoningIndependentSafeRowFields(t *testing.T) {
	row := func(title string) map[string]any {
		return selectorElementForTest("article", map[string]string{"class": "row"},
			selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest(title)),
			selectorElementForTest("section", map[string]string{"class": "safe"},
				selectorElementForTest("span", map[string]string{"class": "value"}, selectorTextForTest("public")),
			),
			selectorElementForTest("input", map[string]string{
				"class": "secret", "type": "password", "value": "[REDACTED]",
			}),
		)
	}
	root := selectorElementForTest("main", nil,
		selectorElementForTest("div", map[string]string{"id": "items"}, row("one"), row("two")),
		selectorElementForTest("div", map[string]string{"id": "text-source"},
			selectorTextForTest("public"),
			selectorElementForTest("input", map[string]string{"type": "hidden", "value": "[REDACTED]"}),
		),
		selectorElementForTest("div", map[string]string{"id": "html-source"},
			selectorElementForTest("input", map[string]string{"type": "password", "value": "[REDACTED]"}),
		),
		selectorElementForTest("pre", map[string]string{"id": "json-source"},
			selectorTextForTest(`{"public":true}`),
			selectorElementForTest("input", map[string]string{"type": "hidden", "value": "[REDACTED]"}),
		),
		selectorElementForTest("table", map[string]string{"id": "table-source"},
			selectorElementForTest("tr", nil, selectorElementForTest("th", nil, selectorTextForTest("Name"))),
			selectorElementForTest("tr", nil,
				selectorElementForTest("td", nil,
					selectorTextForTest("Alice"),
					selectorElementForTest("input", map[string]string{"type": "hidden", "value": "[REDACTED]"}),
				),
			),
		),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/items", root))
	for _, step := range []map[string]any{
		{"action": "extractText", "name": "value", "target": map[string]any{"selector": "#text-source"}},
		{"action": "extractHtml", "name": "value", "target": map[string]any{"selector": "#html-source"}},
		{"action": "extractJson", "name": "value", "target": map[string]any{"selector": "#json-source"}},
		{"action": "extractTable", "name": "value", "target": map[string]any{"selector": "#table-source"}},
	} {
		t.Run(step["action"].(string), func(t *testing.T) {
			if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(err, ErrSelectorSourceUnavailable) {
				t.Fatalf("sensitive standalone source was not rejected before provider work: %v", err)
			}
		})
	}

	safeRule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": ".title"},
			"self": map[string]any{
				"type": "exists",
				"fields": map[string]any{
					"titleAgain": map[string]any{"type": "text", "selector": ".title"},
				},
			},
			"scope": map[string]any{
				"type": "html", "selector": ".safe",
				"fields": map[string]any{
					"value": map[string]any{"type": "text", "selector": ".value"},
				},
			},
		},
	})
	prompt, provider, err := catalog.PrepareProviderPrompt(safeRule)
	if err != nil {
		t.Fatalf("unrelated redacted row descendant poisoned independently safe fields: %v", err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	step := selectorRuleStepForTest(t, provider)
	rowID := stringValue(step["target"].(map[string]any)["rowCandidateId"])
	unsafeSelfID := ""
	for id, evidence := range catalog.promptFields {
		if evidence.targetID == rowID && evidence.prompt.ParentFieldCandidateID == "" && evidence.selector == "" {
			unsafeSelfID = id
			break
		}
	}
	if unsafeSelfID == "" {
		t.Fatal("row self source was not represented for capability enforcement")
	}
	mutateSelectorCandidateRuleStep(t, provider, func(providerStep map[string]any) {
		field := providerStep["fields"].(map[string]any)["title"].(map[string]any)
		field["fieldCandidateId"] = unsafeSelfID
		field["type"] = "html"
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) {
		t.Fatalf("provider selected redacted row subtree for typed HTML: %v", err)
	}
}

func TestTypedJSONAndRegexCapabilitiesMatchRuntimeContracts(t *testing.T) {
	row := func(code string) map[string]any {
		return selectorElementForTest("article", map[string]string{"class": "row"},
			selectorElementForTest("pre", map[string]string{"class": "payload"},
				selectorTextForTest(`{"items":[{"name":"first"},{"name":"second"}]}`),
			),
			selectorElementForTest("span", map[string]string{"class": "code"}, selectorTextForTest(code)),
			selectorElementForTest("span", map[string]string{"class": "price"}, selectorTextForTest("$1,249.50")),
			selectorElementForTest("span", map[string]string{
				"class": "label", "data-code": "ABC-42",
			}, selectorTextForTest("label")),
			selectorElementForTest("span", map[string]string{"class": "availability"}, selectorTextForTest("not available")),
		)
	}
	root := selectorElementForTest("main", map[string]string{"id": "items"},
		row(" prefix #ABC-42 suffix "),
		row(" prefix #XYZ-99 suffix "),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/items", root))
	valid := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": map[string]any{
			"payload":      map[string]any{"type": "json", "selector": ".payload", "path": "items.0.name"},
			"code":         map[string]any{"type": "regex", "selector": ".code", "regex": `#[A-Z]+-\d+`},
			"codeText":     map[string]any{"type": "text", "selector": ".code", "regex": `#[A-Z]+-\d+`},
			"price":        map[string]any{"type": "number", "selector": ".price", "regex": `[0-9,.]+`},
			"label":        map[string]any{"type": "attr", "selector": ".label", "attr": "data-code", "regex": `[A-Z]+-[0-9]+`},
			"availability": map[string]any{"type": "text", "selector": ".availability"},
		},
	})
	prompt, provider, err := catalog.PrepareProviderPrompt(valid)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); err != nil {
		t.Fatalf("runtime-backed typed JSON/regex contract did not resolve: %v", err)
	}

	for _, test := range []struct {
		name   string
		field  string
		key    string
		value  any
		remove bool
		error  string
	}{
		{name: "json_noncanonical_index", field: "payload", key: "path", value: "items.01.name"},
		{name: "json_negative_zero", field: "payload", key: "path", value: "items.-0.name"},
		{name: "json_unsafe_integer", field: "payload", key: "path", value: "items.9007199254740992.name"},
		{name: "regex_missing", field: "code", key: "regex", remove: true},
		{name: "regex_invalid", field: "code", key: "regex", value: "("},
		{name: "regex_no_recorded_match", field: "code", key: "regex", value: "^missing$"},
		{
			name: "regex_go_only_inline_flag", field: "code", key: "regex",
			value: `(?i)#[a-z]+-[0-9]+`, error: "portable JavaScript/Go subset",
		},
		{name: "text_regex_no_recorded_match", field: "codeText", key: "regex", value: "^missing$"},
		{name: "attr_regex_no_recorded_match", field: "label", key: "regex", value: "^missing$"},
		{name: "number_regex_non_numeric_match", field: "price", key: "regex", value: `\$`},
		{name: "number_unparseable_evidence", field: "availability", key: "type", value: "number"},
		{name: "css_has_no_recorded_computed_value", field: "availability", key: "type", value: "css"},
		{name: "regex_modifier_ignored_by_json", field: "payload", key: "regex", value: ".*"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, candidate, prepareErr := catalog.PrepareProviderPrompt(valid)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			mutateSelectorCandidateRuleStep(t, candidate, func(providerStep map[string]any) {
				field := providerStep["fields"].(map[string]any)[test.field].(map[string]any)
				if test.remove {
					delete(field, test.key)
				} else {
					field[test.key] = test.value
				}
			})
			_, resolveErr := catalog.ResolveProviderExtractionCandidates(candidate, scope.CatalogHash)
			if !errors.Is(resolveErr, ErrInvalidProvisionalRule) {
				t.Fatalf("provider runtime-incompatible typed field was not rejected: %v", resolveErr)
			}
			if test.error != "" && !strings.Contains(resolveErr.Error(), test.error) {
				t.Fatalf("provider field was rejected by the wrong runtime gate: %v", resolveErr)
			}
		})
	}
}

func TestTrustedAliasExtractionModifiersSurviveOpaqueResolution(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("span", map[string]string{"id": "result"}, selectorTextForTest("Result")),
	)
	for _, test := range []struct {
		name    string
		inline  map[string]any
		timeout float64
		visible bool
	}{
		{name: "inherits_alias", inline: map[string]any{"$ref": "result"}, timeout: 1234, visible: false},
		{name: "inline_overrides", inline: map[string]any{
			"$ref": "result", "timeout": float64(987), "visible": true,
		}, timeout: 987, visible: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
			rule := selectorRuleForTest(map[string]any{
				"action": "extractText", "name": "result", "target": test.inline,
			})
			rule.Selectors = models.JSON(`{
				"result":{"selector":"#result","timeout":1234,"visible":false}
			}`)
			prompt, provider, err := catalog.PrepareProviderPrompt(rule)
			if err != nil {
				t.Fatal(err)
			}
			var scope SelectorPromptCatalog
			if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
				t.Fatal(err)
			}
			providerTarget := selectorRuleStepForTest(t, provider)["target"].(map[string]any)
			if providerTarget["timeout"] != test.timeout || providerTarget["visible"] != test.visible {
				t.Fatalf("provider target lost alias modifiers or inline precedence: %#v", providerTarget)
			}
			if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); err != nil {
				t.Fatal(err)
			}
			resolvedTarget := selectorRuleStepForTest(t, provider)["target"].(map[string]any)
			if resolvedTarget["selector"] != "#result" ||
				resolvedTarget["timeout"] != test.timeout ||
				resolvedTarget["visible"] != test.visible {
				t.Fatalf("resolved target lost alias modifiers: %#v", resolvedTarget)
			}
		})
	}
}

func TestTableCapabilityMatchesExactRuntimeHeaderAndRowSemantics(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("table", map[string]string{"id": "header-only"},
			selectorElementForTest("tr", nil,
				selectorElementForTest("th", nil, selectorTextForTest("Name")),
			),
		),
		selectorElementForTest("table", map[string]string{"id": "header-data"},
			selectorElementForTest("tr", nil,
				selectorElementForTest("th", nil, selectorTextForTest("Name")),
			),
			selectorElementForTest("tr", nil,
				selectorElementForTest("td", nil, selectorTextForTest("Alice")),
			),
		),
		selectorElementForTest("table", map[string]string{"id": "thead-only"},
			selectorElementForTest("thead", nil,
				selectorElementForTest("tr", nil,
					selectorElementForTest("th", nil, selectorTextForTest("Name")),
				),
			),
		),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/tables", root))
	headerOnlyDefault := selectorRuleForTest(map[string]any{
		"action": "extractTable", "name": "table",
		"target": map[string]any{"selector": "#header-only"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(headerOnlyDefault); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("default header-skipping table accepted a header-only source: %v", err)
	}
	theadOnly := selectorRuleForTest(map[string]any{
		"action": "extractTable", "name": "table", "includeHeader": true,
		"target": map[string]any{"selector": "#thead-only"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(theadOnly); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("thead-only table accepted no tbody data rows: %v", err)
	}

	headerAsData := selectorRuleForTest(map[string]any{
		"action": "extractTable", "name": "table", "includeHeader": true,
		"target": map[string]any{"selector": "#header-only"},
	})
	prompt, provider, err := catalog.PrepareProviderPrompt(headerAsData)
	if err != nil {
		t.Fatalf("includeHeader table should emit its non-empty row: %v", err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
		delete(step, "includeHeader")
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) {
		t.Fatalf("provider changed table config to a header-only empty result: %v", err)
	}

	headerData := selectorRuleForTest(map[string]any{
		"action": "extractTable", "name": "table",
		"target": map[string]any{"selector": "#header-data"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(headerData); err != nil {
		t.Fatalf("default header-skipping table rejected a non-empty data row: %v", err)
	}
	explicitHeaders := selectorRuleForTest(map[string]any{
		"action": "extractTable", "name": "table",
		"headers": map[string]any{"Name": "name"},
		"target":  map[string]any{"selector": "#header-only"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(explicitHeaders); err != nil {
		t.Fatalf("explicit headers should treat a no-tbody row as data under runtime semantics: %v", err)
	}
}

func TestSelectorlessPageInfoUsesDeterministicEmptyCatalog(t *testing.T) {
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test",
		selectorTextForTest("selectorless semantic snapshot"),
	))
	rule := &models.Rule{Steps: models.JSON(`[
		{"action":"extractPageInfo","name":"page","fields":{"url":{"type":"url"}}}
	]`)}
	firstPrompt, firstProvider, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatal(err)
	}
	secondPrompt, secondProvider, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(firstPrompt), &scope); err != nil {
		t.Fatal(err)
	}
	if scope.CatalogHash == "" || len(scope.Candidates) != 0 || firstPrompt != secondPrompt {
		t.Fatalf("selectorless catalog was not empty, hashed, and deterministic: %s / %s", firstPrompt, secondPrompt)
	}
	if string(firstProvider.Steps) != string(secondProvider.Steps) {
		t.Fatalf("selectorless provider rule was nondeterministic: %s / %s", firstProvider.Steps, secondProvider.Steps)
	}
	report, err := catalog.ResolveProviderExtractionCandidates(firstProvider, scope.CatalogHash)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets != 0 {
		t.Fatalf("selectorless page-info unexpectedly resolved a target: %+v", report)
	}
	targetedPageInfo := selectorRuleForTest(map[string]any{
		"action": "extractPageInfo", "name": "page",
		"target": map[string]any{"selector": "main"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(targetedPageInfo); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("trusted targeted extractPageInfo was not rejected before provider work: %v", err)
	}
	targeted := selectorRuleForTest(map[string]any{
		"action": "extractHtml", "name": "html", "target": map[string]any{"selector": "#missing"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(targeted); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("target-based extraction without any candidate did not fail its source contract: %v", err)
	}
}

func TestTrustedFieldChainsArePinnedBeyondOptionalDiscoveryCaps(t *testing.T) {
	rows := make([]map[string]any, 0, 2)
	for row := 0; row < 2; row++ {
		children := make([]map[string]any, 0, 60)
		for field := 0; field < 60; field++ {
			children = append(children, selectorElementForTest(
				"span",
				map[string]string{"class": fmt.Sprintf("field-%02d", field)},
				selectorTextForTest(fmt.Sprintf("r%d-f%d", row, field)),
			))
		}
		rows = append(rows, selectorElementForTest("article", map[string]string{"class": "row"}, children...))
	}
	root := selectorElementForTest("main", map[string]string{"id": "items"}, rows...)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/items", root))
	fields := map[string]any{}
	for field := 0; field < 60; field++ {
		fields[fmt.Sprintf("field_%02d", field)] = map[string]any{
			"type": "text", "selector": fmt.Sprintf(".field-%02d", field),
		}
	}
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": fields,
	})
	prompt, provider, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	providerFields := selectorRuleStepForTest(t, provider)["fields"].(map[string]any)
	if len(providerFields) != 60 {
		t.Fatalf("trusted fields beyond optional cap were dropped: %d", len(providerFields))
	}
	for name, raw := range providerFields {
		if raw.(map[string]any)["fieldCandidateId"] == "" {
			t.Fatalf("trusted late field %s was not pinned: %#v", name, raw)
		}
	}
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedDeepAndSelfFieldChainsArePinned(t *testing.T) {
	deepRow := func(text string) map[string]any {
		return selectorElementForTest("article", map[string]string{"class": "row"},
			selectorElementForTest("div", map[string]string{"class": "l1"},
				selectorElementForTest("div", map[string]string{"class": "l2"},
					selectorElementForTest("div", map[string]string{"class": "l3"},
						selectorElementForTest("div", map[string]string{"class": "l4"},
							selectorElementForTest("span", map[string]string{"class": "l5"}, selectorTextForTest(text)),
						),
					),
				),
			),
		)
	}
	root := selectorElementForTest("main", map[string]string{"id": "items"}, deepRow("one"), deepRow("two"))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/items", root))
	deep := map[string]any{"type": "text", "selector": ".l5"}
	for level := 4; level >= 1; level-- {
		deep = map[string]any{
			"type": "exists", "selector": fmt.Sprintf(".l%d", level),
			"fields": map[string]any{fmt.Sprintf("l%d", level+1): deep},
		}
	}
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": map[string]any{
			"self": map[string]any{
				"type":   "exists",
				"fields": map[string]any{"l1": deep},
			},
		},
	})
	prompt, provider, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatal(err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	step := selectorRuleStepForTest(t, provider)
	field := step["fields"].(map[string]any)["self"].(map[string]any)
	depth := 0
	for {
		if field["fieldCandidateId"] == "" {
			t.Fatalf("trusted field at depth %d was not pinned: %#v", depth, field)
		}
		depth++
		nested, ok := field["fields"].(map[string]any)
		if !ok {
			break
		}
		for _, raw := range nested {
			field = raw.(map[string]any)
			break
		}
	}
	if depth < 6 {
		t.Fatalf("expected self plus depth>3 chain, got %d", depth)
	}
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); err != nil {
		t.Fatal(err)
	}
}

func TestSelectorCandidateMultiHookPromptIsDeterministic(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("span", map[string]string{"id": "before"}, selectorTextForTest("Before")),
		selectorElementForTest("span", map[string]string{"id": "after"}, selectorTextForTest("After")),
		selectorElementForTest("span", map[string]string{"id": "error"}, selectorTextForTest("Error")),
		selectorElementForTest("span", map[string]string{"id": "cleanup"}, selectorTextForTest("Cleanup")),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	first := &models.Rule{Steps: models.JSON(`[]`), Hooks: models.JSON(`{
		"cleanup":[{"action":"extractText","name":"cleanup","target":{"selector":"#cleanup"}}],
		"beforeAll":[{"action":"extractText","name":"before","target":{"selector":"#before"}}],
		"onError":[{"action":"extractText","name":"error","target":{"selector":"#error"}}],
		"afterAll":[{"action":"extractText","name":"after","target":{"selector":"#after"}}]
	}`)}
	second := &models.Rule{Steps: models.JSON(`[]`), Hooks: models.JSON(`{
		"afterAll":[{"action":"extractText","name":"after","target":{"selector":"#after"}}],
		"onError":[{"action":"extractText","name":"error","target":{"selector":"#error"}}],
		"beforeAll":[{"action":"extractText","name":"before","target":{"selector":"#before"}}],
		"cleanup":[{"action":"extractText","name":"cleanup","target":{"selector":"#cleanup"}}]
	}`)}
	firstPrompt, firstProvider, err := catalog.PrepareProviderPrompt(first)
	if err != nil {
		t.Fatal(err)
	}
	secondPrompt, secondProvider, err := catalog.PrepareProviderPrompt(second)
	if err != nil {
		t.Fatal(err)
	}
	thirdPrompt, thirdProvider, err := catalog.PrepareProviderPrompt(first)
	if err != nil {
		t.Fatal(err)
	}
	if firstPrompt != secondPrompt || firstPrompt != thirdPrompt ||
		string(firstProvider.Hooks) != string(secondProvider.Hooks) ||
		string(firstProvider.Hooks) != string(thirdProvider.Hooks) {
		t.Fatalf(
			"multi-hook prompt/provider IR was nondeterministic:\n%s\n%s\n%s\n%s\n%s\n%s",
			firstPrompt, secondPrompt, thirdPrompt,
			firstProvider.Hooks, secondProvider.Hooks, thirdProvider.Hooks,
		)
	}
}

func TestOpaqueProviderFieldsUseExactShapesAndInjectVisibleRuntimeGuards(t *testing.T) {
	row := func(title string) map[string]any {
		return selectorElementForTest("article", map[string]string{"class": "row"},
			selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest(title)),
			selectorElementForTest("div", map[string]string{"class": "scope"},
				selectorElementForTest("span", map[string]string{"class": "value"}, selectorTextForTest("value")),
			),
		)
	}
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/items",
		selectorElementForTest("main", map[string]string{"id": "items"}, row("one"), row("two")),
	))
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": map[string]any{
			"title": map[string]any{
				"type": "text", "selector": ".title", "default": "legacy fallback",
				"name": "ignored legacy name", "path": "ignored.for.text",
			},
			"scope": map[string]any{
				"type": "html", "selector": ".scope", "default": map[string]any{"value": "fallback"},
				"regex": "ignored", "trim": false,
				"fields": map[string]any{
					"value": map[string]any{
						"type": "text", "selector": ".value", "default": "legacy fallback",
					},
				},
			},
		},
	})
	trustedBefore := append([]byte(nil), trusted.Steps...)
	prompt, provider, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if string(trusted.Steps) != string(trustedBefore) {
		t.Fatal("provider canonicalization mutated the trusted legacy rule")
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	providerFields := selectorRuleStepForTest(t, provider)["fields"].(map[string]any)
	title := providerFields["title"].(map[string]any)
	if len(title) != 2 || title["type"] != "text" || title["fieldCandidateId"] == "" {
		t.Fatalf("trusted leaf was not narrowed to its exact provider shape: %#v", title)
	}
	structural := providerFields["scope"].(map[string]any)
	if len(structural) != 3 || structural["type"] != "exists" ||
		structural["fieldCandidateId"] == "" || structural["fields"] == nil {
		t.Fatalf("trusted structural field was not narrowed to its exact provider shape: %#v", structural)
	}

	for _, modifier := range []string{
		"default", "name", "condition", "required", "transform", "regex",
		"trim", "attr", "path", "resolve", "cssProperty", "visible",
	} {
		t.Run("structural_"+modifier, func(t *testing.T) {
			_, candidate, prepareErr := catalog.PrepareProviderPrompt(trusted)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			mutateSelectorCandidateRuleStep(t, candidate, func(step map[string]any) {
				field := step["fields"].(map[string]any)["scope"].(map[string]any)
				switch modifier {
				case "condition":
					field[modifier] = map[string]any{"type": "valueEquals", "value": "ready"}
				case "required", "resolve", "visible":
					field[modifier] = true
				default:
					field[modifier] = "ignored"
				}
			})
			if _, resolveErr := catalog.ResolveProviderExtractionCandidates(candidate, scope.CatalogHash); !errors.Is(resolveErr, ErrInvalidProvisionalRule) {
				t.Fatalf("structural provider modifier %q was accepted: %v", modifier, resolveErr)
			}
		})
	}
	t.Run("structural_scalar_type", func(t *testing.T) {
		_, candidate, prepareErr := catalog.PrepareProviderPrompt(trusted)
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		mutateSelectorCandidateRuleStep(t, candidate, func(step map[string]any) {
			step["fields"].(map[string]any)["scope"].(map[string]any)["type"] = "html"
		})
		if _, resolveErr := catalog.ResolveProviderExtractionCandidates(candidate, scope.CatalogHash); !errors.Is(resolveErr, ErrInvalidProvisionalRule) ||
			!strings.Contains(resolveErr.Error(), `must use type "exists"`) {
			t.Fatalf("structural scalar type was not rejected precisely: %v", resolveErr)
		}
	})

	for _, modifier := range []string{
		"default", "name", "condition", "required", "transform", "attr",
		"path", "resolve", "cssProperty", "fields", "visible",
	} {
		t.Run("text_leaf_"+modifier, func(t *testing.T) {
			_, candidate, prepareErr := catalog.PrepareProviderPrompt(trusted)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			mutateSelectorCandidateRuleStep(t, candidate, func(step map[string]any) {
				field := step["fields"].(map[string]any)["title"].(map[string]any)
				switch modifier {
				case "condition":
					field[modifier] = map[string]any{"type": "valueEquals", "value": "ready"}
				case "required", "resolve", "visible":
					field[modifier] = true
				case "fields":
					field[modifier] = map[string]any{
						"nested": map[string]any{"type": "text", "fieldCandidateId": field["fieldCandidateId"]},
					}
				default:
					field[modifier] = "ignored"
				}
			})
			if _, resolveErr := catalog.ResolveProviderExtractionCandidates(candidate, scope.CatalogHash); !errors.Is(resolveErr, ErrInvalidProvisionalRule) {
				t.Fatalf("irrelevant/default text provider modifier %q was accepted: %v", modifier, resolveErr)
			}
		})
	}

	if _, err := catalog.ResolveProviderExtractionCandidates(provider, scope.CatalogHash); err != nil {
		t.Fatalf("exact provider shape failed resolution: %v", err)
	}
	resolvedFields := selectorRuleStepForTest(t, provider)["fields"].(map[string]any)
	var assertVisible func(map[string]any)
	assertVisible = func(fields map[string]any) {
		for name, raw := range fields {
			field := raw.(map[string]any)
			if field["visible"] != true {
				t.Fatalf("resolved provider field %s lacks visible runtime guard: %#v", name, field)
			}
			if _, hasDefault := field["default"]; hasDefault {
				t.Fatalf("resolved provider field %s retained a default: %#v", name, field)
			}
			if nested, ok := field["fields"].(map[string]any); ok {
				assertVisible(nested)
			}
		}
	}
	assertVisible(resolvedFields)
}

func TestSelectorEvidenceRejectsExplicitNonRenderedSourcesAndAncestors(t *testing.T) {
	cases := map[string]map[string]any{}
	recordedFalse := selectorElementForTest("span", map[string]string{"id": "target"}, selectorTextForTest("hidden"))
	recordedFalse["rendered"] = false
	cases["recorded_marker"] = selectorElementForTest("main", nil, recordedFalse)
	cases["hidden_ancestor"] = selectorElementForTest("main", nil,
		selectorElementForTest("section", map[string]string{"hidden": ""},
			selectorElementForTest("span", map[string]string{"id": "target"}, selectorTextForTest("hidden")),
		),
	)
	cases["aria_hidden_ancestor"] = selectorElementForTest("main", nil,
		selectorElementForTest("section", map[string]string{"aria-hidden": "true"},
			selectorElementForTest("span", map[string]string{"id": "target"}, selectorTextForTest("hidden")),
		),
	)
	cases["skipped_tag"] = selectorElementForTest("main", nil,
		selectorElementForTest("script", map[string]string{"id": "target"}, selectorTextForTest("hidden")),
	)
	for name, root := range cases {
		t.Run(name, func(t *testing.T) {
			catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
			rule := selectorRuleForTest(map[string]any{
				"action": "extractText", "name": "value",
				"target": map[string]any{"selector": "#target"},
			})
			if _, _, err := catalog.PrepareProviderPrompt(rule); !errors.Is(err, ErrSelectorSourceUnavailable) ||
				!strings.Contains(err.Error(), "non-rendered") {
				t.Fatalf("explicit non-rendered source was not rejected before provider work: %v", err)
			}
		})
	}

	row := func(title string) map[string]any {
		ghost := selectorElementForTest("div", map[string]string{"class": "ghost"},
			selectorElementForTest("span", map[string]string{"class": "value"}, selectorTextForTest("hidden")),
		)
		ghost["rendered"] = false
		return selectorElementForTest("article", map[string]string{"class": "row"},
			selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest(title)),
			ghost,
		)
	}
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/items",
		selectorElementForTest("main", map[string]string{"id": "items"}, row("one"), row("two")),
	))
	safe := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": ".title"},
		},
	})
	prompt, provider, err := catalog.PrepareProviderPrompt(safe)
	if err != nil {
		t.Fatal(err)
	}
	var promptScope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &promptScope); err != nil {
		t.Fatal(err)
	}
	ghostFieldID := ""
	for id, evidence := range catalog.promptFields {
		if evidence.selector == ".ghost" {
			ghostFieldID = id
			break
		}
	}
	if ghostFieldID == "" {
		t.Fatal("non-rendered optional field was not represented for capability rejection")
	}
	mutateSelectorCandidateRuleStep(t, provider, func(step map[string]any) {
		field := step["fields"].(map[string]any)["title"].(map[string]any)
		field["fieldCandidateId"] = ghostFieldID
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, promptScope.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "non-rendered") {
		t.Fatalf("provider selected an explicitly non-rendered field: %v", err)
	}
}

func TestSelectorEvidenceRepeatedVisibleExtractionIgnoresExplicitlyHiddenRows(t *testing.T) {
	row := func(title string, rendered bool) map[string]any {
		value := selectorElementForTest("article", map[string]string{"class": "row"},
			selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest(title)),
		)
		if !rendered {
			value["rendered"] = false
		}
		return value
	}
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/items",
		selectorElementForTest("main", map[string]string{"id": "items"},
			row("one", true), row("hidden", false), row("two", true),
		),
	))
	v4Prompt, _, err := catalog.prepareProviderPrompt(nil, selectorPromptCatalogVersionV4)
	if err != nil {
		t.Fatal(err)
	}
	rebuiltV4, _, err := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/items",
		selectorElementForTest("main", map[string]string{"id": "items"},
			row("one", true), row("hidden", false), row("two", true),
		),
	)).ReconstructProviderPrompt(v4Prompt, nil)
	if err != nil || rebuiltV4 != v4Prompt {
		t.Fatalf("stored v4 mixed-rendering catalog changed: err=%v\noriginal=%s\nrebuilt=%s", err, v4Prompt, rebuiltV4)
	}
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": ".title"},
		},
	})
	promptJSON, provider, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatal(err)
	}
	var prompt SelectorPromptCatalog
	if err := json.Unmarshal([]byte(promptJSON), &prompt); err != nil {
		t.Fatal(err)
	}
	step := selectorRuleStepForTest(t, provider)
	rowID := step["target"].(map[string]any)["rowCandidateId"].(string)
	var selected *SelectorPromptTargetCandidate
	for index := range prompt.Candidates {
		if prompt.Candidates[index].RowCandidateID == rowID {
			selected = &prompt.Candidates[index]
			break
		}
	}
	if selected == nil || !reflect.DeepEqual(selected.Cardinalities, []int{2}) {
		t.Fatalf("rendered repeated-row cardinality = %#v, want [2]", selected)
	}
	fieldID := step["fields"].(map[string]any)["title"].(map[string]any)["fieldCandidateId"].(string)
	var field *SelectorPromptFieldCandidate
	for index := range selected.FieldCandidates {
		if selected.FieldCandidates[index].FieldCandidateID == fieldID {
			field = &selected.FieldCandidates[index]
			break
		}
	}
	if field == nil || !containsString(field.SupportedTypes, "text") {
		t.Fatalf("visible title field was not text-capable: %#v", field)
	}
	if _, err := catalog.ResolveProviderExtractionCandidates(provider, prompt.CatalogHash); err != nil {
		t.Fatalf("visible repeated extraction did not resolve: %v", err)
	}
	resolved := selectorRuleStepForTest(t, provider)
	if resolved["target"].(map[string]any)["selector"] != "#items > .row" ||
		resolved["fields"].(map[string]any)["title"].(map[string]any)["visible"] != true {
		t.Fatalf("resolved repeated extraction lost its trusted selector or field visibility guard: %#v", resolved)
	}
}

func TestSanitizerRemovalMarkersCannotAuthorizeLiveExtraction(t *testing.T) {
	root := selectorElementForTest("main", nil,
		selectorElementForTest("a", map[string]string{
			"id": "removed-link", "href": "[REMOVED_URL]",
		}, selectorTextForTest("safe label")),
		selectorElementForTest("div", map[string]string{"id": "removed-text"},
			selectorTextForTest("prefix [REMOVED_URL] suffix"),
		),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	for _, step := range []map[string]any{
		{
			"action": "extractAttribute", "name": "href", "attr": "href",
			"target": map[string]any{"selector": "#removed-link"},
		},
		{
			"action": "extractHtml", "name": "html",
			"target": map[string]any{"selector": "#removed-link"},
		},
		{
			"action": "extractText", "name": "text",
			"target": map[string]any{"selector": "#removed-text"},
		},
	} {
		t.Run(step["action"].(string), func(t *testing.T) {
			if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(err, ErrSelectorSourceUnavailable) {
				t.Fatalf("sanitizer removal marker authorized live extraction: %v", err)
			}
		})
	}
	for _, marker := range []string{"[REDACTED]", "<redacted>", "[REMOVED_URL]"} {
		if !containsExplicitRedaction(marker) {
			t.Fatalf("canonical sanitizer marker %q was not classified as sensitive", marker)
		}
	}
}

func TestHiddenDescendantsCannotAuthorizeParentContentExtraction(t *testing.T) {
	hiddenChild := selectorElementForTest(
		"span",
		map[string]string{"class": "fallback"},
		selectorTextForTest("hidden fallback"),
	)
	hiddenChild["rendered"] = false
	target := selectorElementForTest(
		"section",
		map[string]string{"id": "target"},
		selectorTextForTest("visible"),
		hiddenChild,
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/items",
		selectorElementForTest("main", nil, target),
	))

	for _, step := range []map[string]any{
		{
			"action": "extractText", "name": "value",
			"target": map[string]any{"selector": "#target"},
		},
		{
			"action": "extractHtml", "name": "value",
			"target": map[string]any{"selector": "#target"},
		},
		{
			"action": "extractJson", "name": "value",
			"target": map[string]any{"selector": "#target"},
		},
		{
			"action": "extract", "name": "value",
			"target": map[string]any{"selector": "#target"},
			"fields": map[string]any{
				"value": map[string]any{"type": "text"},
			},
		},
	} {
		t.Run(step["action"].(string), func(t *testing.T) {
			if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(err, ErrSelectorSourceUnavailable) ||
				!strings.Contains(err.Error(), "non-rendered descendant") {
				t.Fatalf("visible parent authorized hidden descendant content: %v", err)
			}
		})
	}

	for _, fieldType := range []string{"text", "number", "regex", "json", "count", "html"} {
		t.Run("field_"+fieldType, func(t *testing.T) {
			field := map[string]any{"type": fieldType}
			if fieldType == "regex" {
				field["regex"] = "visible"
			}
			step := map[string]any{
				"action": "extract", "name": "value",
				"target": map[string]any{"selector": "#target"},
				"fields": map[string]any{"value": field},
			}
			if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(err, ErrSelectorSourceUnavailable) ||
				!strings.Contains(err.Error(), "non-rendered descendant") {
				t.Fatalf("%s field authorized hidden descendant content: %v", fieldType, err)
			}
		})
	}

	hiddenCell := selectorElementForTest("td", nil, selectorTextForTest("hidden cell"))
	hiddenCell["rendered"] = false
	tableCatalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/table",
		selectorElementForTest("table", map[string]string{"id": "table"},
			selectorElementForTest("tr", nil,
				selectorElementForTest("td", nil, selectorTextForTest("visible cell")),
				hiddenCell,
			),
		),
	))
	tableRule := selectorRuleForTest(map[string]any{
		"action": "extractTable", "name": "rows", "includeHeader": true,
		"target": map[string]any{"selector": "#table"},
	})
	if _, _, err := tableCatalog.PrepareProviderPrompt(tableRule); !errors.Is(err, ErrSelectorSourceUnavailable) ||
		!strings.Contains(err.Error(), "non-rendered") {
		t.Fatalf("table extraction authorized a hidden descendant cell: %v", err)
	}
}

func TestSanitizationProvenanceConstrainsOnlyConsumedProviderModes(t *testing.T) {
	row := func() map[string]any {
		result := selectorElementForTest(
			"article",
			map[string]string{"class": "row"},
			selectorElementForTest(
				"span",
				map[string]string{"class": "safe", "data-code": "ABC"},
				selectorTextForTest("42"),
			),
		)
		result["sanitization"] = map[string]any{
			"markupAltered": true, "contentOmitted": true,
		}
		return result
	}
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/items",
		selectorElementForTest(
			"main",
			map[string]string{"id": "items"},
			row(),
			row(),
		),
	))
	for _, action := range []string{"extractText", "extractJson"} {
		step := map[string]any{
			"action": action, "name": "value",
			"target": map[string]any{"selector": "#items"},
		}
		if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(err, ErrSelectorSourceUnavailable) ||
			!strings.Contains(err.Error(), "sanitization provenance") {
			t.Fatalf("standalone %s consumed provenance-altered subtree: %v", action, err)
		}
	}
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .row"},
		"fields": map[string]any{
			"safe": map[string]any{"type": "text", "selector": ".safe"},
		},
	})
	prompt, _, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatalf("unrelated row provenance poisoned independently selected safe leaf: %v", err)
	}
	var scope SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &scope); err != nil {
		t.Fatal(err)
	}
	rowTargetID := ""
	for id, evidence := range catalog.promptTargets {
		if evidence.selector == "#items > .row" {
			rowTargetID = id
			break
		}
	}
	if rowTargetID == "" {
		t.Fatal("row target evidence was not retained")
	}
	selfID := ""
	for id, evidence := range catalog.promptFields {
		if evidence.targetID == rowTargetID &&
			evidence.prompt.ParentFieldCandidateID == "" &&
			evidence.selector == "" {
			selfID = id
			if containsString(evidence.prompt.SupportedTypes, "text") ||
				containsString(evidence.prompt.SupportedTypes, "count") ||
				containsString(evidence.prompt.SupportedTypes, "html") {
				t.Fatalf("content-consuming types were advertised for omitted evidence: %#v", evidence.prompt)
			}
			break
		}
	}
	if selfID == "" {
		t.Fatal("row-self evidence candidate was not retained for fail-closed validation")
	}

	for _, test := range []struct {
		fieldType string
		extras    map[string]any
	}{
		{fieldType: "text"},
		{fieldType: "number"},
		{fieldType: "json"},
		{fieldType: "regex", extras: map[string]any{"regex": `[0-9]+`}},
		{fieldType: "count"},
		{fieldType: "html"},
	} {
		t.Run(test.fieldType, func(t *testing.T) {
			_, candidate, prepareErr := catalog.PrepareProviderPrompt(trusted)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			mutateSelectorCandidateRuleStep(t, candidate, func(step map[string]any) {
				field := step["fields"].(map[string]any)["safe"].(map[string]any)
				for key := range field {
					delete(field, key)
				}
				field["type"] = test.fieldType
				field["fieldCandidateId"] = selfID
				for key, value := range test.extras {
					field[key] = value
				}
			})
			if _, resolveErr := catalog.ResolveProviderExtractionCandidates(candidate, scope.CatalogHash); !errors.Is(resolveErr, ErrInvalidProvisionalRule) ||
				(!strings.Contains(resolveErr.Error(), "sanitization provenance") &&
					!strings.Contains(resolveErr.Error(), "sanitized-markup provenance")) {
				t.Fatalf("%s consumed provenance-altered row content: %v", test.fieldType, resolveErr)
			}
		})
	}

	_, existenceRule, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, existenceRule, func(step map[string]any) {
		field := step["fields"].(map[string]any)["safe"].(map[string]any)
		for key := range field {
			delete(field, key)
		}
		field["type"] = "exists"
		field["fieldCandidateId"] = selfID
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(existenceRule, scope.CatalogHash); err != nil {
		t.Fatalf("existence-only field was poisoned by unrelated content provenance: %v", err)
	}

	table := selectorElementForTest(
		"table",
		map[string]string{"id": "table"},
		selectorElementForTest(
			"tbody",
			nil,
			selectorElementForTest(
				"tr",
				nil,
				selectorElementForTest("td", nil, selectorTextForTest("safe")),
			),
		),
	)
	table["sanitization"] = map[string]any{
		"markupAltered": true, "contentOmitted": true,
	}
	tableCatalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/table",
		selectorElementForTest("main", nil, table),
	))
	tableRule := selectorRuleForTest(map[string]any{
		"action": "extractTable", "name": "rows",
		"target":  map[string]any{"selector": "#table"},
		"headers": map[string]any{"value": "value"}, "includeHeader": true,
	})
	if _, _, err := tableCatalog.PrepareProviderPrompt(tableRule); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("table extraction consumed provenance-altered cells: %v", err)
	}
}

func TestMarkupAndAttributeProvenanceAreModeSpecific(t *testing.T) {
	target := selectorElementForTest(
		"section",
		map[string]string{"id": "target", "class": "stable", "data-code": "ABC"},
		selectorTextForTest("safe"),
	)
	target["sanitization"] = map[string]any{
		"markupAltered": true, "alteredAttributes": []any{"class"},
	}
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0,
		"https://example.test/items",
		selectorElementForTest("main", nil, target),
	))

	allowed := []map[string]any{
		{
			"action": "extractText", "name": "value",
			"target": map[string]any{"selector": "#target"},
		},
		{
			"action": "extractAttribute", "name": "value", "attr": "data-code",
			"target": map[string]any{"selector": "#target"},
		},
	}
	for _, step := range allowed {
		if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); err != nil {
			t.Fatalf("unaffected mode %s was rejected by markup-only provenance: %v", step["action"], err)
		}
	}
	rejected := []map[string]any{
		{
			"action": "extractHtml", "name": "value",
			"target": map[string]any{"selector": "#target"},
		},
		{
			"action": "extractAttribute", "name": "value", "attr": "class",
			"target": map[string]any{"selector": "#target"},
		},
	}
	for _, step := range rejected {
		if _, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(err, ErrSelectorSourceUnavailable) {
			t.Fatalf("altered mode %s was authorized: %v", step["action"], err)
		}
	}
}

func TestLegacySanitizationVersionCannotProveContentClean(t *testing.T) {
	recording := map[string]any{
		"meta": map[string]any{"sanitizationVersion": "extension-v1"},
		"snapshots": []any{selectorSnapshotForTest(
			0,
			"https://example.test/items",
			selectorElementForTest(
				"main",
				nil,
				selectorElementForTest(
					"section",
					map[string]string{"id": "target", "class": "stable", "data-code": "ABC"},
					selectorTextForTest("safe"),
				),
			),
		)},
	}
	catalog, err := BuildSelectorEvidenceCatalog(recording)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []map[string]any{
		{
			"action": "extractText", "name": "value",
			"target": map[string]any{"selector": "#target"},
		},
		{
			"action": "extractHtml", "name": "value",
			"target": map[string]any{"selector": "#target"},
		},
		{
			"action": "extractAttribute", "name": "value", "attr": "class",
			"target": map[string]any{"selector": "#target"},
		},
	} {
		if _, _, prepareErr := catalog.PrepareProviderPrompt(selectorRuleForTest(step)); !errors.Is(prepareErr, ErrSelectorSourceUnavailable) {
			t.Fatalf("legacy provenance authorized %s content: %v", step["action"], prepareErr)
		}
	}
	safeAttr := selectorRuleForTest(map[string]any{
		"action": "extractAttribute", "name": "value", "attr": "data-code",
		"target": map[string]any{"selector": "#target"},
	})
	if _, _, err := catalog.PrepareProviderPrompt(safeAttr); err != nil {
		t.Fatalf("legacy provenance rejected unchanged non-class attribute: %v", err)
	}
}

func TestRecordedPrivateEvidenceAttributesCannotBeForgedOrOverridden(t *testing.T) {
	legacyRoot := selectorElementForTest(
		"section",
		map[string]string{
			"id":                                 "target",
			recordedSanitizationUnknownAttribute: "false",
			recordedSanitizedContentAttribute:    "false",
			recordedSanitizedMarkupAttribute:     "false",
			recordedSanitizedAttrsAttribute:      "data-code",
			recordedRenderedAttribute:            "true",
		},
		selectorTextForTest("safe"),
	)
	legacyCatalog, err := BuildSelectorEvidenceCatalog(map[string]any{
		"meta": map[string]any{"sanitizationVersion": "extension-v1"},
		"snapshots": []any{
			selectorSnapshotForTest(0, "https://example.test/items", legacyRoot),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	textRule := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "value",
		"target": map[string]any{"selector": "#target"},
	})
	if _, _, err := legacyCatalog.PrepareProviderPrompt(textRule); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("page-authored private attributes overrode legacy fail-closed provenance: %v", err)
	}

	modernRoot := selectorElementForTest(
		"section",
		map[string]string{
			"id":                              "target",
			"class":                           "stable",
			"data-code":                       "ABC",
			recordedSanitizedAttrsAttribute:   "data-code",
			recordedSanitizedContentAttribute: "false",
		},
		selectorTextForTest("safe"),
	)
	modernRoot["sanitization"] = map[string]any{
		"markupAltered": true, "alteredAttributes": []any{"class"},
	}
	modernCatalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0, "https://example.test/items", modernRoot,
	))
	classRule := selectorRuleForTest(map[string]any{
		"action": "extractAttribute", "name": "value", "attr": "class",
		"target": map[string]any{"selector": "#target"},
	})
	if _, _, err := modernCatalog.PrepareProviderPrompt(classRule); !errors.Is(err, ErrSelectorSourceUnavailable) {
		t.Fatalf("page-authored private attribute replaced server-derived alteredAttributes: %v", err)
	}
	dataRule := selectorRuleForTest(map[string]any{
		"action": "extractAttribute", "name": "value", "attr": "data-code",
		"target": map[string]any{"selector": "#target"},
	})
	if _, _, err := modernCatalog.PrepareProviderPrompt(dataRule); err != nil {
		t.Fatalf("page-authored private attribute poisoned unrelated exact attribute evidence: %v", err)
	}
}

func TestMalformedV2SubtreesAndIframeFallbackFailClosedBeforeProvider(t *testing.T) {
	nullEvidence := selectorElementForTest(
		"section",
		map[string]string{"id": "target"},
		selectorTextForTest("safe"),
	)
	nullEvidence["sanitization"] = nil

	invalidChild := selectorElementForTest(
		"section",
		map[string]string{"id": "target"},
	)
	invalidChild["children"] = []any{"untyped child content"}

	renderedText := selectorElementForTest(
		"section",
		map[string]string{"id": "target"},
	)
	renderedText["children"] = []any{
		map[string]any{"type": "text", "text": "hidden text", "rendered": false},
	}

	iframeFallback := selectorElementForTest(
		"iframe",
		map[string]string{"id": "target"},
	)
	iframeFallback["children"] = []any{
		map[string]any{"type": "text", "text": "fallback secret"},
	}

	for name, root := range map[string]map[string]any{
		"explicit_null_provenance": nullEvidence,
		"invalid_child":            invalidChild,
		"text_rendered_marker":     renderedText,
		"iframe_fallback":          iframeFallback,
	} {
		t.Run(name, func(t *testing.T) {
			catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
				0, "https://example.test/items", root,
			))
			for _, action := range []string{"extractText", "extractHtml"} {
				rule := selectorRuleForTest(map[string]any{
					"action": action, "name": "value",
					"target": map[string]any{"selector": "#target"},
				})
				if _, _, err := catalog.PrepareProviderPrompt(rule); !errors.Is(err, ErrSelectorSourceUnavailable) {
					t.Fatalf("%s authorized malformed or omitted subtree evidence: %v", action, err)
				}
			}
		})
	}
}

func TestCanonicalJSONArrayIndexFitsJavaScriptAndNativeInt(t *testing.T) {
	const maxJavaScriptSafeInteger = uint64(1<<53 - 1)
	maxAccepted := maxJavaScriptSafeInteger
	maxNativeInt := uint64(^uint(0) >> 1)
	if maxNativeInt < maxAccepted {
		maxAccepted = maxNativeInt
	}
	segment := strconv.FormatUint(maxAccepted, 10)
	index, ok := canonicalJSONArrayIndex(segment)
	if !ok || uint64(index) != maxAccepted {
		t.Fatalf("largest portable index %q was not preserved: %d %v", segment, index, ok)
	}
	overflow := strconv.FormatUint(maxAccepted+1, 10)
	if _, ok := canonicalJSONArrayIndex(overflow); ok {
		t.Fatalf("index outside the JavaScript/native-int intersection was accepted: %s", overflow)
	}
}

func singletonTextCandidateForContractTest(t *testing.T) (*SelectorEvidenceCatalog, *models.Rule) {
	t.Helper()
	root := selectorElementForTest("main", nil,
		selectorElementForTest("span", map[string]string{"id": "result"}, selectorTextForTest("Result")),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	rule := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "result",
		"target": map[string]any{"selector": "#result", "visible": true},
	})
	return catalog, rule
}
