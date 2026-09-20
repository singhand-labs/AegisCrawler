package rule

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestSelectorRepairPlanIsSourceBoundAndEvidenceScoped(t *testing.T) {
	primary := selectorResultsForTest("primary", []selectorCardForTest{
		{classes: "card primary", title: true},
		{classes: "card primary", title: true},
	})
	decoys := selectorResultsForTest("decoys", []selectorCardForTest{
		{classes: "decoy", title: true},
		{classes: "decoy", title: true},
	})
	root := selectorElementForTest("html", nil,
		selectorElementForTest("body", nil, primary, decoys),
	)
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", root),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#primary > .card"},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": "h2.title"},
		},
	})
	prompt, providerRule, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &promptCatalog); err != nil {
		t.Fatal(err)
	}
	if promptCatalog.Version != selectorPromptCatalogVersion {
		t.Fatalf("repair source catalog did not retain v2 aliases: %q", promptCatalog.Version)
	}
	var correctRow, wrongRow string
	for _, candidate := range promptCatalog.Candidates {
		switch candidate.ObservedSelector {
		case "#primary > .card":
			correctRow = candidate.RowCandidateID
		case "#decoys > .decoy":
			wrongRow = candidate.RowCandidateID
		}
	}
	if correctRow == "" || wrongRow == "" || correctRow == wrongRow {
		t.Fatalf("test catalog did not expose distinct row cohorts: %s", prompt)
	}
	step := selectorRuleStepForTest(t, providerRule)
	step["target"].(map[string]any)["rowCandidateId"] = wrongRow
	encoded, _ := json.Marshal([]any{step})
	providerRule.Steps = encoded

	_, resolutionErr := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash)
	var selection *SelectorCandidateSelectionError
	if !errors.As(resolutionErr, &selection) || selection.Code != "candidate_scope" {
		t.Fatalf("wrong cohort was not a typed eligible selection failure: %T %v", resolutionErr, resolutionErr)
	}
	first, err := catalog.BuildSelectorRepairPlan(providerRule, selection, "report-a\x00ir-hash")
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.BuildSelectorRepairPlan(providerRule, selection, "report-b\x00ir-hash")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Slots) == 0 || len(first.Slots) != len(second.Slots) {
		t.Fatalf("unexpected plans: first=%+v second=%+v", first, second)
	}
	for index := range first.Slots {
		if first.Slots[index].ID == second.Slots[index].ID {
			t.Fatalf("slot ID was not source-attempt-bound: %+v", first.Slots[index])
		}
		for _, candidateID := range first.Slots[index].AllowedCandidateIDs {
			switch first.Slots[index].Kind {
			case "rowCandidateId":
				if !strings.HasPrefix(candidateID, "r_") {
					t.Fatalf("repair plan lost v2 row alias: %+v", first.Slots[index])
				}
			case "targetCandidateId":
				if !strings.HasPrefix(candidateID, "t_") {
					t.Fatalf("repair plan lost v2 target alias: %+v", first.Slots[index])
				}
			case "fieldCandidateId":
				if !strings.HasPrefix(candidateID, "f_") {
					t.Fatalf("repair plan lost v2 field alias: %+v", first.Slots[index])
				}
			}
		}
		if first.Slots[index].Kind == "rowCandidateId" {
			if !slices.Contains(first.Slots[index].AllowedCandidateIDs, correctRow) ||
				!slices.Contains(first.Slots[index].AllowedCandidateIDs, wrongRow) {
				t.Fatalf("row slot omitted evidence-valid cohorts: %+v", first.Slots[index])
			}
		}
	}
	for _, assignment := range first.AllowedAssignments {
		if len(assignment) != len(first.Slots) {
			t.Fatalf("incomplete compatible tuple: assignment=%+v slots=%+v", assignment, first.Slots)
		}
	}
}

func TestSelectorRepairPlanFailsWhenTypedSlotHasNoAlternative(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search",
			selectorResultsForTest("only", []selectorCardForTest{
				{classes: "card", title: true},
				{classes: "card", title: true},
			})),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#only > .card"},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": "h2.title"},
		},
	})
	_, providerRule, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	failure := &SelectorCandidateSelectionError{
		Code: "candidate_capability", Slot: "steps[0].target", Detail: "wrong cohort",
	}
	if _, err := catalog.BuildSelectorRepairPlan(providerRule, failure, "source"); err == nil {
		t.Fatal("plan with no different evidence-valid alternative was accepted")
	}
}

