package rule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/singhand-labs/AegisCrawler/internal/models"
	"golang.org/x/net/html"
)

func TestOpaqueSelectorCandidatesResolveProviderRuleBeforePublicValidation(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search?q=one", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card result", title: true, summary: true},
			{classes: "card result", title: true, summary: true},
		})),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > article.card.result", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": "h2.title"},
			"summary": map[string]any{"type": "text", "selector": "p.summary"},
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
	if promptCatalog.CatalogHash == "" || !strings.Contains(prompt, `"observedSelector":"#results \u003e .card"`) {
		t.Fatalf("prompt omitted its hash/read-only CSS hint: %s", prompt)
	}
	if strings.Contains(prompt, "title one") || strings.Contains(prompt, "summary one") {
		t.Fatalf("page text leaked into prompt catalog: %s", prompt)
	}
	providerStep := selectorRuleStepForTest(t, providerRule)
	providerTarget := providerStep["target"].(map[string]any)
	if providerTarget["rowCandidateId"] == "" || providerTarget["selector"] != nil {
		t.Fatalf("trusted row was not reverse-mapped to an opaque ID: %#v", providerTarget)
	}
	for name, raw := range providerStep["fields"].(map[string]any) {
		field := raw.(map[string]any)
		if field["fieldCandidateId"] == "" || field["selector"] != nil {
			t.Fatalf("trusted field %s was not reverse-mapped: %#v", name, field)
		}
	}

	report, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets != 1 || report.Fields != 2 {
		t.Fatalf("unexpected resolution report: %+v", report)
	}
	resolvedStep := selectorRuleStepForTest(t, providerRule)
	if selector := resolvedStep["target"].(map[string]any)["selector"]; selector != "#results > .card" {
		t.Fatalf("opaque row did not resolve to canonical CSS: %v", selector)
	}
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(providerRule, catalog); err != nil {
		t.Fatalf("resolved public rule failed defense-in-depth evidence validation: %v", err)
	}
	encoded, _ := json.Marshal(providerRule)
	for _, forbidden := range []string{"rowCandidateId", "targetCandidateId", "fieldCandidateId"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("provider-only %s survived resolution: %s", forbidden, encoded)
		}
	}
}

func TestOpaqueSelectorCandidatesPreferExactSemanticMainTarget(t *testing.T) {
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{
			"id": "content", "class": "main-page-content",
		},
			selectorElementForTest("h1", nil, selectorTextForTest("CSS grid layout")),
			selectorElementForTest("p", nil, selectorTextForTest("Introduction")),
			selectorElementForTest("h2", nil, selectorTextForTest("Grid layout in action")),
		),
	))
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/guide", root),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "summary", "multiple": false,
		"target": map[string]any{"selector": "#content", "visible": true},
		"fields": map[string]any{
			"title":         map[string]any{"type": "text", "selector": "h1"},
			"introduction":  map[string]any{"type": "text", "selector": "p"},
			"first_section": map[string]any{"type": "text", "selector": "h2"},
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
	foundMain := false
	for _, candidate := range promptCatalog.Candidates {
		if candidate.ObservedSelector == "main" {
			foundMain = true
			break
		}
	}
	if !foundMain {
		t.Fatalf("provider catalog did not canonicalize exact main evidence: %s", prompt)
	}
	if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); err != nil {
		t.Fatal(err)
	}
	target := selectorRuleStepForTest(t, providerRule)["target"].(map[string]any)
	if target["selector"] != "main" {
		t.Fatalf("opaque main target resolved to %v", target["selector"])
	}
	if _, err := ValidateAndStabilizeGeneratedExtractionSelectors(providerRule, catalog); err != nil {
		t.Fatal(err)
	}
	if target["selector"] != "main" {
		t.Fatalf("authenticated semantic main was restabilized to %v", target["selector"])
	}
}

func TestPrioritizeSemanticRelativeSelectorsKeepsMDNFieldsWithinBound(t *testing.T) {
	input := []string{
		"#one", ".two", "[role=x]", "div", "span", "section", "article",
		"footer", "header", "nav", "table", "dl", "h1", "h1 ~ * p", "h2#first_section",
	}
	got := prioritizeSemanticRelativeSelectors(input)
	want := []string{"h1", "h1 ~ * p", "h2#first_section"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("semantic priority = %v", got[:3])
		}
	}
}

