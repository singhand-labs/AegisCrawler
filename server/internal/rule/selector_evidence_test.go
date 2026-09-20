package rule

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

func TestSelectorEvidenceAcceptsStableAnchoredRepeatedCards(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search?q=one", selectorResultsForTest("results", nil)),
		selectorSnapshotForTest(1, "https://example.test/search?q=one", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		})),
		selectorSnapshotForTest(2, "https://example.test/search?q=one", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		})),
	)
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true, "onEmpty": "fail",
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".title"},
			"summary": map[string]any{"type": "text", "selector": ".summary"},
		},
	})

	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if err != nil {
		t.Fatalf("stable repeated selector rejected: %v", err)
	}
	if report.Checked != 1 || report.Canonicalized != 0 {
		t.Fatalf("unexpected selector evidence report: %+v", report)
	}
}

func TestSelectorEvidenceRejectsEmptyRecordedTextFields(t *testing.T) {
	rows := make([]map[string]any, 0, 2)
	for index := 0; index < 2; index++ {
		rows = append(rows, selectorElementForTest("article", map[string]string{"class": "result"},
			selectorElementForTest("h3", map[string]string{"class": "t"}, selectorTextForTest(fmt.Sprintf("title %d", index+1))),
			selectorElementForTest("div", map[string]string{"class": "c-row"}),
			selectorElementForTest("div", map[string]string{"class": "c-abstract", "role": "text"},
				selectorTextForTest(fmt.Sprintf("summary %d", index+1))),
		))
	}
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "content_left"}, rows...),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://www.baidu.com/s", root))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "items", "multiple": true, "onEmpty": "fail",
		"target": map[string]any{"selector": "#content_left > .result", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".t"},
			"summary": map[string]any{"type": "text", "selector": ".c-row"},
		},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "non-empty recorded text") {
		t.Fatalf("empty recorded text selector must fail before replay: %v", err)
	}

	step := selectorRuleStepForTest(t, rule)
	step["fields"].(map[string]any)["summary"].(map[string]any)["selector"] = `[role="text"]`
	encoded, marshalErr := json.Marshal([]any{step})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	rule.Steps = models.JSON(encoded)
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog); err != nil {
		t.Fatalf("recorded non-empty semantic text selector was rejected: %v", err)
	}

	var candidate *SelectorEvidenceCandidate
	for index := range catalog.validationCandidates {
		if catalog.validationCandidates[index].Selector == "#content_left > .result" {
			candidate = &catalog.validationCandidates[index]
			break
		}
	}
	if candidate == nil {
		t.Fatalf("missing repeated result candidate: %+v", catalog.validationCandidates)
	}
	textIndex := slices.Index(candidate.RelativeSelectors, `[role="text"]`)
	emptyIndex := slices.Index(candidate.RelativeSelectors, ".c-row")
	if textIndex < 0 || emptyIndex < 0 || textIndex > emptyIndex {
		t.Fatalf("text-bearing relative selectors must be preferred: %+v", candidate.RelativeSelectors)
	}
}

func TestSelectorEvidenceCanonicalizesEveryExactRepeatedEquivalent(t *testing.T) {
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
		{classes: "card result-op", title: true},
		{classes: "card result-op", title: true},
	})))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true, "onEmpty": "fail",
		"target": map[string]any{"selector": "#results > article.card.result-op", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": "h2.title"}},
	})
	extract := selectorRuleStepForTest(t, rule)
	steps := []any{
		map[string]any{
			"action": "waitForElementVisible",
			"target": map[string]any{"selector": "#results > article.card.result-op", "visible": true},
		},
		map[string]any{"action": "waitForTimeout", "ms": 1500},
		extract,
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	rule.Steps = encoded

	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if err != nil {
		t.Fatal(err)
	}
	stabilized := decodeWorkflowArray(t, rule.Steps)
	wait := stabilized[0].(map[string]any)
	step := stabilized[2].(map[string]any)
	if selector := step["target"].(map[string]any)["selector"]; selector != "#results > .card" {
		t.Fatalf("exact repeated selector was not stabilized: %v", selector)
	}
	if !reflect.DeepEqual(wait["target"], step["target"]) {
		t.Fatalf("derived readiness was not synchronized with stabilized extraction: wait=%#v extract=%#v", wait, step)
	}
	if selector := step["fields"].(map[string]any)["title"].(map[string]any)["selector"]; selector != ".title" {
		t.Fatalf("exact relative selector was not stabilized: %v", selector)
	}
	if report.Canonicalized != 2 {
		t.Fatalf("unexpected stabilization report: %+v", report)
	}
	changed, err := normalizeWorkflowRepeatedExtractionReadiness(rule)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("selector stabilization made the full readiness pipeline non-idempotent: %s", rule.Steps)
	}
}

