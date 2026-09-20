package llm

import (
	"strings"
	"testing"
)

func TestScanSafetyDetectsRiskyTarget(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{"action": "click", "target": map[string]any{"selector": "button#purchase"}},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if flags[0].Reason == "" {
		t.Fatal("expected reason")
	}
	if flags[0].StepIndex != "0" {
		t.Fatalf("expected step index 0, got %s", flags[0].StepIndex)
	}
}

func TestScanSafetyDetectsRiskyAction(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{"action": "submit"},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if flags[0].Reason != "高风险动作类型" {
		t.Fatalf("unexpected reason: %s", flags[0].Reason)
	}
	if flags[0].StepIndex != "0" {
		t.Fatalf("expected step index 0, got %s", flags[0].StepIndex)
	}
}

func TestScanSafetyIgnoresSafeRule(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{"action": "click", "target": map[string]any{"selector": "button#search"}},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) != 0 {
		t.Fatalf("expected 0 flags, got %d", len(flags))
	}
}

func TestScanSafetySkipsNonObjectSteps(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			"not a step",
			map[string]any{"action": "submit"},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if flags[0].StepIndex != "1" {
		t.Fatalf("expected step index 1, got %s", flags[0].StepIndex)
	}
}

func TestScanSafetyRecursesIntoNestedSteps(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "if",
				"then": []any{
					map[string]any{"action": "click", "target": map[string]any{"selector": "button#buy"}},
				},
				"else": []any{
					map[string]any{"action": "submit"},
				},
				"steps": []any{
					map[string]any{"action": "click", "target": map[string]any{"ariaLabel": "checkout"}},
				},
			},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) != 3 {
		t.Fatalf("expected 3 flags, got %d", len(flags))
	}

	want := map[string]bool{
		"0.then.0":  true,
		"0.else.0":  true,
		"0.steps.0": true,
	}
	got := map[string]bool{}
	for _, f := range flags {
		got[f.StepIndex] = true
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("missing flag for step %s", k)
		}
	}
}

func TestScanSafetyDeeplyNestedPath(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "if",
				"then": []any{
					map[string]any{
						"action": "if",
						"else": []any{
							map[string]any{"action": "submit"},
						},
					},
				},
			},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if flags[0].StepIndex != "0.then.0.else.0" {
		t.Fatalf("expected path 0.then.0.else.0, got %s", flags[0].StepIndex)
	}
}

func TestContentFilterDetectsScriptTag(t *testing.T) {
	s := &EnhancementSuggestion{
		Selectors: map[string]SelectorSuggestion{
			"title": {Selector: "<script>alert(1)</script>"},
		},
	}
	flags := ContentFilter(s)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if !strings.Contains(flags[0].Reason, "script") {
		t.Fatalf("expected script flag, got %s", flags[0].Reason)
	}
	if reject, _ := UnsafeContentError(flags); !reject {
		t.Fatal("expected script tag to be a hard rejection")
	}
}

func TestContentFilterDetectsEventHandler(t *testing.T) {
	s := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "replace", Path: "/steps/0/target/selector", Value: "button[onclick='steal()']"},
		},
	}
	flags := ContentFilter(s)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if !strings.Contains(flags[0].Reason, "事件处理器") {
		t.Fatalf("expected event handler flag, got %s", flags[0].Reason)
	}
}

func TestContentFilterDetectsExternalURL(t *testing.T) {
	s := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "replace", Path: "/steps/0/url", Value: "https://evil.example.com"},
		},
	}
	flags := ContentFilter(s)
	if len(flags) != 1 {
		t.Fatalf("expected 1 flag, got %d", len(flags))
	}
	if !strings.Contains(flags[0].Reason, "外部 URL") {
		t.Fatalf("expected external URL flag, got %s", flags[0].Reason)
	}
	if reject, _ := UnsafeContentError(flags); reject {
		t.Fatal("external URL should be flagged but not a hard rejection")
	}
}

func TestContentFilterScansVariablesAndSuggestions(t *testing.T) {
	s := &EnhancementSuggestion{
		Selectors: map[string]SelectorSuggestion{
			"title": {Selector: "h1"},
		},
		Variables: map[string]string{
			"token": "<script>alert(1)</script>",
		},
		Suggestions: []string{
			"safe suggestion",
			"<body onerror='steal()'>",
		},
	}
	flags := ContentFilter(s)
	if len(flags) != 2 {
		t.Fatalf("expected 2 flags, got %d: %+v", len(flags), flags)
	}

	got := map[string]bool{}
	for _, f := range flags {
		got[f.StepIndex] = true
	}
	if !got["variable:token"] {
		t.Fatalf("expected variable flag, got %+v", flags)
	}
	if !got["suggestion:1"] {
		t.Fatalf("expected suggestion flag, got %+v", flags)
	}
	if reject, _ := UnsafeContentError(flags); !reject {
		t.Fatal("expected script/event handler to be a hard rejection")
	}
}