func TestSemanticV4RelativeSelectorsFindsExactMDNFields(t *testing.T) {
	dom := selectorElementForTest("main", nil,
		selectorElementForTest("h1", nil, selectorTextForTest("CSS grid layout")),
		selectorElementForTest("p", nil, selectorTextForTest("Introduction")),
		selectorElementForTest("h2", map[string]string{"id": "grid_layout_in_action"},
			selectorTextForTest("Grid layout in action")),
		selectorElementForTest("h2", map[string]string{"id": "other_section"},
			selectorTextForTest("Other")),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", dom))
	var root *html.Node
	walkElements(catalog.snapshots[0].root, func(node *html.Node) {
		if strings.EqualFold(node.Data, "main") {
			root = node
		}
	})
	if root == nil {
		t.Fatal("fixture omitted main")
	}
	selectors := semanticV4RelativeSelectors([][]*html.Node{{root}})
	for _, expected := range []string{"h1", "h1 + p", "h2#grid_layout_in_action"} {
		if !slices.Contains(selectors, expected) {
			t.Fatalf("semantic candidates %v omitted %s", selectors, expected)
		}
	}
}

func TestSemanticV4RelativeSelectorsFindsNestedHeadingIntroduction(t *testing.T) {
	dom := selectorElementForTest("main", nil,
		selectorElementForTest("h1", nil, selectorTextForTest("Guide")),
		selectorElementForTest("div", map[string]string{"class": "header-copy"},
			selectorElementForTest("p", nil, selectorTextForTest("Introduction"))),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", dom))
	var root *html.Node
	walkElements(catalog.snapshots[0].root, func(node *html.Node) {
		if strings.EqualFold(node.Data, "main") {
			root = node
		}
	})
	selectors := semanticV4RelativeSelectors([][]*html.Node{{root}})
	if !slices.Contains(selectors, "h1 + * p") {
		t.Fatalf("nested introduction candidates = %v", selectors)
	}
}

func TestSemanticV4RelativeSelectorsFindsIntroductionAcrossWrapperBranches(t *testing.T) {
	dom := selectorElementForTest("main", nil,
		selectorElementForTest("div", map[string]string{"class": "layout-header"},
			selectorElementForTest("h1", nil, selectorTextForTest("Guide"))),
		selectorElementForTest("div", map[string]string{"class": "layout-body"},
			selectorElementForTest("section", map[string]string{"class": "intro"},
				selectorElementForTest("p", nil, selectorTextForTest("Introduction"))),
			selectorElementForTest("section", map[string]string{"class": "details"},
				selectorElementForTest("p", nil, selectorTextForTest("Details")))),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", dom))
	var root *html.Node
	walkElements(catalog.snapshots[0].root, func(node *html.Node) {
		if strings.EqualFold(node.Data, "main") {
			root = node
		}
	})
	selectors := semanticV4RelativeSelectors([][]*html.Node{{root}})
	want := ".layout-body > .intro > p"
	if !slices.Contains(selectors, want) {
		t.Fatalf("cross-wrapper introduction candidates = %v; want %q", selectors, want)
	}
}

func TestSemanticV4RelativeSelectorsFindsFirstIntroductionAmongSiblingParagraphs(t *testing.T) {
	dom := selectorElementForTest("main", nil,
		selectorElementForTest("div", map[string]string{"class": "layout__header"},
			selectorElementForTest("div", map[string]string{"class": "heading-section"},
				selectorElementForTest("h1", nil, selectorTextForTest("Guide"))),
			selectorElementForTest("div", map[string]string{"class": "content-section"},
				selectorElementForTest("p", nil, selectorTextForTest("Introduction")),
				selectorElementForTest("p", nil, selectorTextForTest("Compatibility note")))),
		selectorElementForTest("div", map[string]string{"class": "layout__body"},
			selectorElementForTest("h2", map[string]string{"id": "first-section"},
				selectorTextForTest("First section"))),
	)
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", dom))
	var root *html.Node
	walkElements(catalog.snapshots[0].root, func(node *html.Node) {
		if strings.EqualFold(node.Data, "main") {
			root = node
		}
	})
	selectors := semanticV4RelativeSelectors([][]*html.Node{{root}})
	want := ".layout__header > .content-section > p:not(p + p)"
	if !slices.Contains(selectors, want) {
		t.Fatalf("sibling-paragraph introduction candidates = %v; want %q", selectors, want)
	}
}

func TestOpaqueSelectorCandidatesExposeCrossWrapperIntroductionOnMain(t *testing.T) {
	dom := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "content", "class": "main-content"},
			selectorElementForTest("div", map[string]string{"class": "layout-header"},
				selectorElementForTest("h1", nil, selectorTextForTest("Guide"))),
			selectorElementForTest("div", map[string]string{"class": "layout-body"},
				selectorElementForTest("section", map[string]string{"class": "intro"},
					selectorElementForTest("p", nil, selectorTextForTest("Introduction")),
					selectorElementForTest("p", nil, selectorTextForTest("Compatibility note"))),
				selectorElementForTest("h2", map[string]string{"id": "first-section"},
					selectorTextForTest("First section"))),
		),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/guide", dom))
	prompt, _, err := catalog.PrepareProviderPrompt(selectorRuleForTest(map[string]any{
		"action": "scrollBy", "direction": "down", "distance": 1, "unit": "pages",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &promptCatalog); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range promptCatalog.Candidates {
		if candidate.ObservedSelector != "main" {
			continue
		}
		selectors := []string{}
		for _, field := range candidate.FieldCandidates {
			selectors = append(selectors, field.ObservedRelativeSelector)
		}
		for _, want := range []string{"h1", ".layout-body > .intro > p:not(p + p)", "h2#first-section"} {
			if !slices.Contains(selectors, want) {
				t.Fatalf("main field candidates = %v; want %q", selectors, want)
			}
		}
		return
	}
	t.Fatalf("provider prompt omitted main candidate: %s", prompt)
}

func TestOpaqueSelectorCandidatesRejectRawBaiduRowAndAbstractSelectors(t *testing.T) {
	rows := make([]map[string]any, 0, 3)
	for index := 0; index < 3; index++ {
		rows = append(rows, selectorElementForTest("div", map[string]string{
			"id": "result-" + string(rune('a'+index)), "class": "result c-container",
		},
			selectorElementForTest("h3", map[string]string{"class": "t"}, selectorTextForTest("title")),
			selectorElementForTest("div", map[string]string{"class": "c-row"}),
			selectorElementForTest("div", map[string]string{"class": "c-abstract"}, selectorTextForTest("summary")),
		))
	}
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "content_left"}, rows...),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://www.baidu.com/s", root))
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#content_left > .result", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".t"},
			"summary": map[string]any{"type": "text", "selector": ".c-abstract"},
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
	mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
		target := step["target"].(map[string]any)
		delete(target, "rowCandidateId")
		target["selector"] = ".c-abstract"
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "exactly one opaque selector candidate ID") {
		t.Fatalf("provider-authored .c-abstract row selector was not rejected: %v", err)
	}

	_, providerRule, err = catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
		summary := step["fields"].(map[string]any)["summary"].(map[string]any)
		delete(summary, "fieldCandidateId")
		summary["selector"] = ".c-row"
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "fieldCandidateId is required") {
		t.Fatalf("provider-authored empty .c-row field selector was not rejected: %v", err)
	}
}