func TestSelectorEvidenceDerivesStableStructuralRelations(t *testing.T) {
	cards := selectorElementForTest("div", map[string]string{"class": "cards"},
		selectorElementForTest("article", map[string]string{"class": "card"}, selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("first"))),
		selectorElementForTest("article", map[string]string{"class": "card"}, selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("second"))),
	)
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "results"},
			selectorElementForTest("section", map[string]string{"class": "result-list"}, cards),
		),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", root))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results .card", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})

	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog); err != nil {
		t.Fatalf("stable anchored structural cohort rejected: %v", err)
	}
	found := false
	for _, candidate := range catalog.validationCandidates {
		found = found || candidate.Selector == "#results > .result-list > .cards > .card"
	}
	if !found {
		t.Fatalf("structural repeated candidate was not derived: %+v", catalog.validationCandidates)
	}
}

func TestSelectorEvidenceDerivesStableClassSubcohortsWithoutPromotingDescendants(t *testing.T) {
	first := selectorMixedResultRowsForTest(3)
	second := selectorMixedResultRowsForTest(3)
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search?q=one", first),
		selectorSnapshotForTest(1, "https://example.test/search?q=one", second),
	)

	var organic *SelectorEvidenceCandidate
	for index := range catalog.validationCandidates {
		candidate := &catalog.validationCandidates[index]
		if candidate.Selector == "#content_left > .result" {
			organic = candidate
			break
		}
	}
	if organic == nil {
		t.Fatalf("stable class-defined subcohort was not derived: %+v", catalog.validationCandidates)
	}
	if !slices.Equal(organic.Cardinalities, []int{3, 3}) ||
		!slices.Contains(organic.RelativeSelectors, ".cosc-title") ||
		!slices.Contains(organic.RelativeSelectors, ".cos-color-text-tiny") {
		t.Fatalf("organic result evidence lost cardinality or fields: %+v", organic)
	}
	for _, candidate := range catalog.validationCandidates {
		if candidate.Selector == "#content_left > div.result" ||
			slices.Contains(candidate.RelativeSelectors, "h3.cosc-title") ||
			slices.Contains(candidate.RelativeSelectors, "div.cos-color-text-tiny") {
			t.Fatalf("exact-equivalent selector consumed catalog capacity: %+v", candidate)
		}
	}

	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true, "onEmpty": "fail",
		"target": map[string]any{"selector": "#content_left > div.result", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": "h3.cosc-title"},
			"summary": map[string]any{"type": "text", "selector": "div.cos-color-text-tiny"},
		},
	})
	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if err != nil {
		t.Fatalf("stable class-defined subcohort was rejected: %v", err)
	}
	step := selectorRuleStepForTest(t, rule)
	if selector := step["target"].(map[string]any)["selector"]; selector != "#content_left > .result" {
		t.Fatalf("organic cohort was not canonicalized: %v", selector)
	}
	fields := step["fields"].(map[string]any)
	if fields["title"].(map[string]any)["selector"] != ".cosc-title" ||
		fields["summary"].(map[string]any)["selector"] != ".cos-color-text-tiny" ||
		report.Canonicalized != 3 {
		t.Fatalf("organic fields were not canonicalized: fields=%v report=%+v", fields, report)
	}

	innerWrapper := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true, "onEmpty": "fail",
		"target": map[string]any{"selector": "#content_left .cosc-card", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".cosc-title"},
			"summary": map[string]any{"type": "text", "selector": ".cos-color-text-tiny"},
		},
	})
	_, err = ValidateAndStabilizeGeneratedExtractionSelectors(innerWrapper, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "exact stable recorded repeated cohort") {
		t.Fatalf("one-to-one inner wrappers must not be promoted to their parent rows: %v", err)
	}
}

func TestSelectorEvidenceDerivesStableSemanticAttributeSubcohorts(t *testing.T) {
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "results"},
			selectorElementForTest("article", map[string]string{"class": "card", "data-qa": "organic"},
				selectorElementForTest("h2", map[string]string{"class": "title"})),
			selectorElementForTest("article", map[string]string{"class": "card", "data-qa": "organic"},
				selectorElementForTest("h2", map[string]string{"class": "title"})),
			selectorElementForTest("article", map[string]string{"class": "card", "data-qa": "module"},
				selectorElementForTest("h2", map[string]string{"class": "module-title"})),
		),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", root))
	found := false
	for _, candidate := range catalog.validationCandidates {
		if candidate.Selector == `#results > [data-qa="organic"]` {
			found = slices.Equal(candidate.Cardinalities, []int{2}) &&
				slices.Contains(candidate.RelativeSelectors, ".title")
		}
	}
	if !found {
		t.Fatalf("stable semantic-attribute subcohort was not derived: %+v", catalog.validationCandidates)
	}
}

