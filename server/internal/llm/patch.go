package llm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ApplyPatch merges selectors/variables and applies step patch ops to the baseline rule.
func ApplyPatch(baseline map[string]any, suggestion *EnhancementSuggestion) (map[string]any, error) {
	patched, err := deepCopyMap(baseline)
	if err != nil {
		return nil, fmt.Errorf("deep copy baseline: %w", err)
	}

	if len(suggestion.Selectors) > 0 {
		selectors, _ := patched["selectors"].(map[string]any)
		if selectors == nil {
			selectors = map[string]any{}
		}
		for alias, s := range suggestion.Selectors {
			selectors[alias] = map[string]any{"selector": s.Selector, "reason": s.Reason}
		}
		patched["selectors"] = selectors
	}

	if len(suggestion.Variables) > 0 {
		vars, _ := patched["variables"].(map[string]any)
		if vars == nil {
			vars = map[string]any{}
		}
		for k, v := range suggestion.Variables {
			vars[k] = v
		}
		patched["variables"] = vars
	}

	for i, op := range suggestion.Steps {
		if err := applyPatchOp(patched, op); err != nil {
			return nil, fmt.Errorf("step patch %d (%s %s): %w", i, op.Op, op.Path, err)
		}
	}
	return patched, nil
}

func deepCopyMap(m map[string]any) (map[string]any, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal baseline: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal baseline: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func applyPatchOp(doc map[string]any, op PatchOp) error {
	parts := strings.Split(strings.Trim(op.Path, "/"), "/")
	if len(parts) == 0 {
		return fmt.Errorf("empty path")
	}
	var newRoot any
	var err error
	switch op.Op {
	case "replace":
		newRoot, err = setAtPath(doc, parts, op.Value)
	case "add":
		newRoot, err = addAtPath(doc, parts, op.Value)
	case "remove":
		newRoot, err = removeAtPath(doc, parts)
	case "merge":
		newRoot, err = mergeAtPath(doc, parts, op.Value)
	default:
		return fmt.Errorf("unsupported op %q", op.Op)
	}
	if err != nil {
		return err
	}
	newMap, ok := newRoot.(map[string]any)
	if !ok {
		return fmt.Errorf("root must remain object")
	}
	// Copy newMap entries into doc, then remove doc entries that no longer exist.
	// This order avoids clearing newMap when it aliases doc (the common in-place case).
	for k, v := range newMap {
		doc[k] = v
	}
	for k := range doc {
		if _, exists := newMap[k]; !exists {
			delete(doc, k)
		}
	}
	return nil
}

func setAtPath(cur any, parts []string, value any) (any, error) {
	if len(parts) == 0 {
		return value, nil
	}
	p := parts[0]
	rest := parts[1:]
	switch v := cur.(type) {
	case map[string]any:
		next, _ := v[p]
		newNext, err := setAtPath(next, rest, value)
		if err != nil {
			return nil, err
		}
		v[p] = newNext
		return v, nil
	case []any:
		idx, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		if idx < 0 || idx >= len(v) {
			return nil, fmt.Errorf("index %d out of range", idx)
		}
		newNext, err := setAtPath(v[idx], rest, value)
		if err != nil {
			return nil, err
		}
		v[idx] = newNext
		return v, nil
	default:
		return nil, fmt.Errorf("cannot traverse %T at %q", cur, p)
	}
}

func addAtPath(cur any, parts []string, value any) (any, error) {
	if len(parts) == 0 {
		return value, nil
	}
	p := parts[0]
	rest := parts[1:]
	switch v := cur.(type) {
	case map[string]any:
		if len(rest) == 0 {
			v[p] = value
			return v, nil
		}
		next, _ := v[p]
		newNext, err := addAtPath(next, rest, value)
		if err != nil {
			return nil, err
		}
		v[p] = newNext
		return v, nil
	case []any:
		idx, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		if idx < 0 || idx > len(v) {
			return nil, fmt.Errorf("index %d out of range", idx)
		}
		if len(rest) == 0 {
			return append(v[:idx], append([]any{value}, v[idx:]...)...), nil
		}
		newNext, err := addAtPath(v[idx], rest, value)
		if err != nil {
			return nil, err
		}
		v[idx] = newNext
		return v, nil
	default:
		return nil, fmt.Errorf("cannot traverse %T at %q", cur, p)
	}
}

func removeAtPath(cur any, parts []string) (any, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("cannot remove root")
	}
	p := parts[0]
	rest := parts[1:]
	switch v := cur.(type) {
	case map[string]any:
		if len(rest) == 0 {
			delete(v, p)
			return v, nil
		}
		next, _ := v[p]
		newNext, err := removeAtPath(next, rest)
		if err != nil {
			return nil, err
		}
		v[p] = newNext
		return v, nil
	case []any:
		idx, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		if idx < 0 || idx >= len(v) {
			return nil, fmt.Errorf("index %d out of range", idx)
		}
		if len(rest) == 0 {
			return append(v[:idx], v[idx+1:]...), nil
		}
		newNext, err := removeAtPath(v[idx], rest)
		if err != nil {
			return nil, err
		}
		v[idx] = newNext
		return v, nil
	default:
		return nil, fmt.Errorf("cannot traverse %T at %q", cur, p)
	}
}

func mergeAtPath(cur any, parts []string, value any) (any, error) {
	if len(parts) == 0 {
		return mergeValues(cur, value)
	}
	p := parts[0]
	rest := parts[1:]
	switch v := cur.(type) {
	case map[string]any:
		next, _ := v[p]
		newNext, err := mergeAtPath(next, rest, value)
		if err != nil {
			return nil, err
		}
		v[p] = newNext
		return v, nil
	case []any:
		idx, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		if idx < 0 || idx >= len(v) {
			return nil, fmt.Errorf("index %d out of range", idx)
		}
		newNext, err := mergeAtPath(v[idx], rest, value)
		if err != nil {
			return nil, err
		}
		v[idx] = newNext
		return v, nil
	default:
		return nil, fmt.Errorf("cannot traverse %T at %q", cur, p)
	}
}

func mergeValues(a, b any) (any, error) {
	am, okA := a.(map[string]any)
	bm, okB := b.(map[string]any)
	if !okA || !okB {
		return b, nil
	}
	for k, v := range bm {
		am[k] = v
	}
	return am, nil
}