func TestOpaqueSelectorCandidatesCanonicalizeOnlyRedundantAuthenticatedLocatorHints(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		})),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": ".title"},
		},
	})
	prompt, _, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &promptCatalog); err != nil {
		t.Fatal(err)
	}

	t.Run("known scoped IDs retain private selectors", func(t *testing.T) {
		_, providerRule, err := catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			target := step["target"].(map[string]any)
			target["selector"] = "#attacker"
			target["stableSelector"] = "#copied-stable"
			target["observedSelector"] = "#copied-observed"
			field := step["fields"].(map[string]any)["title"].(map[string]any)
			field["selector"] = ".attacker"
			field["stableSelector"] = ".copied-stable"
			field["observedSelector"] = ".copied-observed"
			field["observedRelativeSelector"] = ".copied-relative"
		})

		report, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash)
		if err != nil {
			t.Fatal(err)
		}
		if report.Targets != 1 || report.Fields != 1 {
			t.Fatalf("unexpected resolution report: %+v", report)
		}
		step := selectorRuleStepForTest(t, providerRule)
		target := step["target"].(map[string]any)
		field := step["fields"].(map[string]any)["title"].(map[string]any)
		if target["selector"] != "#results > .card" {
			t.Fatalf("provider target hint replaced private selector: %#v", target)
		}
		if field["selector"] != ".title" {
			t.Fatalf("provider field hint replaced private selector: %#v", field)
		}
		encoded, _ := json.Marshal(step)
		for _, forbidden := range []string{
			"#attacker", "#copied-stable", "#copied-observed",
			".attacker", ".copied-stable", ".copied-observed", ".copied-relative",
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("provider locator hint survived canonicalization: %s", encoded)
			}
		}
	})

	t.Run("named field arrays canonicalize before existing ID validation", func(t *testing.T) {
		_, providerRule, err := catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			field := step["fields"].(map[string]any)["title"].(map[string]any)
			field["name"] = "title"
			step["fields"] = []any{field}
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(
			providerRule,
			promptCatalog.CatalogHash,
		); err != nil {
			t.Fatal(err)
		}
		step := selectorRuleStepForTest(t, providerRule)
		fields, ok := step["fields"].(map[string]any)
		if !ok || len(fields) != 1 {
			t.Fatalf("named field array did not canonicalize to a field map: %#v", step["fields"])
		}
		field := fields["title"].(map[string]any)
		if field["name"] != nil || field["selector"] != ".title" || field["visible"] != true {
			t.Fatalf("canonical field did not pass normal private resolution: %#v", field)
		}
	})

	t.Run("invalid field arrays fail closed", func(t *testing.T) {
		for name, testCase := range map[string]struct {
			mutate     func(map[string]any)
			diagnostic string
		}{
			"missing name": {
				mutate: func(field map[string]any) {
					delete(field, "name")
				},
				diagnostic: ".name must be a non-empty exact field name",
			},
			"unknown candidate": {
				mutate: func(field map[string]any) {
					field["name"] = "title"
					field["fieldCandidateId"] = "field_unknown"
				},
				diagnostic: "unknown or cross-target",
			},
		} {
			t.Run(name, func(t *testing.T) {
				_, providerRule, err := catalog.PrepareProviderPrompt(trusted)
				if err != nil {
					t.Fatal(err)
				}
				mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
					field := step["fields"].(map[string]any)["title"].(map[string]any)
					field["name"] = "title"
					testCase.mutate(field)
					step["fields"] = []any{field}
				})
				if _, err := catalog.ResolveProviderExtractionCandidates(
					providerRule,
					promptCatalog.CatalogHash,
				); !errors.Is(err, ErrInvalidProvisionalRule) ||
					!strings.Contains(err.Error(), testCase.diagnostic) {
					t.Fatalf("invalid field array was accepted: %v", err)
				}
			})
		}

		_, providerRule, err := catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			first := step["fields"].(map[string]any)["title"].(map[string]any)
			first["name"] = "title"
			encoded, _ := json.Marshal(first)
			var duplicate map[string]any
			_ = json.Unmarshal(encoded, &duplicate)
			step["fields"] = []any{first, duplicate}
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(
			providerRule,
			promptCatalog.CatalogHash,
		); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), "duplicate field name") {
			t.Fatalf("duplicate field names were accepted: %v", err)
		}
	})

	t.Run("missing IDs cannot be inferred from hints", func(t *testing.T) {
		_, providerRule, err := catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			target := step["target"].(map[string]any)
			delete(target, "rowCandidateId")
			target["selector"] = "#results > .card"
			target["stableSelector"] = "#results > .card"
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), "exactly one opaque selector candidate ID") {
			t.Fatalf("raw target hints authorized a missing candidate ID: %v", err)
		}

		_, providerRule, err = catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			field := step["fields"].(map[string]any)["title"].(map[string]any)
			delete(field, "fieldCandidateId")
			field["selector"] = ".title"
			field["observedRelativeSelector"] = ".title"
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), "fieldCandidateId is required") {
			t.Fatalf("raw field hints authorized a missing candidate ID: %v", err)
		}
	})

	t.Run("unknown conflicting and unrelated keys still fail closed", func(t *testing.T) {
		_, providerRule, err := catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			target := step["target"].(map[string]any)
			target["rowCandidateId"] = "row_1"
			target["stableSelector"] = "#results > .card"
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), "unknown or out-of-scope") {
			t.Fatalf("locator hint authorized an unknown candidate ID: %v", err)
		}

		_, providerRule, err = catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			target := step["target"].(map[string]any)
			target["targetCandidateId"] = "t_conflict"
			target["stableSelector"] = "#results > .card"
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), "exactly one opaque selector candidate ID") {
			t.Fatalf("conflicting candidate IDs were accepted: %v", err)
		}

		_, providerRule, err = catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			target := step["target"].(map[string]any)
			target["stableSelector"] = "#results > .card"
			target["xpath"] = "//article"
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), "target.xpath is forbidden") {
			t.Fatalf("unrelated locator key was canonicalized away: %v", err)
		}

		_, providerRule, err = catalog.PrepareProviderPrompt(trusted)
		if err != nil {
			t.Fatal(err)
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			field := step["fields"].(map[string]any)["title"].(map[string]any)
			field["stableSelector"] = ".title"
			field["xpath"] = ".//h2"
		})
		if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), ".xpath is forbidden or ignored") {
			t.Fatalf("unrelated field key was canonicalized away: %v", err)
		}
	})

	t.Run("field hints cannot cross target scope", func(t *testing.T) {
		primary := selectorResultsForTest("primary", []selectorCardForTest{
			{classes: "card primary", title: true},
			{classes: "card primary", title: true},
		})
		decoys := selectorResultsForTest("decoys", []selectorCardForTest{
			{classes: "decoy", title: true},
			{classes: "decoy", title: true},
		})
		scopedCatalog := selectorCatalogForTest(t, selectorSnapshotForTest(
			0, "https://example.test/search",
			selectorElementForTest("html", nil, selectorElementForTest("body", nil, primary, decoys)),
		))
		scopedTrusted := selectorRuleForTest(map[string]any{
			"action": "extract", "name": "rows", "multiple": true,
			"target": map[string]any{"selector": "#primary > .card"},
			"fields": map[string]any{
				"title": map[string]any{"type": "text", "selector": "h2.title"},
			},
		})
		scopedPrompt, providerRule, err := scopedCatalog.PrepareProviderPrompt(scopedTrusted)
		if err != nil {
			t.Fatal(err)
		}
		var scopedPromptCatalog SelectorPromptCatalog
		if err := json.Unmarshal([]byte(scopedPrompt), &scopedPromptCatalog); err != nil {
			t.Fatal(err)
		}
		wrongFieldID := ""
		for _, candidate := range scopedPromptCatalog.Candidates {
			if candidate.ObservedSelector != "#decoys > .decoy" {
				continue
			}
			for _, field := range candidate.FieldCandidates {
				if field.ParentFieldCandidateID == "" {
					wrongFieldID = field.FieldCandidateID
					break
				}
			}
		}
		if wrongFieldID == "" {
			t.Fatal("fixture did not expose a decoy field candidate")
		}
		mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
			field := step["fields"].(map[string]any)["title"].(map[string]any)
			field["fieldCandidateId"] = wrongFieldID
			field["selector"] = "h2.title"
			field["stableSelector"] = "h2.title"
		})
		if _, err := scopedCatalog.ResolveProviderExtractionCandidates(
			providerRule,
			scopedPromptCatalog.CatalogHash,
		); !errors.Is(err, ErrInvalidProvisionalRule) ||
			!strings.Contains(err.Error(), "unknown or cross-target") {
			t.Fatalf("locator hints authorized a cross-target field candidate: %v", err)
		}
	})
}