func TestSelectorEvidenceCompactsTargetsOnlyAfterFullStateComparison(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card a", title: true},
			{classes: "card a", title: true},
		})),
		selectorSnapshotForTest(1, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card b", title: true},
			{classes: "card b", title: true},
		})),
	)
	var stable *SelectorEvidenceCandidate
	for index := range catalog.validationCandidates {
		candidate := &catalog.validationCandidates[index]
		if candidate.Selector == "#results > .card" {
			stable = candidate
		}
		if candidate.Selector == "#results > article.card" {
			t.Fatalf("full-state exact equivalent was not compacted: %+v", candidate)
		}
	}
	if stable == nil || !slices.Equal(stable.SnapshotSequences, []int{0, 1}) ||
		!slices.Equal(stable.Cardinalities, []int{2, 2}) {
		t.Fatalf("stable class shared across state drift was lost: %+v", stable)
	}
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog); err != nil {
		t.Fatalf("stable cross-snapshot cohort was rejected: %v", err)
	}
}

func TestSelectorEvidenceCanonicalizesOnlyAnExactRecordedNodeSet(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card css-Ab12Cd", title: true, summary: true},
			{classes: "card css-Ab12Cd", title: true, summary: true},
		})),
		selectorSnapshotForTest(1, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card css-Ab12Cd", title: true, summary: true},
			{classes: "card css-Ab12Cd", title: true, summary: true},
		})),
	)
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card.css-Ab12Cd", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})

	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if err != nil {
		t.Fatalf("exact volatile selector was not canonicalized: %v", err)
	}
	step := selectorRuleStepForTest(t, rule)
	target := step["target"].(map[string]any)
	if target["selector"] != "#results > .card" || report.Canonicalized != 1 {
		t.Fatalf("unexpected canonicalization: selector=%v report=%+v", target["selector"], report)
	}
}

func TestSelectorEvidenceCanonicalizesExactUniqueAnchor(t *testing.T) {
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("h1", map[string]string{"class": "css-Ab12Cd", "data-qa": "page-title"}, selectorTextForTest("private page text")),
	))
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", root),
		selectorSnapshotForTest(1, "https://example.test/search", root),
	)
	rule := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "title",
		"target": map[string]any{"selector": ".css-Ab12Cd", "visible": true},
	})

	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if err != nil {
		t.Fatalf("exact stable unique anchor was not used: %v", err)
	}
	selector := selectorRuleStepForTest(t, rule)["target"].(map[string]any)["selector"]
	if selector != `[data-qa="page-title"]` || report.Canonicalized != 1 {
		t.Fatalf("unexpected unique canonicalization: selector=%v report=%+v", selector, report)
	}
}

func TestSelectorEvidenceCanonicalizesRefInlineWithoutMutatingAlias(t *testing.T) {
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
		{classes: "card css-Ab12Cd", title: true}, {classes: "card css-Ab12Cd", title: true},
	})))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"$ref": "recorded-cards", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})
	rule.Selectors = models.JSON(`{"recorded-cards":{"selector":"#results > .card.css-Ab12Cd"}}`)

	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if err != nil {
		t.Fatal(err)
	}
	target := selectorRuleStepForTest(t, rule)["target"].(map[string]any)
	if target["$ref"] != "recorded-cards" || target["selector"] != "#results > .card" || report.Canonicalized != 1 {
		t.Fatalf("unexpected ref canonicalization: target=%v report=%+v", target, report)
	}
	if string(rule.Selectors) != `{"recorded-cards":{"selector":"#results > .card.css-Ab12Cd"}}` {
		t.Fatalf("shared selector alias was mutated: %s", rule.Selectors)
	}
}

func TestSelectorEvidenceRejectsHashDriftAcrossRecordedStates(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card css-Ab12Cd", title: true},
			{classes: "card css-Ab12Cd", title: true},
		})),
		selectorSnapshotForTest(1, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card css-Xy34Zq", title: true},
			{classes: "card css-Xy34Zq", title: true},
		})),
	)
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card.css-Ab12Cd", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "exact stable recorded repeated cohort") {
		t.Fatalf("hash drift must fail closed, got %v", err)
	}
}

