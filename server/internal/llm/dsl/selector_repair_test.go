package dsl

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	platformrule "github.com/singhand-labs/AegisCrawler/internal/rule"
)

func selectorRepairUnitRequest() SelectorRepairJobRequest {
	rule := &models.Rule{
		ID: "rule", Version: "1", Name: "Rule", Entry: "https://example.test",
		Domain: models.JSON(`"example.test"`),
		Steps: models.JSON(`[
			{"action":"extract","name":"rows","multiple":true,
			 "target":{"rowCandidateId":"row_wrong","visible":true},
			 "fields":{"title":{"type":"text","fieldCandidateId":"field_title"}}},
			{"action":"sendResult","payload":{"title":"{{extracted.rows}}"}}
		]`),
	}
	return SelectorRepairJobRequest{
		SourceAttemptReportID: "report-a", SourceProviderIRHash: dslJSONHash(rule),
		SelectorCatalogHash: "catalog-hash",
		SelectorCatalog:     `{"version":"selector-catalog-v2","catalogHash":"catalog-hash","candidates":[]}`,
		ProviderIR:          rule,
		Diagnostic: DSLAttemptValidation{
			Phase: "selector-candidate-resolution", Status: "failed",
			Code: "INVALID_DSL", Message: "wrong cohort",
		},
		OutputContract: json.RawMessage(`{"outputFields":[{"name":"title","type":"string"}]}`),
		Failure: &platformrule.SelectorCandidateSelectionError{
			Code: "candidate_scope", Slot: "steps[0].fields.title", Detail: "wrong cohort",
		},
		Slots: []platformrule.SelectorRepairSlot{
			{
				ID: "slot_target", Kind: "rowCandidateId", CurrentCandidateID: "row_wrong",
				AllowedCandidateIDs: []string{"row_right", "row_wrong"},
				Path:                []string{"steps", "#0", "target", "rowCandidateId"},
			},
			{
				ID: "slot_field", Kind: "fieldCandidateId", CurrentCandidateID: "field_title",
				AllowedCandidateIDs: []string{"field_title"},
				Path:                []string{"steps", "#0", "fields", "title", "fieldCandidateId"},
			},
		},
		AllowedAssignments: []map[string]string{
			{"slot_target": "row_right", "slot_field": "field_title"},
		},
	}
}

func TestSelectorRepairPromptContainsNoRecordingOrPrivatePaths(t *testing.T) {
	request := selectorRepairUnitRequest()
	request.RepairPlanHash = selectorRepairPlanHash(request)
	prompt, err := selectorRepairUserPrompt(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`"recording"`, `"events"`, `"snapshots"`, `"chunkAnalyses"`,
		`"replayArtifacts"`, `"path"`, `steps\u0000#0`,
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("selector-only prompt leaked forbidden source %q: %s", forbidden, prompt)
		}
	}
	for _, required := range []string{
		`"slotId":"slot_target"`, `"allowedCandidateIds":["row_right","row_wrong"]`,
		`"confirmedOutputContract"`, `"selectorCatalog"`,
		`"selectionFailure":{"code":"candidate_scope","slot":"steps[0].fields.title"`,
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("selector-only prompt omitted %q: %s", required, prompt)
		}
	}
	for _, required := range []string{
		"exactly equal one complete object in allowedAssignments",
		"copied byte-for-byte",
		"row_1",
		"field_1",
		"target_1",
	} {
		if !strings.Contains(selectorRepairSystemPrompt, required) {
			t.Fatalf("selector-only system prompt omitted %q", required)
		}
	}
}

func TestSelectorRepairFailureAndDiagnosticBoundsAreExact(t *testing.T) {
	original := &platformrule.SelectorCandidateSelectionError{
		Code: " candidate_scope ", Slot: " steps[0].fields.title ",
		Detail: " wrong cohort ",
	}
	normalized, err := normalizeSelectorRepairFailure(original)
	if err != nil {
		t.Fatal(err)
	}
	if normalized == original ||
		normalized.Code != "candidate_scope" ||
		normalized.Slot != "steps[0].fields.title" ||
		normalized.Detail != "wrong cohort" {
		t.Fatalf("selection failure was not normalized into a bounded copy: %+v", normalized)
	}
	if original.Code != " candidate_scope " {
		t.Fatal("selection failure normalization mutated the captured source error")
	}
	oversized := *normalized
	oversized.Detail = strings.Repeat("x", maxSelectorRepairFailureDetailBytes+1)
	if _, err := normalizeSelectorRepairFailure(&oversized); !errors.Is(err, ErrWorkflowSourceUnavailable) {
		t.Fatalf("oversized selection failure was accepted: %v", err)
	}

	request := selectorRepairUnitRequest()
	request.Diagnostic.Detail = strings.Repeat("d", maxSelectorRepairDiagnosticBytes)
	if _, err := selectorRepairUserPrompt(request); !errors.Is(err, ErrWorkflowSourceUnavailable) {
		t.Fatalf("oversized diagnostic was truncated or accepted: %v", err)
	}
}