func TestOpaqueSelectorCandidatesFailClosedOnHashScopeCardinalityAndType(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		})),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{"title": map[string]any{"type": "text", "selector": ".title"}},
	})
	prompt, providerRule, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &promptCatalog); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, "stale"); !errors.Is(err, ErrInvalidProvisionalRule) {
		t.Fatalf("stale catalog hash was accepted: %v", err)
	}

	_, providerRule, err = catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
		step["target"].(map[string]any)["rowCandidateId"] = "row_1"
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "unknown or out-of-scope") {
		t.Fatalf("unknown row ID was accepted: %v", err)
	}

	_, providerRule, err = catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
		step["multiple"] = false
	})
	if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "single extraction requires targetCandidateId") {
		t.Fatalf("row ID was accepted with single cardinality: %v", err)
	}

	emptyRows := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("main", map[string]string{"id": "items"},
			selectorElementForTest("article", map[string]string{"class": "item"}, selectorElementForTest("div", map[string]string{"class": "layout"})),
			selectorElementForTest("article", map[string]string{"class": "item"}, selectorElementForTest("div", map[string]string{"class": "layout"})),
		),
	))
	emptyCatalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/items", emptyRows))
	emptyTrusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#items > .item", "visible": true},
		"fields": map[string]any{"exists": map[string]any{"type": "exists", "selector": ".layout"}},
	})
	emptyPrompt, emptyProvider, err := emptyCatalog.PrepareProviderPrompt(emptyTrusted)
	if err != nil {
		t.Fatal(err)
	}
	var emptyPromptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(emptyPrompt), &emptyPromptCatalog); err != nil {
		t.Fatal(err)
	}
	mutateSelectorCandidateRuleStep(t, emptyProvider, func(step map[string]any) {
		step["fields"].(map[string]any)["exists"].(map[string]any)["type"] = "text"
	})
	if _, err := emptyCatalog.ResolveProviderExtractionCandidates(emptyProvider, emptyPromptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "empty recorded text") {
		t.Fatalf("empty text/type mismatch was accepted: %v", err)
	}
}