func TestSelectorEvidenceRejectsWrongCardinalityWithoutWidening(t *testing.T) {
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
		{classes: "card featured", title: true},
		{classes: "card", title: true},
		{classes: "card", title: true},
	})))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .featured", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "recorded cardinality is 1") {
		t.Fatalf("one-card selector must be rejected, got %v", err)
	}
	if got := selectorRuleStepForTest(t, rule)["target"].(map[string]any)["selector"]; got != "#results > .featured" {
		t.Fatalf("wrong-cardinality selector was widened to %v", got)
	}
}

func TestSelectorEvidenceRejectsInconsistentCardinality(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true}, {classes: "card", title: true},
		})),
		selectorSnapshotForTest(1, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true}, {classes: "card", title: true}, {classes: "card", title: true},
		})),
	)
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "inconsistent recorded evidence") {
		t.Fatalf("inconsistent cardinality must fail closed, got %v", err)
	}
}

func TestSelectorEvidenceRejectsMissingRelativeFieldCoverage(t *testing.T) {
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
		{classes: "card", title: true, summary: true},
		{classes: "card", title: true, summary: false},
	})))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".title"},
			"summary": map[string]any{"type": "text", "selector": ".summary"},
		},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "must match exactly one descendant") {
		t.Fatalf("missing field coverage must fail closed, got %v", err)
	}
}

func TestSelectorEvidenceRecoversMissingFieldFromUnambiguousSemanticNodes(t *testing.T) {
	rows := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "results"},
			selectorElementForTest("article", map[string]string{"class": "card"},
				selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("first")),
				selectorElementForTest("div", map[string]string{"class": "summary-gap_68jXq cos-color-text-tiny"}, selectorTextForTest("first summary"))),
			selectorElementForTest("article", map[string]string{"class": "card"},
				selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("second")),
				selectorElementForTest("div", map[string]string{"class": "summary-gap_68jXq cos-color-text-tiny"}, selectorTextForTest("second summary"))),
		),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", rows))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".title"},
			"summary": map[string]any{"type": "text", "selector": ".c-abstract"},
		},
	})

	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if err != nil {
		t.Fatalf("unambiguous semantic field evidence was rejected: %v", err)
	}
	fields := selectorRuleStepForTest(t, rule)["fields"].(map[string]any)
	if selector := fields["summary"].(map[string]any)["selector"]; selector != ".cos-color-text-tiny" {
		t.Fatalf("missing summary selector was not canonicalized to durable evidence: %v", selector)
	}
	if report.Canonicalized != 1 {
		t.Fatalf("unexpected semantic recovery report: %+v", report)
	}
}

func TestSelectorEvidenceRejectsAmbiguousSemanticFieldRecovery(t *testing.T) {
	rows := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "results"},
			selectorElementForTest("article", map[string]string{"class": "card"},
				selectorElementForTest("div", map[string]string{"class": "summary-primary"}, selectorTextForTest("first primary")),
				selectorElementForTest("div", map[string]string{"class": "summary-secondary"}, selectorTextForTest("first secondary"))),
			selectorElementForTest("article", map[string]string{"class": "card"},
				selectorElementForTest("div", map[string]string{"class": "summary-primary"}, selectorTextForTest("second primary")),
				selectorElementForTest("div", map[string]string{"class": "summary-secondary"}, selectorTextForTest("second secondary"))),
		),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", rows))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{
			"summary": map[string]any{"type": "text", "selector": ".c-abstract"},
		},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "must match exactly one descendant") {
		t.Fatalf("ambiguous semantic field evidence must fail closed: %v", err)
	}
}