func TestContentFilterIgnoresSafeSuggestion(t *testing.T) {
	s := &EnhancementSuggestion{
		Selectors: map[string]SelectorSuggestion{
			"title": {Selector: "h1"},
		},
		Steps: []PatchOp{
			{Op: "replace", Path: "/name", Value: "增强后"},
		},
	}
	flags := ContentFilter(s)
	if len(flags) != 0 {
		t.Fatalf("expected 0 flags, got %d", len(flags))
	}
}

func TestScanSealingFlagsDetectsScriptTag(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "extract",
				"target": map[string]any{"selector": "div<span><script>alert(1)</script>"},
			},
		},
	}
	flags := ScanSealingFlags(rule)
	if !containsKey(flags, "script-tag-in-selector") {
		t.Fatalf("expected script-tag-in-selector in %v", flags)
	}
}

func TestScanSealingFlagsDetectsExternalURL(t *testing.T) {
	rule := map[string]any{
		"entry": "https://example.com/start",
		"steps": []any{
			map[string]any{
				"action": "navigate",
				"url":    "https://evil.example.com/payload",
			},
		},
	}
	flags := ScanSealingFlags(rule)
	if !containsKey(flags, "external-resource-load") {
		t.Fatalf("expected external-resource-load in %v", flags)
	}
}

func TestScanSealingFlagsAllowsOnlyExactEntryOriginNavigation(t *testing.T) {
	for name, candidate := range map[string]string{
		"different scheme": "http://example.com/section-2",
		"different host":   "https://evil.example/section-2",
		"subdomain":        "https://www.example.com/section-2",
		"different port":   "https://example.com:8443/section-2",
		"credentials":      "https://user@example.com/section-2",
	} {
		t.Run(name, func(t *testing.T) {
			flags := ScanSealingFlags(map[string]any{
				"entry": "https://example.com/start",
				"steps": []any{map[string]any{"action": "navigate", "url": candidate}},
			})
			if !containsKey(flags, "external-resource-load") {
				t.Fatalf("unsafe navigation %q omitted external-resource-load: %v", candidate, flags)
			}
		})
	}

	flags := ScanSealingFlags(map[string]any{
		"entry": "https://example.com/start",
		"steps": []any{map[string]any{
			"action": "navigate",
			"url":    "https://EXAMPLE.com/section-{{section_number}}#section-{{section_number}}",
		}},
	})
	if containsKey(flags, "external-resource-load") {
		t.Fatalf("same-origin reviewed navigation was marked external: %v", flags)
	}
}

func TestScanSealingFlagsDetectsIframe(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "extract",
				"target": map[string]any{"text": "<iframe src='https://evil.example.com'>"},
			},
		},
	}
	flags := ScanSealingFlags(rule)
	if !containsKey(flags, "iframe-embed") {
		t.Fatalf("expected iframe-embed in %v", flags)
	}
}

func TestScanSealingFlagsDetectsEventHandler(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "extract",
				"target": map[string]any{"ariaLabel": "onclick=alert(1)"},
			},
		},
	}
	flags := ScanSealingFlags(rule)
	if !containsKey(flags, "event-handler-attribute") {
		t.Fatalf("expected event-handler-attribute in %v", flags)
	}
}

func TestScanSealingFlagsCleanRuleHasNoFlags(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "click",
				"target": map[string]any{"selector": "button.submit"},
			},
			map[string]any{
				"action": "extract",
				"target": map[string]any{"selector": "div.result", "text": "Product Name"},
			},
		},
	}
	if flags := ScanSealingFlags(rule); len(flags) != 0 {
		t.Fatalf("expected no sealing flags for clean rule, got %v", flags)
	}
}

func TestScanSealingFlagsWalksNestedSteps(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "if",
				"then": []any{
					map[string]any{
						"action": "extract",
						"target": map[string]any{"selector": "<script>x</script>"},
					},
				},
			},
		},
	}
	flags := ScanSealingFlags(rule)
	if !containsKey(flags, "script-tag-in-selector") {
		t.Fatalf("expected nested-step scan to find script-tag-in-selector, got %v", flags)
	}
}