func TestSelectorRepairPlanFindsNestedExtractionOwnerAndExcludesOtherActions(t *testing.T) {
	catalog, extraction, _ := selectorRepairWrongCohortFixture(t)
	other := cloneSelectorRepairTestStep(t, extraction)
	tests := []struct {
		name        string
		steps       []any
		hooks       map[string]any
		failureSlot string
		privatePath string
	}{
		{
			name: "nested steps",
			steps: []any{map[string]any{
				"action": "loop", "steps": []any{extraction, other},
			}},
			failureSlot: "steps[0].steps[0].fields.title",
			privatePath: "steps\u0000#0\u0000steps\u0000#0",
		},
		{
			name: "nested case",
			steps: []any{map[string]any{
				"action": "switch",
				"cases":  []any{map[string]any{"steps": []any{extraction, other}}},
			}},
			failureSlot: "steps[0].cases[0].steps[0].fields.title",
			privatePath: "steps\u0000#0\u0000cases\u0000#0\u0000steps\u0000#0",
		},
		{
			name:        "hook",
			steps:       []any{other},
			hooks:       map[string]any{"onError": []any{extraction, other}},
			failureSlot: "hooks.onError[0].fields.title",
			privatePath: "hooks\u0000onError\u0000#0",
		},
		{
			name: "map trigger",
			steps: []any{map[string]any{
				"action": "handleDownload", "trigger": extraction,
			}},
			failureSlot: "steps[0].trigger[0].fields.title",
			privatePath: "steps\u0000#0\u0000trigger",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := selectorRuleForTest(map[string]any{"action": "noop"})
			encodedSteps, _ := json.Marshal(test.steps)
			rule.Steps = encodedSteps
			if test.hooks != nil {
				encodedHooks, _ := json.Marshal(test.hooks)
				rule.Hooks = encodedHooks
			}
			plan, err := catalog.BuildSelectorRepairPlan(rule, &SelectorCandidateSelectionError{
				Code: "candidate_scope", Slot: test.failureSlot, Detail: "wrong cohort",
			}, "source")
			if err != nil {
				t.Fatal(err)
			}
			for _, slot := range plan.Slots {
				path := selectorRepairPathKey(slot.Path)
				if !strings.HasPrefix(path, test.privatePath+"\u0000") {
					t.Fatalf("repair plan escaped the owning extraction action: %q", path)
				}
				if strings.Contains(path, "\u0000#1\u0000") {
					t.Fatalf("repair plan included an unrelated sibling action: %q", path)
				}
				if strings.Contains(path, "\u0000trigger\u0000#0\u0000") {
					t.Fatalf("map-valued trigger received a synthetic private array token: %q", path)
				}
			}
		})
	}
}

func TestSelectorRepairPlanFailsClosedOnAssignmentOverflow(t *testing.T) {
	catalog, extraction, _ := selectorRepairWrongCohortFixture(t)
	target := extraction["target"].(map[string]any)
	targetID := strings.TrimSpace(stringValue(target["rowCandidateId"]))
	evidence := catalog.promptTargets[targetID]
	if evidence == nil || len(evidence.fields) == 0 {
		t.Fatal("fixture has no target field evidence")
	}
	catalog.promptTargets = map[string]*providerTargetEvidence{targetID: evidence}
	template := evidence.fields[0]
	evidence.fields = nil
	fields := map[string]any{}
	for index := 0; index < 12; index++ {
		clone := *template
		clone.prompt = template.prompt
		clone.prompt.FieldCandidateID = fmt.Sprintf("field_%02d", index)
		evidence.fields = append(evidence.fields, &clone)
	}
	for _, name := range []string{"first", "second"} {
		fields[name] = map[string]any{
			"type": "text", "fieldCandidateId": evidence.fields[0].prompt.FieldCandidateID,
		}
	}
	extraction["fields"] = fields
	rule := selectorRuleForTest(extraction)
	_, err := catalog.BuildSelectorRepairPlan(rule, &SelectorCandidateSelectionError{
		Code: "candidate_scope", Slot: "steps[0].fields.first", Detail: "wrong field",
	}, "source")
	if !errors.Is(err, errSelectorRepairAssignmentOverflow) {
		t.Fatalf("combinatorial overflow was not rejected: %v", err)
	}
}

func selectorRepairWrongCohortFixture(
	t *testing.T,
) (*SelectorEvidenceCatalog, map[string]any, *SelectorCandidateSelectionError) {
	t.Helper()
	primary := selectorResultsForTest("primary", []selectorCardForTest{
		{classes: "card primary", title: true},
		{classes: "card primary", title: true},
	})
	decoys := selectorResultsForTest("decoys", []selectorCardForTest{
		{classes: "decoy", title: true},
		{classes: "decoy", title: true},
	})
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(
		0, "https://example.test/search",
		selectorElementForTest("html", nil, selectorElementForTest("body", nil, primary, decoys)),
	))
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#primary > .card"},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": "h2.title"},
		},
	})
	prompt, providerRule, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &promptCatalog); err != nil {
		t.Fatal(err)
	}
	var wrongRow string
	for _, candidate := range promptCatalog.Candidates {
		if candidate.ObservedSelector == "#decoys > .decoy" {
			wrongRow = candidate.RowCandidateID
		}
	}
	step := selectorRuleStepForTest(t, providerRule)
	step["target"].(map[string]any)["rowCandidateId"] = wrongRow
	encoded, _ := json.Marshal([]any{step})
	providerRule.Steps = encoded
	_, resolutionErr := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash)
	var selection *SelectorCandidateSelectionError
	if !errors.As(resolutionErr, &selection) {
		t.Fatalf("fixture did not produce a typed selector failure: %v", resolutionErr)
	}
	return catalog, step, selection
}

func cloneSelectorRepairTestStep(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