func TestSelectorEvidenceRequiresBrowserDescendantsAndValidatesNestedFields(t *testing.T) {
	rows := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "results"},
			selectorElementForTest("article", map[string]string{"class": "card"},
				selectorElementForTest("section", map[string]string{"class": "details"},
					selectorElementForTest("h2", map[string]string{"class": "title css-Ab12Cd"}, selectorTextForTest("first")))),
			selectorElementForTest("article", map[string]string{"class": "card"},
				selectorElementForTest("section", map[string]string{"class": "details"},
					selectorElementForTest("h2", map[string]string{"class": "title css-Ab12Cd"}, selectorTextForTest("second")))),
		),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", rows))

	self := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{"self": map[string]any{"type": "text", "selector": ".card"}},
	})
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(self, catalog); !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "exactly one descendant") {
		t.Fatalf("row-self selector must not be treated as querySelector coverage: %v", err)
	}

	nested := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{
			"details": map[string]any{
				"type": "text", "selector": ".details",
				"fields": map[string]any{
					"title": map[string]any{"type": "text", "selector": ".title.css-Ab12Cd"},
				},
			},
		},
	})
	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(nested, catalog)
	if err != nil {
		t.Fatalf("nested selector evidence rejected: %v", err)
	}
	step := selectorRuleStepForTest(t, nested)
	title := step["fields"].(map[string]any)["details"].(map[string]any)["fields"].(map[string]any)["title"].(map[string]any)
	if title["selector"] != ".title" || report.Canonicalized != 1 {
		t.Fatalf("nested volatile selector was not stabilized: field=%v report=%+v", title, report)
	}

	nestedFields := step["fields"].(map[string]any)["details"].(map[string]any)["fields"].(map[string]any)
	delete(nestedFields, "title")
	nestedFields["unknown"] = map[string]any{"type": "text", "selector": ".missing"}
	encoded, _ := json.Marshal([]any{step})
	nested.Steps = encoded
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(nested, catalog); !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "fields.details.fields.unknown") {
		t.Fatalf("nested missing selector was not rejected with its path: %v", err)
	}
}

func TestSelectorEvidenceRejectsTemporalNodeSetMismatchOutsideCandidateWindow(t *testing.T) {
	before := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "results"},
			selectorElementForTest("aside", map[string]string{"class": "model"}, selectorElementForTest("h2", map[string]string{"class": "title"})),
		),
	))
	after := selectorResultsForTest("results", []selectorCardForTest{
		{classes: "card model", title: true}, {classes: "card model", title: true},
	})
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", before),
		selectorSnapshotForTest(1, "https://example.test/search", after),
	)
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": ".model", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) {
		t.Fatalf("selector that changes node sets outside the cohort window was accepted: %v", err)
	}
}

func TestSelectorEvidenceRejectsNoEvidenceUnsupportedAndPositionalTargets(t *testing.T) {
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
		{classes: "card", title: true}, {classes: "card", title: true},
	})))
	tests := []struct {
		name   string
		target map[string]any
		want   string
	}{
		{name: "missing", target: map[string]any{"selector": ".missing", "visible": true}, want: "no consistent recorded evidence"},
		{name: "xpath", target: map[string]any{"selector": ".card", "xpath": "//article", "visible": true}, want: "target.xpath is not recording-grounded"},
		{name: "position", target: map[string]any{"selector": ".card", "position": map[string]any{"x": 1, "y": 2}, "visible": true}, want: "target.position is not recording-grounded"},
		{name: "positional-css", target: map[string]any{"selector": "#results > .card:nth-child(1)", "visible": true}, want: "is positional and not recording-grounded"},
		{name: "non-browser-css", target: map[string]any{"selector": `#results:contains("title")`, "visible": true}, want: "non-browser pseudo-class"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := selectorRuleForTest(map[string]any{
				"action": "extract", "name": "rows", "multiple": true, "target": test.target,
				"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
			})
			_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
			if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestSelectorEvidenceAllowsBrowserHasAndIgnoresMarkersInsideCSSStrings(t *testing.T) {
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "results"},
			selectorElementForTest("article", map[string]string{"class": "card", "data-label": ":first-child"}, selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("first"))),
			selectorElementForTest("article", map[string]string{"class": "card", "data-label": ":first-child"}, selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("second"))),
		),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", root))
	rule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": `#results > .card[data-label=":first-child"]:has(.title)`, "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog); err != nil {
		t.Fatalf("browser-standard :has or a quoted positional marker was rejected: %v", err)
	}
	if state := selectorStateKey("relative/search?token=secret#fragment"); state != "relative/search" {
		t.Fatalf("invalid URL state retained query or fragment data: %q", state)
	}
}

func TestSelectorEvidenceAcceptsRuntimeImplicitRolesAndRejectsAttributeHashes(t *testing.T) {
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("button", map[string]string{"data-qa": "submit", "aria-label": "Submit"}),
		selectorElementForTest("div", map[string]string{"id": "a1b2c3d4e5f6"}),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/form", root))
	button := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "label",
		"target": map[string]any{"selector": `[data-qa="submit"]`, "role": "button", "roleName": "Submit", "visible": true},
	})
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(button, catalog); err != nil {
		t.Fatalf("runtime-compatible implicit role was rejected: %v", err)
	}

	hash := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "value",
		"target": map[string]any{"selector": `[id="a1b2c3d4e5f6"]`, "visible": true},
	})
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(hash, catalog); !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "volatile") {
		t.Fatalf("attribute-form hash id was treated as stable: %v", err)
	}
}

