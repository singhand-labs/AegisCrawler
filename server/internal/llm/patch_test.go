package llm

import "testing"

func TestApplyPatchReplaceSelector(t *testing.T) {
	baseline := map[string]any{
		"selectors": map[string]any{"search": map[string]any{"selector": "#suggest"}},
		"steps": []any{
			map[string]any{"action": "click", "target": map[string]any{"$ref": "search"}},
		},
	}
	suggestion := &EnhancementSuggestion{
		Selectors: map[string]SelectorSuggestion{
			"search": {Selector: "[data-testid='search-input']", Reason: "更稳定"},
		},
		Steps: []PatchOp{
			{Op: "replace", Path: "/steps/0/description", Value: "点击搜索框"},
		},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	selectors := patched["selectors"].(map[string]any)
	search := selectors["search"].(map[string]any)
	if search["selector"] != "[data-testid='search-input']" {
		t.Fatalf("unexpected selector: %v", search["selector"])
	}
	steps := patched["steps"].([]any)
	step0 := steps[0].(map[string]any)
	if step0["description"] != "点击搜索框" {
		t.Fatalf("unexpected description: %v", step0["description"])
	}
}

func TestApplyPatchAddStepAtIndex(t *testing.T) {
	baseline := map[string]any{
		"steps": []any{
			map[string]any{"action": "click"},
			map[string]any{"action": "input"},
		},
	}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "add", Path: "/steps/1", Value: map[string]any{"action": "wait"}},
		},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	steps := patched["steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if steps[1].(map[string]any)["action"] != "wait" {
		t.Fatalf("unexpected inserted step: %v", steps[1])
	}
}

func TestApplyPatchRemoveStep(t *testing.T) {
	baseline := map[string]any{
		"steps": []any{
			map[string]any{"action": "click"},
			map[string]any{"action": "input"},
		},
	}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "remove", Path: "/steps/0"},
		},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	steps := patched["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].(map[string]any)["action"] != "input" {
		t.Fatalf("unexpected remaining step: %v", steps[0])
	}
}

func TestApplyPatchMergeStepTarget(t *testing.T) {
	baseline := map[string]any{
		"steps": []any{
			map[string]any{"action": "click", "target": map[string]any{"selector": "#suggest"}},
		},
	}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "merge", Path: "/steps/0/target", Value: map[string]any{"timeout": float64(5000)}},
		},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	steps := patched["steps"].([]any)
	step0 := steps[0].(map[string]any)
	target := step0["target"].(map[string]any)
	if target["selector"] != "#suggest" {
		t.Fatalf("original selector lost: %v", target)
	}
	if target["timeout"] != float64(5000) {
		t.Fatalf("merged timeout missing: %v", target)
	}
}

func TestApplyPatchReplaceNestedPathInStep(t *testing.T) {
	baseline := map[string]any{
		"steps": []any{
			map[string]any{"action": "click", "target": map[string]any{"selector": "#suggest"}},
		},
	}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "replace", Path: "/steps/0/target/selector", Value: "[data-testid='search']"},
		},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	steps := patched["steps"].([]any)
	step0 := steps[0].(map[string]any)
	target := step0["target"].(map[string]any)
	if target["selector"] != "[data-testid='search']" {
		t.Fatalf("unexpected selector: %v", target["selector"])
	}
}

func TestApplyPatchDoesNotEmptyDocumentFromAliasing(t *testing.T) {
	baseline := map[string]any{
		"selectors": map[string]any{"search": map[string]any{"selector": "#suggest"}},
		"steps": []any{
			map[string]any{"action": "click", "target": map[string]any{"$ref": "search"}},
		},
		"variables": map[string]any{"q": "search query"},
	}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "replace", Path: "/steps/0/description", Value: "点击搜索框"},
		},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if patched["selectors"] == nil {
		t.Fatal("selectors were emptied by aliasing bug")
	}
	if patched["variables"] == nil {
		t.Fatal("variables were emptied by aliasing bug")
	}
	steps := patched["steps"].([]any)
	step0 := steps[0].(map[string]any)
	if step0["description"] != "点击搜索框" {
		t.Fatalf("unexpected description: %v", step0["description"])
	}
}

func TestApplyPatchOutOfRangeIndexReturnsError(t *testing.T) {
	baseline := map[string]any{
		"steps": []any{
			map[string]any{"action": "click"},
		},
	}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{
			{Op: "replace", Path: "/steps/5/description", Value: "out of range"},
		},
	}
	_, err := ApplyPatch(baseline, suggestion)
	if err == nil {
		t.Fatal("expected error for out-of-range index")
	}
}