func TestSelectorRepairPatchChangesOnlyAllowedSlots(t *testing.T) {
	request := selectorRepairUnitRequest()
	before, _ := json.Marshal(request.ProviderIR)
	response := &selectorRepairResponse{
		SelectorCatalogHash: request.SelectorCatalogHash,
		Replacements: []selectorRepairReplacement{
			{SlotID: "slot_target", CandidateID: "row_right"},
		},
	}
	repaired, err := applySelectorRepairPatch(
		request.ProviderIR, request.Slots, request.AllowedAssignments, response,
	)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(repaired)
	if string(before) == string(after) || strings.Contains(string(after), "row_wrong") {
		t.Fatalf("allowed replacement was not applied: %s", after)
	}
	normalized := strings.Replace(string(after), "row_right", "row_wrong", 1)
	var normalizedValue, beforeValue any
	_ = json.Unmarshal([]byte(normalized), &normalizedValue)
	_ = json.Unmarshal(before, &beforeValue)
	if !reflect.DeepEqual(normalizedValue, beforeValue) {
		t.Fatalf("non-slot provider IR changed: before=%s after=%s", before, after)
	}

	for name, invalid := range map[string]*selectorRepairResponse{
		"unknown slot": {
			Replacements: []selectorRepairReplacement{{SlotID: "slot_other", CandidateID: "row_right"}},
		},
		"candidate outside allowed set": {
			Replacements: []selectorRepairReplacement{{SlotID: "slot_target", CandidateID: "row_untrusted"}},
		},
		"no-op": {
			Replacements: []selectorRepairReplacement{{SlotID: "slot_target", CandidateID: "row_wrong"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := applySelectorRepairPatch(
				request.ProviderIR, request.Slots, request.AllowedAssignments, invalid,
			); !errors.Is(err, ErrInvalidLLMRule) {
				t.Fatalf("invalid patch was accepted: %v", err)
			}
		})
	}
}

func TestSelectorRepairPatchRejectsIncompatibleCandidateTuples(t *testing.T) {
	tests := []struct {
		name         string
		rule         *models.Rule
		slots        []platformrule.SelectorRepairSlot
		assignments  []map[string]string
		replacements []selectorRepairReplacement
	}{
		{
			name: "cross-target field",
			rule: &models.Rule{Steps: models.JSON(`[
				{"action":"extract","target":{"rowCandidateId":"row_a"},
				 "fields":{"title":{"type":"text","fieldCandidateId":"field_a"}}}
			]`)},
			slots: []platformrule.SelectorRepairSlot{
				{
					ID: "target", CurrentCandidateID: "row_a",
					AllowedCandidateIDs: []string{"row_a", "row_b"},
					Path:                []string{"steps", "#0", "target", "rowCandidateId"},
				},
				{
					ID: "field", CurrentCandidateID: "field_a",
					AllowedCandidateIDs: []string{"field_a", "field_b"},
					Path:                []string{"steps", "#0", "fields", "title", "fieldCandidateId"},
				},
			},
			assignments: []map[string]string{
				{"target": "row_a", "field": "field_a"},
				{"target": "row_b", "field": "field_b"},
			},
			replacements: []selectorRepairReplacement{
				{SlotID: "target", CandidateID: "row_b"},
			},
		},
		{
			name: "wrong nested parent",
			rule: &models.Rule{Steps: models.JSON(`[
				{"action":"extract","target":{"rowCandidateId":"row"},
				 "fields":{"author":{"type":"object","fieldCandidateId":"parent_a",
				   "fields":{"name":{"type":"text","fieldCandidateId":"child_a"}}}}}
			]`)},
			slots: []platformrule.SelectorRepairSlot{
				{
					ID: "parent", CurrentCandidateID: "parent_a",
					AllowedCandidateIDs: []string{"parent_a", "parent_b"},
					Path:                []string{"steps", "#0", "fields", "author", "fieldCandidateId"},
				},
				{
					ID: "child", CurrentCandidateID: "child_a",
					AllowedCandidateIDs: []string{"child_a", "child_b"},
					Path:                []string{"steps", "#0", "fields", "author", "fields", "name", "fieldCandidateId"},
				},
			},
			assignments: []map[string]string{
				{"parent": "parent_a", "child": "child_a"},
				{"parent": "parent_b", "child": "child_b"},
			},
			replacements: []selectorRepairReplacement{
				{SlotID: "parent", CandidateID: "parent_b"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := applySelectorRepairPatch(
				test.rule, test.slots, test.assignments,
				&selectorRepairResponse{Replacements: test.replacements},
			)
			if !errors.Is(err, ErrInvalidLLMRule) ||
				!strings.Contains(err.Error(), "complete compatible assignment") {
				t.Fatalf("incompatible tuple was accepted: %v", err)
			}
		})
	}
}

func TestSelectorRepairPatchAppliesMapValuedTriggerSlot(t *testing.T) {
	rule := &models.Rule{Steps: models.JSON(`[
		{"action":"handleDownload","trigger":{
			"action":"extract","target":{"rowCandidateId":"row_wrong"},
			"fields":{"title":{"type":"text","fieldCandidateId":"field_title"}}
		}}
	]`)}
	slot := platformrule.SelectorRepairSlot{
		ID: "trigger-target", Kind: "rowCandidateId",
		CurrentCandidateID:  "row_wrong",
		AllowedCandidateIDs: []string{"row_right", "row_wrong"},
		Path:                []string{"steps", "#0", "trigger", "target", "rowCandidateId"},
	}
	repaired, err := applySelectorRepairPatch(
		rule, []platformrule.SelectorRepairSlot{slot},
		[]map[string]string{{slot.ID: "row_right"}},
		&selectorRepairResponse{Replacements: []selectorRepairReplacement{{
			SlotID: slot.ID, CandidateID: "row_right",
		}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var steps []map[string]any
	if err := json.Unmarshal(repaired.Steps, &steps); err != nil {
		t.Fatal(err)
	}
	trigger, _ := steps[0]["trigger"].(map[string]any)
	target, _ := trigger["target"].(map[string]any)
	if target["rowCandidateId"] != "row_right" {
		t.Fatalf("map-valued trigger slot was not applied: %#v", steps)
	}
}

func TestSelectorRepairResponseIsExactAndBounded(t *testing.T) {
	valid := `{"selectorCatalogHash":"hash","replacements":[{"slotId":"slot","candidateId":"row"}]}`
	if _, err := parseSelectorRepairResponse(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"selectorCatalogHash":"hash","replacements":[{"slotId":"slot","candidateId":"row"}],"rule":{}}`,
		`{"selectorCatalogHash":"hash","selectorCatalogHash":"other","replacements":[{"slotId":"slot","candidateId":"row"}]}`,
		`{"selectorCatalogHash":"hash","replacements":[{"slotId":"slot","candidateId":"row"},{"slotId":"slot","candidateId":"other"}]}`,
		valid + `{}`,
	} {
		if _, err := parseSelectorRepairResponse(invalid); !errors.Is(err, ErrInvalidLLMRule) {
			t.Fatalf("non-exact response was accepted: %s err=%v", invalid, err)
		}
	}
}

func TestSelectorRepairSourceCallMustBeReplayableTerminalRuleResponse(t *testing.T) {
	valid := &models.LLMProviderCall{
		ID: "source", CallIndex: 2, Phase: llm.CompletionPhaseSynthesis,
		CallKind: models.LLMProviderCallKindResponse, Replayable: true,
	}
	calls := []*models.LLMProviderCall{
		{
			ID: "analysis", CallIndex: 1, Phase: llm.CompletionPhaseAnalysis,
			CallKind: models.LLMProviderCallKindResponse, Replayable: true,
		},
		valid,
	}
	if err := validateSelectorRepairTerminalSourceCall(valid, calls); err != nil {
		t.Fatalf("valid terminal source was rejected: %v", err)
	}
	tests := map[string]struct {
		source *models.LLMProviderCall
		calls  []*models.LLMProviderCall
	}{
		"selector repair child cannot be a parent source": {
			source: &models.LLMProviderCall{
				ID: "patch", CallIndex: 1, Phase: llm.CompletionPhaseSelectorRepair,
				CallKind: models.LLMProviderCallKindResponse, Replayable: true,
			},
			calls: []*models.LLMProviderCall{{
				ID: "patch", CallIndex: 1, Phase: llm.CompletionPhaseSelectorRepair,
				CallKind: models.LLMProviderCallKindResponse, Replayable: true,
			}},
		},
		"provider error": {
			source: &models.LLMProviderCall{
				ID: "error", CallIndex: 1, Phase: llm.CompletionPhaseFinal,
				CallKind: models.LLMProviderCallKindError, Replayable: true,
			},
			calls: []*models.LLMProviderCall{{
				ID: "error", CallIndex: 1, Phase: llm.CompletionPhaseFinal,
				CallKind: models.LLMProviderCallKindError, Replayable: true,
			}},
		},
		"nonterminal": {
			source: valid,
			calls: append(append([]*models.LLMProviderCall(nil), calls...), &models.LLMProviderCall{
				ID: "later", CallIndex: 3, Phase: llm.CompletionPhaseFinal,
				CallKind: models.LLMProviderCallKindResponse, Replayable: true,
			}),
		},
		"nonreplayable": {
			source: &models.LLMProviderCall{
				ID: "source", CallIndex: 1, Phase: llm.CompletionPhaseFinal,
				CallKind: models.LLMProviderCallKindResponse,
			},
			calls: []*models.LLMProviderCall{{
				ID: "source", CallIndex: 1, Phase: llm.CompletionPhaseFinal,
				CallKind: models.LLMProviderCallKindResponse,
			}},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateSelectorRepairTerminalSourceCall(test.source, test.calls); !errors.Is(err, ErrInvalidWorkflowInput) {
				t.Fatalf("invalid source call was accepted: %v", err)
			}
		})
	}
}