func TestSelectorEvidenceRequiresAtLeastOneUsableSemanticSnapshot(t *testing.T) {
	_, err := BuildSelectorEvidenceCatalog(map[string]any{"snapshots": []any{
		map[string]any{"capture": map[string]any{"status": "failed"}},
	}})
	if !errors.Is(err, ErrSelectorEvidenceUnavailable) {
		t.Fatalf("unusable recording evidence should fail before provider work: %v", err)
	}
}

func TestSelectorEvidenceKeepsIframeDocumentsIsolated(t *testing.T) {
	frameCard := selectorElementForTest("article", map[string]string{"class": "card", "id": "frame-card"}, selectorElementForTest("span", map[string]string{"class": "title"}))
	iframe := selectorElementForTest("iframe", map[string]string{"id": "embedded"}, frameCard)
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil, iframe))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", root))
	rule := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "title",
		"target": map[string]any{"selector": "#frame-card", "visible": true},
	})

	_, err := ValidateAndStabilizeGeneratedExtractionSelectors(rule, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "no consistent recorded evidence") {
		t.Fatalf("iframe descendant leaked into top-level selector evidence: %v", err)
	}

	framed := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "title",
		"target": map[string]any{"selector": ".title", "frame": "#embedded", "visible": true},
	})
	_, err = ValidateAndStabilizeGeneratedExtractionSelectors(framed, catalog)
	if !errors.Is(err, ErrInvalidProvisionalRule) || !strings.Contains(err.Error(), "frame selector lacks isolated recording evidence") {
		t.Fatalf("framed extraction must fail until isolated validation exists: %v", err)
	}
}

func TestSelectorEvidenceSupportsStableUniqueAndNumericScopedRows(t *testing.T) {
	rows := selectorElementForTest("section", map[string]string{"data-testid": "result-list"},
		selectorElementForTest("article", map[string]string{"id": "101", "class": "card"}, selectorElementForTest("span", map[string]string{"class": "title"}, selectorTextForTest("first"))),
		selectorElementForTest("article", map[string]string{"id": "102", "class": "card"}, selectorElementForTest("span", map[string]string{"class": "title"}, selectorTextForTest("second"))),
	)
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("h1", map[string]string{"data-qa": "page-title"}), rows,
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", root))

	unique := selectorRuleForTest(map[string]any{
		"action": "extractText", "name": "heading",
		"target": map[string]any{"selector": `[data-qa="page-title"]`, "visible": true},
	})
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(unique, catalog); err != nil {
		t.Fatalf("stable unique selector rejected: %v", err)
	}

	repeated := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": `[data-testid="result-list"] > .card[id]`, "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(repeated, catalog); err != nil {
		t.Fatalf("numeric row ids should be scoped structurally rather than used as anchors: %v", err)
	}

	duplicateRows := selectorElementForTest("section", map[string]string{"data-testid": "duplicate-list"},
		selectorElementForTest("article", map[string]string{"id": "item", "class": "card"}, selectorElementForTest("span", map[string]string{"class": "title"}, selectorTextForTest("first"))),
		selectorElementForTest("article", map[string]string{"id": "item", "class": "card"}, selectorElementForTest("span", map[string]string{"class": "title"}, selectorTextForTest("second"))),
	)
	duplicateRoot := selectorElementForTest("html", nil, selectorElementForTest("body", nil, duplicateRows))
	duplicateCatalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", duplicateRoot))
	for _, candidate := range duplicateCatalog.validationCandidates {
		if candidate.Selector == "#item" {
			t.Fatalf("duplicate id was incorrectly treated as a unique anchor: %+v", candidate)
		}
	}
	duplicateRule := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": `#item`, "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})
	report, err := ValidateAndStabilizeGeneratedExtractionSelectors(duplicateRule, duplicateCatalog)
	if err != nil {
		t.Fatalf("duplicate row ids should be scoped structurally rather than used as anchors: %v", err)
	}
	if selector := selectorRuleStepForTest(t, duplicateRule)["target"].(map[string]any)["selector"]; selector != `[data-testid="duplicate-list"] > .card` || report.Canonicalized != 1 {
		t.Fatalf("duplicate id selector was not scoped to the exact cohort: selector=%v report=%+v", selector, report)
	}
}