func TestScanSealingFlagsDeduplicatesKeys(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{"action": "extract", "target": map[string]any{"selector": "<script>a</script>"}},
			map[string]any{"action": "extract", "target": map[string]any{"text": "<script>b</script>"}},
			map[string]any{"action": "extract", "target": map[string]any{"ariaLabel": "<script>c</script>"}},
		},
	}
	flags := ScanSealingFlags(rule)
	count := 0
	for _, f := range flags {
		if f == "script-tag-in-selector" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected script-tag-in-selector deduplicated to 1, got %d in %v", count, flags)
	}
}

func TestScanSafetyDetectsHTMLEntityEncodedRiskyKeyword(t *testing.T) {
	// Use a non-risky action so only the target keyword triggers ScanSafety.
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "click",
				"target": map[string]any{"selector": "button.&#112;urchase"},
			},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) == 0 {
		t.Fatalf("expected ScanSafety to detect HTML-entity-encoded risky keyword")
	}
	found := false
	for _, f := range flags {
		if strings.Contains(f.Reason, "高风险关键词") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a risky-keyword flag, got %v", flags)
	}
}

func TestScanSafetyDetectsURLEncodedRiskyKeyword(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "click",
				"target": map[string]any{"selector": "button.purc%68ase"},
			},
		},
	}
	flags := ScanSafety(rule)
	if len(flags) == 0 {
		t.Fatalf("expected ScanSafety to detect URL-encoded risky keyword")
	}
	found := false
	for _, f := range flags {
		if strings.Contains(f.Reason, "高风险关键词") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a risky-keyword flag, got %v", flags)
	}
}

func TestScanSafetyDoesNotRegressOnPlainTextRiskyKeyword(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "click",
				"target": map[string]any{"selector": "button.purchase"},
			},
		},
	}
	if flags := ScanSafety(rule); len(flags) == 0 {
		t.Fatalf("expected ScanSafety to still detect plain-text risky keyword")
	}
}

func TestScanSafetyDoesNotFlagCleanRule(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "click",
				"target": map[string]any{"selector": "button.submit"},
			},
		},
	}
	if flags := ScanSafety(rule); len(flags) != 0 {
		t.Fatalf("expected no flags for clean rule, got %v", flags)
	}
}

func containsKey(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func TestContentFilterRuleDetectsScriptTag(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "extract",
				"target": map[string]any{"selector": "<script>alert(1)</script>"},
			},
		},
	}
	flags := ContentFilterRule(rule)
	if len(flags) != 1 {
		t.Fatalf("expected 1 content-filter flag, got %d: %+v", len(flags), flags)
	}
	if flags[0].Action != "content-filter:script-tag" {
		t.Fatalf("unexpected action: %s", flags[0].Action)
	}
	if flags[0].StepIndex != "0" || flags[0].Reason != "selector" {
		t.Fatalf("unexpected flag metadata: %+v", flags[0])
	}
}

func TestContentFilterRuleDetectsMultiplePatterns(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "navigate",
				"url":    "https://evil.example.com/payload",
			},
			map[string]any{
				"action": "extract",
				"target": map[string]any{"text": "<iframe src='x'></iframe>"},
			},
		},
	}
	flags := ContentFilterRule(rule)
	if len(flags) != 2 {
		t.Fatalf("expected 2 content-filter flags, got %d: %+v", len(flags), flags)
	}
	actions := map[string]bool{}
	for _, f := range flags {
		actions[f.Action] = true
	}
	if !actions["content-filter:external-url"] || !actions["content-filter:iframe"] {
		t.Fatalf("expected external-url and iframe flags, got %v", actions)
	}
}

func TestContentFilterRuleRecursesNestedSteps(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "if",
				"then": []any{
					map[string]any{
						"action": "extract",
						"target": map[string]any{"ariaLabel": "onclick=steal()"},
					},
				},
			},
		},
	}
	flags := ContentFilterRule(rule)
	if len(flags) != 1 {
		t.Fatalf("expected 1 content-filter flag, got %d: %+v", len(flags), flags)
	}
	if flags[0].Action != "content-filter:event-handler" {
		t.Fatalf("unexpected action: %s", flags[0].Action)
	}
	if flags[0].StepIndex != "0.then.0" {
		t.Fatalf("unexpected step index: %s", flags[0].StepIndex)
	}
}

func TestContentFilterRuleCleanRuleHasNoFlags(t *testing.T) {
	rule := map[string]any{
		"steps": []any{
			map[string]any{
				"action": "click",
				"target": map[string]any{"selector": "button.submit"},
			},
		},
	}
	if flags := ContentFilterRule(rule); len(flags) != 0 {
		t.Fatalf("expected no content-filter flags for clean rule, got %v", flags)
	}
}