func TestOpaqueSelectorCandidateResolutionDoesNotMutateProviderRuleOnFailure(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://example.test/search", selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		})),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card"},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".title"},
			"summary": map[string]any{"type": "text", "selector": ".summary"},
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
	mutateSelectorCandidateRuleStep(t, providerRule, func(step map[string]any) {
		// "summary" resolves before "title" in the stable field ordering, so
		// this failure occurs only after earlier target/field work has run.
		step["fields"].(map[string]any)["title"].(map[string]any)["fieldCandidateId"] = "field_unknown"
	})
	before, err := json.Marshal(providerRule)
	if err != nil {
		t.Fatal(err)
	}
	beforeSteps := append(models.JSON(nil), providerRule.Steps...)

	if _, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "unknown or cross-target") {
		t.Fatalf("expected unknown field candidate failure, got %v", err)
	}
	after, err := json.Marshal(providerRule)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) || !bytes.Equal(providerRule.Steps, beforeSteps) {
		t.Fatalf("failed resolution mutated provider rule:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestOpaqueSelectorCandidateIDsAndCatalogHashAreDeterministicAndSelectorIndependent(t *testing.T) {
	root := selectorResultsForTest("results", []selectorCardForTest{
		{classes: "card", title: true, summary: true},
		{classes: "card", title: true, summary: true},
	})
	first := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search?q=one", root))
	second := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test/search?q=two", root))
	firstJSON := first.PromptEvidenceJSON()
	secondJSON := second.PromptEvidenceJSON()
	if firstJSON != secondJSON {
		t.Fatalf("query-only URL change altered evidence-derived IDs/hash:\n%s\n%s", firstJSON, secondJSON)
	}
	if len(firstJSON) > maxSelectorEvidencePromptBytes {
		t.Fatalf("opaque prompt catalog exceeded byte bound: %d", len(firstJSON))
	}
	var decoded SelectorPromptCatalog
	if err := json.Unmarshal([]byte(firstJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != selectorPromptCatalogVersion {
		t.Fatalf("new selector catalog did not use the current version: %q", decoded.Version)
	}
	for _, candidate := range decoded.Candidates {
		if candidate.RowCandidateID != "" &&
			(!strings.HasPrefix(candidate.RowCandidateID, "r_") || len(candidate.RowCandidateID) != 14) {
			t.Fatalf("unexpected row candidate ID: %q", candidate.RowCandidateID)
		}
		if candidate.TargetCandidateID != "" &&
			(!strings.HasPrefix(candidate.TargetCandidateID, "t_") || len(candidate.TargetCandidateID) != 14) {
			t.Fatalf("unexpected target candidate ID: %q", candidate.TargetCandidateID)
		}
		for _, field := range candidate.FieldCandidates {
			if !strings.HasPrefix(field.FieldCandidateID, "f_") || len(field.FieldCandidateID) != 14 {
				t.Fatalf("unexpected field candidate ID: %q", field.FieldCandidateID)
			}
		}
	}
}

const selectorCatalogV3GoldenPrompt = `{"version":"selector-catalog-v3","catalogHash":"Eb9KN8rDpt13Jp8b4XG0WZl_CkOZfnT6Cg_PFkvCevk","candidates":[{"rowCandidateId":"r_k7El6mSppYtM","observedSelector":"#results \u003e .card","state":"https://example.test/search","snapshotSequences":[0],"cardinalities":[2],"fieldCandidates":[{"fieldCandidateId":"f_cdccq4nJqr03","observedRelativeSelector":".summary","supportedTypes":["attr","boolean","count","exists","html","regex","text"],"nonEmptyText":true},{"fieldCandidateId":"f_2OLQi3QDrCTa","observedRelativeSelector":".title","supportedTypes":["attr","boolean","count","exists","html","regex","text"],"nonEmptyText":true},{"fieldCandidateId":"f_k-_sQ0N2U_vh","supportedTypes":["attr","boolean","count","exists","html","regex","text"],"nonEmptyText":true},{"fieldCandidateId":"f_kkTsAiVtiGxh","parentFieldCandidateId":"f_2OLQi3QDrCTa","supportedTypes":["attr","boolean","count","exists","html","regex","text"],"nonEmptyText":true},{"fieldCandidateId":"f_CFJR3EFuNFsM","parentFieldCandidateId":"f_cdccq4nJqr03","supportedTypes":["attr","boolean","count","exists","html","regex","text"],"nonEmptyText":true}]},{"targetCandidateId":"t_4mXOaiwJbd2o","observedSelector":"#results","state":"https://example.test/search","snapshotSequences":[0],"cardinalities":[1],"fieldCandidates":[{"fieldCandidateId":"f_OI1yoep-vsZR","supportedTypes":["attr","boolean","count","exists","html","regex","text"],"nonEmptyText":true}]}]}`

const selectorCatalogV3GoldenProviderRule = `{"id":"","workspaceId":"","version":"","name":"","domain":{},"urlPattern":{},"enabled":false,"priority":"","entry":"","variables":{},"selectors":{},"humanize":{},"steps":[{"action":"extract","fields":{"summary":{"fieldCandidateId":"f_cdccq4nJqr03","type":"text"},"title":{"fieldCandidateId":"f_2OLQi3QDrCTa","type":"text"}},"multiple":true,"name":"rows","target":{"rowCandidateId":"r_k7El6mSppYtM","visible":true}}],"output":{},"sendPolicy":{},"hooks":{},"tags":{},"owner":"","approvalStatus":"","source":"","createdAt":"0001-01-01T00:00:00Z","updatedAt":"0001-01-01T00:00:00Z"}`

func TestOpaqueSelectorCandidatesDeduplicateExactCanonicalTargetIDs(t *testing.T) {
	root := selectorElementForTest("html", nil,
		selectorElementForTest("body", nil,
			selectorElementForTest("div", map[string]string{"class": "b_respl"},
				selectorElementForTest("div", map[string]string{
					"id": "first", recordedRenderedAttribute: "true",
				}),
				selectorElementForTest("div", map[string]string{
					"id": "second", recordedRenderedAttribute: "true",
				}),
			),
		),
	)
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(26, "https://www.bing.com/search?q=tides", root),
	)
	state := catalog.snapshots[0].state
	catalog.Candidates = []SelectorEvidenceCandidate{
		{
			Selector: ".b_respl > div", State: state,
			SnapshotSequences: []int{26}, Cardinalities: []int{2},
		},
		{
			Selector: ".b_respl > div[id]", State: state,
			SnapshotSequences: []int{26}, Cardinalities: []int{2},
		},
	}
	catalog.validationCandidates = nil

	var prompt SelectorPromptCatalog
	if err := json.Unmarshal([]byte(catalog.PromptEvidenceJSON()), &prompt); err != nil {
		t.Fatal(err)
	}
	if len(prompt.Candidates) != 1 {
		t.Fatalf("exact canonical target was emitted more than once: %+v", prompt.Candidates)
	}
	candidate := prompt.Candidates[0]
	if candidate.RowCandidateID == "" || candidate.ObservedSelector != ".b_respl > div" {
		t.Fatalf("unexpected canonical target: %+v", candidate)
	}
}

func TestSelectorCatalogV3TrustedReconstructionMatchesPreV4Golden(t *testing.T) {
	snapshot := selectorSnapshotForTest(
		0,
		"https://example.test/search",
		selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		}),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card", "visible": true},
		"fields": map[string]any{
			"title":   map[string]any{"type": "text", "selector": ".title"},
			"summary": map[string]any{"type": "text", "selector": ".summary"},
		},
	})
	catalog := selectorCatalogForTest(t, snapshot)
	prompt, provider, err := catalog.prepareProviderPrompt(
		trusted,
		selectorPromptCatalogVersionV3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != selectorCatalogV3GoldenPrompt {
		t.Fatalf("v3 prompt changed from the pre-v4 golden:\nwant: %s\ngot:  %s",
			selectorCatalogV3GoldenPrompt, prompt)
	}
	providerJSON, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	if string(providerJSON) != selectorCatalogV3GoldenProviderRule {
		t.Fatalf("v3 provider rule changed from the pre-v4 golden:\nwant: %s\ngot:  %s",
			selectorCatalogV3GoldenProviderRule, providerJSON)
	}

	rebuilt := selectorCatalogForTest(t, snapshot)
	rebuiltPrompt, rebuiltProvider, err := rebuilt.ReconstructProviderPrompt(
		selectorCatalogV3GoldenPrompt,
		trusted,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltPrompt != selectorCatalogV3GoldenPrompt {
		t.Fatalf("stored v3 prompt was not reconstructed byte-for-byte:\nwant: %s\ngot:  %s",
			selectorCatalogV3GoldenPrompt, rebuiltPrompt)
	}
	rebuiltProviderJSON, err := json.Marshal(rebuiltProvider)
	if err != nil {
		t.Fatal(err)
	}
	if string(rebuiltProviderJSON) != selectorCatalogV3GoldenProviderRule {
		t.Fatalf("stored v3 trusted provider rule changed:\nwant: %s\ngot:  %s",
			selectorCatalogV3GoldenProviderRule, rebuiltProviderJSON)
	}
	var golden SelectorPromptCatalog
	if err := json.Unmarshal([]byte(selectorCatalogV3GoldenPrompt), &golden); err != nil {
		t.Fatal(err)
	}
	report, err := rebuilt.ResolveProviderExtractionCandidates(
		rebuiltProvider,
		golden.CatalogHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets != 1 || report.Fields != 2 {
		t.Fatalf("reconstructed v3 provider rule did not resolve exactly: %+v", report)
	}
}

func TestSelectorCatalogV4DerivesAnchorlessDocumentRootCandidatesAndPreservesV2(t *testing.T) {
	quote := func() map[string]any {
		return selectorElementForTest("div", map[string]string{"class": "quote"},
			selectorElementForTest("span", map[string]string{"class": "text"}, selectorTextForTest("quote")),
			selectorElementForTest("small", map[string]string{"class": "author"}, selectorTextForTest("author")),
			selectorElementForTest("a", map[string]string{"href": "/author/example/"}, selectorTextForTest("about")),
		)
	}
	snapshot := selectorSnapshotForTest(
		0,
		"https://quotes.example/tag/love/",
		selectorElementForTest("html", nil,
			selectorElementForTest("body", nil,
				selectorElementForTest("div", map[string]string{"class": "container"},
					selectorElementForTest("div", map[string]string{"class": "row"},
						selectorElementForTest("div", map[string]string{"class": "results"}, quote(), quote()),
					),
				),
			),
		),
	)
	catalog := selectorCatalogForTest(t, snapshot)
	var current SelectorPromptCatalog
	if err := json.Unmarshal([]byte(catalog.PromptEvidenceJSON()), &current); err != nil {
		t.Fatal(err)
	}
	if current.Version != selectorPromptCatalogVersion {
		t.Fatalf("anchorless catalog did not use the current version: %q", current.Version)
	}
	var repeated *SelectorPromptTargetCandidate
	for index := range current.Candidates {
		candidate := &current.Candidates[index]
		if candidate.RowCandidateID != "" &&
			len(candidate.Cardinalities) == 1 &&
			candidate.Cardinalities[0] == 2 {
			repeated = candidate
			break
		}
	}
	if repeated == nil || repeated.RowCandidateID == "" || len(repeated.FieldCandidates) < 3 {
		t.Fatalf("document-root anchor did not expose the repeated quote cohort: %+v", current.Candidates)
	}
	if !strings.HasPrefix(repeated.ObservedSelector, "html > ") {
		t.Fatalf("anchorless cohort was not rooted at the unique document element: %+v", repeated)
	}
	for _, candidate := range current.Candidates {
		if candidate.ObservedSelector == "html" {
			t.Fatalf("implicit document anchor leaked as a standalone provider target: %+v", candidate)
		}
	}

	legacyCatalog := selectorCatalogForTest(t, snapshot)
	legacyPrompt, _, err := legacyCatalog.prepareProviderPrompt(nil, selectorPromptCatalogVersionV2)
	if err != nil {
		t.Fatal(err)
	}
	var legacy SelectorPromptCatalog
	if err := json.Unmarshal([]byte(legacyPrompt), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Version != selectorPromptCatalogVersionV2 || len(legacy.Candidates) != 0 {
		t.Fatalf("v2 anchorless catalog was reinterpreted: %s", legacyPrompt)
	}
	rebuilt := selectorCatalogForTest(t, snapshot)
	rebuiltPrompt, _, err := rebuilt.ReconstructProviderPrompt(legacyPrompt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltPrompt != legacyPrompt {
		t.Fatalf("stored v2 anchorless catalog changed:\noriginal: %s\nrebuilt:  %s", legacyPrompt, rebuiltPrompt)
	}
}

func TestSelectorCatalogV4PrioritizesLargeAnchorlessContentCohortWithinPromptBound(t *testing.T) {
	quotes := make([]map[string]any, 0, 10)
	for index := 0; index < 10; index++ {
		children := []map[string]any{
			selectorElementForTest("span", map[string]string{"class": "text"}, selectorTextForTest("quote")),
			selectorElementForTest("small", map[string]string{"class": "author"}, selectorTextForTest("author")),
			selectorElementForTest("a", map[string]string{"href": "/author/example/"}, selectorTextForTest("about")),
		}
		for _, suffix := range []string{
			"aa", "bb", "cc", "dd", "ee", "ff", "gg", "hh", "ii", "jj",
			"kk", "ll", "mm", "nn", "oo", "pp", "qq", "rr", "ss", "tt",
		} {
			children = append(children, selectorElementForTest(
				"span",
				map[string]string{"class": "detail-" + suffix},
				selectorElementForTest("em", nil, selectorTextForTest("detail")),
			))
		}
		quotes = append(quotes, selectorElementForTest("div", map[string]string{"class": "quote"}, children...))
	}
	page := selectorElementForTest("html", nil,
		selectorElementForTest("body", nil,
			selectorElementForTest("div", map[string]string{"class": "container"},
				selectorElementForTest("div", map[string]string{"class": "row header-box"},
					selectorElementForTest("div", map[string]string{"class": "col-md-8"}),
					selectorElementForTest("div", map[string]string{"class": "col-md-4"}),
				),
				selectorElementForTest("div", map[string]string{"class": "row"},
					selectorElementForTest("div", map[string]string{"class": "col-md-8"}, quotes...),
					selectorElementForTest("div", map[string]string{"class": "col-md-4"},
						selectorElementForTest("a", map[string]string{"class": "tag"}, selectorTextForTest("tag")),
					),
				),
			),
			selectorElementForTest("div", map[string]string{"class": "footer"},
				selectorElementForTest("div", map[string]string{"class": "container"},
					selectorElementForTest("p", nil), selectorElementForTest("p", nil),
				),
			),
		),
	)
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://quotes.example/tag/love/", page),
		selectorSnapshotForTest(1, "https://quotes.example/tag/love/", page),
		selectorSnapshotForTest(2, "https://quotes.example/tag/love/", page),
	)
	var prompt SelectorPromptCatalog
	if err := json.Unmarshal([]byte(catalog.PromptEvidenceJSON()), &prompt); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range prompt.Candidates {
		if minIntSlice(candidate.Cardinalities) == 10 {
			if candidate.RowCandidateID == "" ||
				len(candidate.FieldCandidates) < 3 ||
				len(candidate.FieldCandidates) > maxPromptV3FieldsPerTarget {
				t.Fatalf("large content cohort lacks typed candidates: %+v", candidate)
			}
			return
		}
	}
	t.Fatalf("bounded current prompt dropped the ten-row content cohort: %+v", prompt.Candidates)
}

func TestSelectorCatalogV4IncludesDeepBooksCohortAndPreservesV3(t *testing.T) {
	book := func(index int) map[string]any {
		href := fmt.Sprintf("/catalogue/book-%d_%d/index.html", index, index)
		ratings := []string{"One", "Two", "Three", "Four", "Five"}
		return selectorElementForTest("li", map[string]string{"class": "col-xs-6"},
			selectorElementForTest("article", map[string]string{"class": "product_pod"},
				selectorElementForTest("div", map[string]string{"class": "image_container"},
					selectorElementForTest("a", map[string]string{"href": href},
						selectorElementForTest("img", map[string]string{"alt": fmt.Sprintf("Book %d", index)}),
					),
				),
				selectorElementForTest("p", map[string]string{
					"class": "star-rating " + ratings[(index-1)%len(ratings)],
				}),
				selectorElementForTest("h3", nil,
					selectorElementForTest("a", map[string]string{
						"href": href, "title": fmt.Sprintf("Book %d", index),
					}, selectorTextForTest(fmt.Sprintf("Book %d", index))),
				),
				selectorElementForTest("div", map[string]string{"class": "product_price"},
					selectorElementForTest("p", map[string]string{"class": "price_color"},
						selectorTextForTest("£12.34")),
					selectorElementForTest("p", map[string]string{"class": "availability"},
						selectorTextForTest("In stock")),
				),
			),
		)
	}
	books := make([]map[string]any, 0, 20)
	for index := 1; index <= 20; index++ {
		books = append(books, book(index))
	}
	page := selectorElementForTest("html", nil,
		selectorElementForTest("body", map[string]string{"id": "default"},
			selectorElementForTest("div", map[string]string{"class": "page"},
				selectorElementForTest("div", map[string]string{"class": "page_inner"},
					selectorElementForTest("div", map[string]string{"class": "row"},
						selectorElementForTest("div", map[string]string{"class": "col-md-9"},
							selectorElementForTest("section", nil,
								selectorElementForTest("div", nil,
									selectorElementForTest("ol", map[string]string{"class": "row"}, books...),
								),
							),
						),
					),
				),
			),
		),
	)
	snapshot := selectorSnapshotForTest(
		0,
		"https://books.example/catalogue/category/books/mystery_3/index.html",
		page,
	)
	catalog := selectorCatalogForTest(t, snapshot)
	var current SelectorPromptCatalog
	if err := json.Unmarshal([]byte(catalog.PromptEvidenceJSON()), &current); err != nil {
		t.Fatal(err)
	}
	if current.Version != selectorPromptCatalogVersion {
		t.Fatalf("deep Books catalog did not use the current version: %q", current.Version)
	}
	var repeated *SelectorPromptTargetCandidate
	for index := range current.Candidates {
		candidate := &current.Candidates[index]
		if minIntSlice(candidate.Cardinalities) == 20 {
			repeated = candidate
			break
		}
	}
	if repeated == nil || repeated.RowCandidateID == "" {
		t.Fatalf("current catalog omitted the deeply nested 20-book cohort: %+v", current.Candidates)
	}
	if !strings.HasPrefix(repeated.ObservedSelector, "#default > ") ||
		!strings.HasSuffix(repeated.ObservedSelector, " > li") {
		t.Fatalf("deep Books cohort was not stably rooted: %+v", repeated)
	}
	requiredFields := map[string]struct {
		selectorFragment string
		fieldType        string
	}{
		"title":        {selectorFragment: "h3 > a", fieldType: "attr"},
		"price":        {selectorFragment: "price_color", fieldType: "text"},
		"availability": {selectorFragment: "availability", fieldType: "text"},
		"rating":       {selectorFragment: "star-rating", fieldType: "attr"},
	}
	fieldSelectors := map[string]string{}
	for name, required := range requiredFields {
		found := false
		for _, field := range repeated.FieldCandidates {
			if field.ParentFieldCandidateID == "" &&
				strings.Contains(field.ObservedRelativeSelector, required.selectorFragment) &&
				slices.Contains(field.SupportedTypes, required.fieldType) {
				found = true
				fieldSelectors[name] = field.ObservedRelativeSelector
				break
			}
		}
		if !found {
			t.Fatalf(
				"deep Books cohort omitted direct %s field evidence for %q: %+v",
				required.fieldType,
				required.selectorFragment,
				repeated.FieldCandidates,
			)
		}
	}

	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "books", "multiple": true, "onEmpty": "fail",
		"target": map[string]any{"selector": repeated.ObservedSelector, "visible": true},
		"fields": map[string]any{
			"title": map[string]any{
				"type": "attr", "selector": fieldSelectors["title"],
				"attr": "title",
			},
			"price": map[string]any{
				"type": "text", "selector": fieldSelectors["price"],
			},
			"availability": map[string]any{
				"type": "text", "selector": fieldSelectors["availability"],
			},
			"rating": map[string]any{
				"type": "attr", "selector": fieldSelectors["rating"],
				"attr": "class",
			},
			"product_url": map[string]any{
				"type": "attr", "selector": fieldSelectors["title"],
				"attr": "href", "resolve": true,
			},
		},
	})
	preparedPrompt, providerRule, err := catalog.PrepareProviderPrompt(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var prepared SelectorPromptCatalog
	if err := json.Unmarshal([]byte(preparedPrompt), &prepared); err != nil {
		t.Fatal(err)
	}
	var selectedRowID, foreignFieldID string
	for _, candidate := range prepared.Candidates {
		if candidate.ObservedSelector == repeated.ObservedSelector {
			selectedRowID = candidate.RowCandidateID
			continue
		}
		for _, field := range candidate.FieldCandidates {
			if field.ParentFieldCandidateID == "" {
				foreignFieldID = field.FieldCandidateID
				break
			}
		}
		if foreignFieldID != "" {
			break
		}
	}
	if selectedRowID == "" || foreignFieldID == "" {
		t.Fatalf("prepared Books catalog lacks selected/foreign scopes: %s", preparedPrompt)
	}
	providerStep := selectorRuleStepForTest(t, providerRule)
	providerTarget := providerStep["target"].(map[string]any)
	if providerTarget["rowCandidateId"] != selectedRowID || providerTarget["selector"] != nil {
		t.Fatalf("Books row was not rewritten to its opaque scope: %#v", providerTarget)
	}
	crossScope, err := copySelectorCandidateRule(providerRule)
	if err != nil {
		t.Fatal(err)
	}
	crossStep := selectorRuleStepForTest(t, crossScope)
	crossFields := crossStep["fields"].(map[string]any)
	crossFields["product_url"].(map[string]any)["fieldCandidateId"] = foreignFieldID
	crossSteps, _ := json.Marshal([]any{crossStep})
	crossScope.Steps = crossSteps
	if _, err := catalog.ResolveProviderExtractionCandidates(
		crossScope,
		prepared.CatalogHash,
	); !errors.Is(err, ErrInvalidProvisionalRule) ||
		!strings.Contains(err.Error(), "unknown or cross-target") {
		t.Fatalf("foreign Books field scope did not fail closed: %v", err)
	}
	report, err := catalog.ResolveProviderExtractionCandidates(
		providerRule,
		prepared.CatalogHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets != 1 || report.Fields != 5 {
		t.Fatalf("Books five-field provider rule did not resolve exactly: %+v", report)
	}
	resolvedStep := selectorRuleStepForTest(t, providerRule)
	if resolvedStep["target"].(map[string]any)["selector"] != repeated.ObservedSelector {
		t.Fatalf("resolved Books row changed selector: %#v", resolvedStep["target"])
	}
	resolvedFields := resolvedStep["fields"].(map[string]any)
	expectedFields := map[string]map[string]any{
		"title": {
			"type": "attr", "selector": fieldSelectors["title"],
			"attr": "title",
		},
		"price": {
			"type": "text", "selector": fieldSelectors["price"],
		},
		"availability": {
			"type": "text", "selector": fieldSelectors["availability"],
		},
		"rating": {
			"type": "attr", "selector": fieldSelectors["rating"],
			"attr": "class",
		},
		"product_url": {
			"type": "attr", "selector": fieldSelectors["title"],
			"attr": "href", "resolve": true,
		},
	}
	for name, expected := range expectedFields {
		actual := resolvedFields[name].(map[string]any)
		for key, value := range expected {
			if actual[key] != value {
				t.Fatalf("resolved Books %s.%s changed: want=%v got=%v field=%#v",
					name, key, value, actual[key], actual)
			}
		}
	}

	v3Prompt, _, err := catalog.prepareProviderPrompt(nil, selectorPromptCatalogVersionV3)
	if err != nil {
		t.Fatal(err)
	}
	var v3 SelectorPromptCatalog
	if err := json.Unmarshal([]byte(v3Prompt), &v3); err != nil {
		t.Fatal(err)
	}
	if v3.Version != selectorPromptCatalogVersionV3 {
		t.Fatalf("historical prompt version changed: %q", v3.Version)
	}
	for _, candidate := range v3.Candidates {
		if minIntSlice(candidate.Cardinalities) == 20 {
			t.Fatalf("historical v3 depth was reinterpreted: %+v", candidate)
		}
	}
	rebuilt := selectorCatalogForTest(t, snapshot)
	rebuiltV3, _, err := rebuilt.ReconstructProviderPrompt(v3Prompt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltV3 != v3Prompt {
		t.Fatalf("stored v3 catalog changed:\noriginal: %s\nrebuilt:  %s", v3Prompt, rebuiltV3)
	}
}

func TestSelectorCatalogV1ReconstructionIsExactAndResolvable(t *testing.T) {
	snapshot := selectorSnapshotForTest(
		0,
		"https://example.test/search",
		selectorResultsForTest("results", []selectorCardForTest{
			{classes: "card", title: true, summary: true},
			{classes: "card", title: true, summary: true},
		}),
	)
	trusted := selectorRuleForTest(map[string]any{
		"action": "extract", "name": "rows", "multiple": true,
		"target": map[string]any{"selector": "#results > .card"},
		"fields": map[string]any{
			"title": map[string]any{"type": "text", "selector": ".title"},
		},
	})
	legacy := selectorCatalogForTest(t, snapshot)
	legacyPrompt, legacyProvider, err := legacy.prepareProviderPrompt(
		trusted,
		selectorPromptCatalogVersionV1,
	)
	if err != nil {
		t.Fatal(err)
	}
	var legacyCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(legacyPrompt), &legacyCatalog); err != nil {
		t.Fatal(err)
	}
	if legacyCatalog.Version != selectorPromptCatalogVersionV1 {
		t.Fatalf("legacy prompt version changed: %q", legacyCatalog.Version)
	}
	legacyStep := selectorRuleStepForTest(t, legacyProvider)
	legacyRowID, _ := legacyStep["target"].(map[string]any)["rowCandidateId"].(string)
	if !strings.HasPrefix(legacyRowID, "row_") || len(legacyRowID) != 26 {
		t.Fatalf("legacy row alias shape changed: %q", legacyRowID)
	}
	assigner := newOpaqueCandidateIDAssigner(selectorPromptCatalogVersionV1)
	if got := assigner.assign("row", "canonical"); got != "row_IcaETbsCyqP_YcLLXtkgRA" {
		t.Fatalf("legacy v1 digest algorithm changed: %q", got)
	}

	rebuiltCatalog := selectorCatalogForTest(t, snapshot)
	rebuiltPrompt, rebuiltProvider, err := rebuiltCatalog.ReconstructProviderPrompt(
		legacyPrompt,
		trusted,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltPrompt != legacyPrompt {
		t.Fatalf("v1 reconstruction was not byte-for-byte exact:\noriginal: %s\nrebuilt:  %s", legacyPrompt, rebuiltPrompt)
	}
	legacyProviderJSON, _ := json.Marshal(legacyProvider)
	rebuiltProviderJSON, _ := json.Marshal(rebuiltProvider)
	if !bytes.Equal(rebuiltProviderJSON, legacyProviderJSON) {
		t.Fatalf("v1 provider rule reconstruction changed:\noriginal: %s\nrebuilt:  %s", legacyProviderJSON, rebuiltProviderJSON)
	}
	if _, err := rebuiltCatalog.ResolveProviderExtractionCandidates(
		rebuiltProvider,
		legacyCatalog.CatalogHash,
	); err != nil {
		t.Fatalf("reconstructed v1 IDs did not resolve: %v", err)
	}
}

func TestSelectorCatalogReconstructionRejectsInvalidOrUnsupportedVersion(t *testing.T) {
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(
			0,
			"https://example.test",
			selectorElementForTest("html", nil, selectorElementForTest("body", nil)),
		),
	)
	for _, stored := range []string{
		`not-json`,
		`{"version":"selector-catalog-v999","catalogHash":"","candidates":[]}`,
		`{"catalogHash":"","candidates":[]}`,
	} {
		if _, _, err := catalog.ReconstructProviderPrompt(stored, nil); !errors.Is(err, ErrSelectorCatalogUnavailable) {
			t.Fatalf("unsupported stored catalog did not fail closed: stored=%q err=%v", stored, err)
		}
	}
}

func TestOpaqueSelectorCandidatesLeaveTargetlessPageInfoUntouched(t *testing.T) {
	root := selectorElementForTest("html", nil, selectorElementForTest("body", nil,
		selectorElementForTest("h1", map[string]string{"id": "title"}, selectorTextForTest("Title")),
	))
	catalog := selectorCatalogForTest(t, selectorSnapshotForTest(0, "https://example.test", root))
	rule := &models.Rule{
		ID: "page", Version: "1", Name: "Page", Domain: models.JSON(`"example.test"`),
		Entry: "https://example.test", Steps: models.JSON(`[{"action":"extractPageInfo","name":"page"}]`),
	}
	prompt, providerRule, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatal(err)
	}
	var promptCatalog SelectorPromptCatalog
	if err := json.Unmarshal([]byte(prompt), &promptCatalog); err != nil {
		t.Fatal(err)
	}
	report, err := catalog.ResolveProviderExtractionCandidates(providerRule, promptCatalog.CatalogHash)
	if err != nil {
		t.Fatal(err)
	}
	if report.Targets != 0 || string(providerRule.Steps) != `[{"action":"extractPageInfo","name":"page"}]` {
		t.Fatalf("targetless page info was changed: report=%+v steps=%s", report, providerRule.Steps)
	}
}

func TestOpaqueSelectorCandidatesPinTrustedSingletonsFromFinalState(t *testing.T) {
	initial := selectorElementForTest("main", nil,
		selectorElementForTest("input", map[string]string{"id": "search"}),
	)
	final := selectorElementForTest("main", nil,
		selectorElementForTest("span", map[string]string{"class": "product-name"}, selectorTextForTest("Name")),
		selectorElementForTest("span", map[string]string{"class": "product-price"}, selectorTextForTest("$12")),
	)
	catalog := selectorCatalogForTest(t,
		selectorSnapshotForTest(0, "https://fixture.test/products", initial),
		selectorSnapshotForTest(1, "https://fixture.test/products", selectorElementForTest("main", nil)),
		selectorSnapshotForTest(2, "https://fixture.test/products?q=x", final),
	)
	rule := &models.Rule{
		ID: "products", Version: "1", Name: "Products", Domain: models.JSON(`"fixture.test"`),
		Entry: "https://fixture.test/products", Steps: models.JSON(`[
			{"action":"extractText","name":"name","target":{"selector":".product-name"}},
			{"action":"extract","name":"price","target":{"selector":".product-price"},"fields":{"price":{"type":"number"}}}
		]`),
	}
	prompt, provider, err := catalog.PrepareProviderPrompt(rule)
	if err != nil {
		t.Fatalf("final-state trusted singleton was not pinned: %v; prompt=%s", err, prompt)
	}
	encoded := string(provider.Steps)
	if strings.Contains(encoded, `"selector"`) || strings.Count(encoded, "targetCandidateId") != 2 ||
		!strings.Contains(encoded, "fieldCandidateId") {
		t.Fatalf("final-state singletons were not reverse-mapped: %s", encoded)
	}
}

func mutateSelectorCandidateRuleStep(t *testing.T, rule *models.Rule, mutate func(map[string]any)) {
	t.Helper()
	var steps []any
	if err := json.Unmarshal(rule.Steps, &steps); err != nil {
		t.Fatal(err)
	}
	mutate(steps[0].(map[string]any))
	encoded, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	rule.Steps = encoded
}