func TestSelectorEvidenceCatalogAndPromptAreDeterministicallyBounded(t *testing.T) {
	children := make([]map[string]any, 0, 304)
	for index := 0; index < 300; index++ {
		children = append(children, selectorElementForTest("span", map[string]string{
			"data-testid": "unique-anchor-" + strings.Repeat("x", 40) + string(rune('A'+index%26)) + string(rune('a'+(index/26)%26)),
		}, selectorTextForTest("page text must not enter selector evidence")))
	}
	children = append(children, selectorElementForTest("section", map[string]string{"id": "results"},
		selectorElementForTest("article", map[string]string{"class": "card"}, selectorElementForTest("h2", map[string]string{"class": "title"})),
		selectorElementForTest("article", map[string]string{"class": "card"}, selectorElementForTest("h2", map[string]string{"class": "title"})),
	))
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil, children...))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", root))
	if len(catalog.Candidates) > maxSelectorCatalogCandidates || len(catalog.validationCandidates) > maxSelectorValidationCandidates {
		t.Fatalf("selector catalogs exceeded bounds: public=%d validation=%d", len(catalog.Candidates), len(catalog.validationCandidates))
	}
	evidence := catalog.PromptEvidenceJSON()
	if len(evidence) > maxSelectorEvidencePromptBytes || !json.Valid([]byte(evidence)) {
		t.Fatalf("prompt evidence is not bounded valid JSON: bytes=%d", len(evidence))
	}
	if strings.Contains(evidence, "page text must not enter") {
		t.Fatalf("page text leaked into selector evidence: %s", evidence)
	}
	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(evidence), &promptCatalog); err != nil {
		t.Fatal(err)
	}
	if promptCatalog.Version != selectorPromptCatalogVersion || promptCatalog.CatalogHash == "" {
		t.Fatalf("prompt catalog omitted its version/hash: %+v", promptCatalog)
	}
	foundRepeated := false
	for _, candidate := range promptCatalog.Candidates {
		foundRepeated = foundRepeated || candidate.ObservedSelector == "#results > .card"
	}
	if !foundRepeated {
		t.Fatalf("repeated cohort was not prioritized into bounded evidence: %s", evidence)
	}
	second := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search", root)).PromptEvidenceJSON()
	if evidence != second {
		t.Fatalf("selector evidence is nondeterministic:\nfirst: %s\nsecond: %s", evidence, second)
	}
	for _, selector := range []string{".css-1a2b3c", ".card-a1b2c3", "#item-123456"} {
		if !isVolatileSelector(selector) {
			t.Fatalf("hash-like selector was treated as stable: %s", selector)
		}
	}
}

func TestSelectorEvidenceBoundsFairlyAcrossStatesAndPrioritizesTheLatestState(t *testing.T) {
	homeChildren := make([]map[string]any, 0, 96)
	for index := 0; index < 96; index++ {
		homeChildren = append(homeChildren, selectorElementForTest("span", map[string]string{
			"data-testid": fmt.Sprintf("home-anchor-%c%c", 'a'+rune(index/26), 'a'+rune(index%26)),
		}))
	}
	home := selectorElementForTest("html", nil, selectorElementForTest("body", nil, homeChildren...))
	results := selectorMixedResultRowsForTest(3)
	snapshots := []map[string]any{
		selectorSnapshotForTest(0, "https://example.test/", home),
		selectorSnapshotForTest(1, "https://example.test/", home),
		selectorSnapshotForTest(2, "https://example.test/", home),
		selectorSnapshotForTest(3, "https://example.test/?q=private-one", results),
		selectorSnapshotForTest(4, "https://example.test/?q=private-one", results),
	}
	catalog := selectorCatalogForTest(t, snapshots...)
	if len(catalog.Candidates) != maxSelectorCatalogCandidates {
		t.Fatalf("unexpected bounded prompt catalog size: %d", len(catalog.Candidates))
	}
	if catalog.Candidates[0].State != "https://example.test/?__aegis_query__" {
		t.Fatalf("latest extraction state was not prioritized: %+v", catalog.Candidates[0])
	}
	stateCounts := map[string]int{}
	foundOrganic := false
	for _, candidate := range catalog.Candidates {
		stateCounts[candidate.State]++
		foundOrganic = foundOrganic || candidate.Selector == "#content_left > .result"
	}
	if stateCounts["https://example.test/"] == 0 || stateCounts["https://example.test/?__aegis_query__"] == 0 || !foundOrganic {
		t.Fatalf("bounded catalog starved a recorded state or organic cohort: states=%v candidates=%+v", stateCounts, catalog.Candidates)
	}

	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(catalog.PromptEvidenceJSON()), &promptCatalog); err != nil {
		t.Fatal(err)
	}
	foundOrganic = false
	for _, candidate := range promptCatalog.Candidates {
		foundOrganic = foundOrganic || candidate.ObservedSelector == "#content_left > .result"
	}
	if !foundOrganic {
		t.Fatalf("byte-bounded prompt evidence omitted the latest-state cohort: %s", catalog.PromptEvidenceJSON())
	}
	if strings.Contains(catalog.PromptEvidenceJSON(), "private-one") || strings.Contains(catalog.PromptEvidenceJSON(), "q=") {
		t.Fatalf("selector evidence disclosed a query name or value: %s", catalog.PromptEvidenceJSON())
	}
	second := selectorCatalogForTest(t, snapshots...).PromptEvidenceJSON()
	if second != catalog.PromptEvidenceJSON() {
		t.Fatalf("state-fair prompt evidence is nondeterministic:\nfirst: %s\nsecond: %s", catalog.PromptEvidenceJSON(), second)
	}
}