func TestApplyPatchDeepCopyMarshalError(t *testing.T) {
	baseline := map[string]any{"bad": make(chan int)}
	_, err := ApplyPatch(baseline, &EnhancementSuggestion{})
	if err == nil {
		t.Fatal("expected error when baseline cannot be marshaled")
	}
}

func TestApplyPatchUnsupportedOp(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "copy", Path: "/steps/0", Value: "x"}},
	}
	_, err := ApplyPatch(baseline, suggestion)
	if err == nil {
		t.Fatal("expected error for unsupported op")
	}
}

func TestApplyPatchSelectorsAndVariables(t *testing.T) {
	baseline := map[string]any{
		"selectors": map[string]any{
			"existing": map[string]any{"selector": "#e"},
		},
		"variables": map[string]any{"existing": "v"},
	}
	suggestion := &EnhancementSuggestion{
		Selectors: map[string]SelectorSuggestion{
			"new": {Selector: "#n", Reason: "r"},
		},
		Variables: map[string]string{"new": "nv"},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	selectors := patched["selectors"].(map[string]any)
	if selectors["existing"] == nil {
		t.Fatal("existing selector removed")
	}
	newSel := selectors["new"].(map[string]any)
	if newSel["selector"] != "#n" || newSel["reason"] != "r" {
		t.Fatalf("unexpected new selector: %v", newSel)
	}
	variables := patched["variables"].(map[string]any)
	if variables["existing"] != "v" || variables["new"] != "nv" {
		t.Fatalf("unexpected variables: %v", variables)
	}
}

func TestApplyPatchAddToMap(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "add", Path: "/description", Value: "desc"}},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if patched["description"] != "desc" {
		t.Fatalf("unexpected description: %v", patched["description"])
	}
}

func TestApplyPatchAddNestedToMap(t *testing.T) {
	baseline := map[string]any{"config": map[string]any{"timeout": float64(1000)}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "add", Path: "/config/retry", Value: float64(3)}},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	config := patched["config"].(map[string]any)
	if config["retry"] != float64(3) {
		t.Fatalf("unexpected retry: %v", config["retry"])
	}
}

func TestApplyPatchAddArrayOutOfRange(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "add", Path: "/steps/5", Value: map[string]any{"action": "wait"}}},
	}
	_, err := ApplyPatch(baseline, suggestion)
	if err == nil {
		t.Fatal("expected error for out-of-range add index")
	}
}

func TestApplyPatchAddInvalidIndex(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "add", Path: "/steps/abc", Value: map[string]any{"action": "wait"}}},
	}
	_, err := ApplyPatch(baseline, suggestion)
	if err == nil {
		t.Fatal("expected error for non-numeric array index")
	}
}

func TestApplyPatchRemoveMapKey(t *testing.T) {
	baseline := map[string]any{"a": 1, "b": 2}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "remove", Path: "/a"}},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := patched["a"]; ok {
		t.Fatal("expected key a to be removed")
	}
}

func TestApplyPatchRemoveInvalidIndex(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "remove", Path: "/steps/abc"}},
	}
	_, err := ApplyPatch(baseline, suggestion)
	if err == nil {
		t.Fatal("expected error for non-numeric remove index")
	}
}

func TestApplyPatchMergeScalarOverwrite(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "merge", Path: "/steps/0/action", Value: "wait"}},
	}
	patched, err := ApplyPatch(baseline, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	steps := patched["steps"].([]any)
	if steps[0].(map[string]any)["action"] != "wait" {
		t.Fatalf("unexpected action: %v", steps[0])
	}
}

func TestApplyPatchMergeInvalidIndex(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "merge", Path: "/steps/abc/action", Value: "wait"}},
	}
	_, err := ApplyPatch(baseline, suggestion)
	if err == nil {
		t.Fatal("expected error for non-numeric merge index")
	}
}

func TestApplyPatchSetInvalidIndex(t *testing.T) {
	baseline := map[string]any{"steps": []any{map[string]any{"action": "click"}}}
	suggestion := &EnhancementSuggestion{
		Steps: []PatchOp{{Op: "replace", Path: "/steps/abc/action", Value: "wait"}},
	}
	_, err := ApplyPatch(baseline, suggestion)
	if err == nil {
		t.Fatal("expected error for non-numeric replace index")
	}
}