type selectorCardForTest struct {
	classes string
	title   bool
	summary bool
}

func selectorMixedResultRowsForTest(organicCount int) map[string]any {
	rows := make([]map[string]any, 0, organicCount+1)
	for index := 0; index < organicCount; index++ {
		rows = append(rows, selectorElementForTest("div", map[string]string{
			"id": fmt.Sprintf("organic-%d", index+1), "class": "result c-container new-pmd",
		}, selectorElementForTest("div", map[string]string{"class": "cosc-card"},
			selectorElementForTest("h3", map[string]string{"class": "cosc-title"}, selectorTextForTest("title")),
			selectorElementForTest("div", map[string]string{"class": "cos-color-text-tiny"}, selectorTextForTest("summary")),
		)))
	}
	rows = append(rows, selectorElementForTest("div", map[string]string{
		"id": "special-module", "class": "result-op c-container new-pmd",
	}, selectorElementForTest("section", map[string]string{"class": "knowledge-module"})))
	return selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "content_left"}, rows...),
	))
}

func selectorResultsForTest(id string, cards []selectorCardForTest) map[string]any {
	children := make([]map[string]any, 0, len(cards))
	for _, card := range cards {
		fields := []map[string]any{}
		if card.title {
			fields = append(fields, selectorElementForTest("h2", map[string]string{"class": "title"}, selectorTextForTest("title")))
		}
		if card.summary {
			fields = append(fields, selectorElementForTest("p", map[string]string{"class": "summary"}, selectorTextForTest("summary")))
		}
		children = append(children, selectorElementForTest("article", map[string]string{"class": card.classes}, fields...))
	}
	items := make([]map[string]any, len(children))
	copy(items, children)
	return selectorElementForTest("html", nil, selectorElementForTest("body", nil, selectorElementForTest("main", map[string]string{"id": id}, items...)))
}

func selectorElementForTest(tag string, attrs map[string]string, children ...map[string]any) map[string]any {
	result := map[string]any{"type": "element", "tagName": tag}
	if len(attrs) > 0 {
		keys := make([]string, 0, len(attrs))
		for key := range attrs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]any, 0, len(keys))
		for _, key := range keys {
			values = append(values, map[string]any{"name": key, "value": attrs[key]})
		}
		result["attributes"] = values
	}
	if len(children) > 0 {
		values := make([]any, len(children))
		for index := range children {
			values[index] = children[index]
		}
		result["children"] = values
	}
	return result
}

func selectorTextForTest(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func selectorSnapshotForTest(sequence int, rawURL string, dom map[string]any) map[string]any {
	return map[string]any{
		"sequence": sequence, "url": rawURL, "domTree": dom,
		"capture": map[string]any{"status": "complete"},
	}
}

func selectorCatalogForTest(t *testing.T, snapshots ...map[string]any) *SelectorEvidenceCatalog {
	t.Helper()
	values := make([]any, len(snapshots))
	for index := range snapshots {
		values[index] = snapshots[index]
	}
	catalog, err := BuildSelectorEvidenceCatalog(map[string]any{
		"meta":      map[string]any{"sanitizationVersion": "extension-v2"},
		"snapshots": values,
	})
	if err != nil {
		t.Fatalf("build selector catalog: %v", err)
	}
	return catalog
}

func selectorRuleForTest(step map[string]any) *models.Rule {
	encoded, _ := json.Marshal([]any{step})
	return &models.Rule{Steps: models.JSON(encoded)}
}

func selectorRuleStepForTest(t *testing.T, rule *models.Rule) map[string]any {
	t.Helper()
	var steps []any
	if err := json.Unmarshal(rule.Steps, &steps); err != nil {
		t.Fatal(err)
	}
	return steps[0].(map[string]any)
}
