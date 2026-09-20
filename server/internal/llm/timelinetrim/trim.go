// Package timelinetrim structurally reduces oversized recording timeline
// items (typically semantic DOM snapshots from heavy React pages) so prompt
// chunks respect the provider input budget. Every removed subtree is replaced
// by an explicit marker so prompts and lineage stay honest about the lossy
// projection.
package timelinetrim

import (
	"sort"
	"encoding/json"
	"fmt"
)

// FloorBytes is the minimum budget an oversized item must fit into after
// trimming. Items that cannot be reduced to this size fail loudly instead of
// silently producing an over-limit prompt.
const FloorBytes = 4_096

// Item is the provider-neutral timeline item shape shared by the requirement
// and DSL generation pipelines.
type Item struct {
	Kind     string
	Index    int
	Position int
	Value    any
}

// Trim reduces a single timeline item so its JSON encoding fits budgetBytes.
// Items that already fit are returned unchanged.
func Trim(item Item, budgetBytes int) (Item, error) {
	encoded, err := json.Marshal(item)
	if err != nil {
		return item, fmt.Errorf("marshal timeline item: %w", err)
	}
	if len(encoded) <= budgetBytes {
		return item, nil
	}
	snapshot, ok := item.Value.(map[string]any)
	if !ok {
		return item, fmt.Errorf(
			"timeline item %s#%d is %d bytes and has no trimmable snapshot payload",
			item.Kind, item.Index, len(encoded))
	}
	if domTree, hasDom := snapshot["domTree"]; hasDom {
		return trimItemWithDomTree(item, snapshot, domTree, budgetBytes)
	}
	// No structural tree (for example legacy html-string snapshots): shrink by
	// truncating the longest strings with explicit markers until it fits.
	value, truncated := truncateLongAttributes(item.Value, 512)
	for attempt := 0; attempt < 8; attempt++ {
		encoded, err := json.Marshal(value)
		if err != nil {
			return item, err
		}
		if len(encoded) <= budgetBytes {
			trimmed := item
			trimmed.Value = value
			return trimmed, nil
		}
		if truncated == 0 {
			break
		}
		value, truncated = truncateLongAttributes(value, 512/(attempt+2)+8)
	}
	return item, fmt.Errorf(
		"timeline item %s#%d is %d bytes and cannot be trimmed to %d bytes",
		item.Kind, item.Index, len(encoded), budgetBytes)
}

func trimItemWithDomTree(item Item, snapshot map[string]any, domTree any, budgetBytes int) (Item, error) {
	encoded, err := json.Marshal(item)
	if err != nil {
		return item, fmt.Errorf("marshal timeline item: %w", err)
	}
	if len(encoded) <= budgetBytes {
		return item, nil
	}
	// The domTree is trimmed against the budget minus the bytes the snapshot
	// wrapper (other fields plus the snapshotTrimmed marker) itself occupies,
	// so the final wrapped item — not just the tree — fits the budget. A
	// wrapper that alone crowds out the tree budget (for example snapshots
	// carrying large iframe frame captures) is itself shrunk with the same
	// structural markers before the tree is trimmed.
	wrapper := make(map[string]any, len(snapshot)+1)
	for key, value := range snapshot {
		wrapper[key] = value
	}
	wrapper["domTree"] = nil
	wrapperBytes, _ := json.Marshal(wrapper)
	wrapperQuota := budgetBytes / 2
	if len(wrapperBytes) > wrapperQuota {
		omitted := 0
		shrunk := rebuildUnderBudget(wrapper, wrapperQuota, &omitted)
		shrunkMap, ok := shrunk.(map[string]any)
		if !ok {
			return item, fmt.Errorf("timeline item %s#%d wrapper shrink lost its object shape", item.Kind, item.Index)
		}
		shrunkMap["__trimmedFields__"] = map[string]any{"originalWrapperBytes": len(wrapperBytes)}
		wrapper = shrunkMap
		wrapperBytes, _ = json.Marshal(wrapper)
		if len(wrapperBytes) > budgetBytes-FloorBytes {
			return item, fmt.Errorf(
				"timeline item %s#%d snapshot metadata is %d bytes and cannot shrink below the %d-byte budget",
				item.Kind, item.Index, len(wrapperBytes), budgetBytes)
		}
	}
	domBudget := budgetBytes - len(wrapperBytes) - 128
	if domBudget < FloorBytes {
		domBudget = FloorBytes
	}
	best, err := trimDomTree(domTree, domBudget)
	if err != nil {
		return item, fmt.Errorf("trim timeline item %s#%d: %w", item.Kind, item.Index, err)
	}
	trimmed := item
	trimmedSnapshot := make(map[string]any, len(wrapper)+1)
	for key, value := range wrapper {
		if key == "domTree" {
			continue
		}
		trimmedSnapshot[key] = value
	}
	trimmedSnapshot["domTree"] = best.value
	trimmedSnapshot["snapshotTrimmed"] = map[string]any{
		"originalBytes":  len(encoded),
		"trimmedBytes":   best.size,
		"omittedNodes":   best.omitted,
		"truncatedAttrs": best.truncatedAttrs,
	}
	trimmed.Value = trimmedSnapshot
	// Final guarantee: the whole wrapped item must fit. Re-trim with the
	// measured overshoot removed if the wrapper estimate was off.
	if wrapped, err := json.Marshal(trimmed); err != nil {
		return item, err
	} else if len(wrapped) > budgetBytes {
		over := len(wrapped) - budgetBytes
		best2, err2 := trimDomTree(best.value, domBudget-over)
		if err2 != nil {
			return item, fmt.Errorf("trim timeline item %s#%d: %w", item.Kind, item.Index, err2)
		}
		trimmedSnapshot["domTree"] = best2.value
		if wrapped2, err := json.Marshal(trimmed); err != nil {
			return item, err
		} else if len(wrapped2) > budgetBytes {
			return item, fmt.Errorf(
				"timeline item %s#%d trimmed to %d bytes but budget is %d",
				item.Kind, item.Index, len(wrapped2), budgetBytes)
		}
	}
	return trimmed, nil
}

type trimmedTree struct {
	value          any
	size           int
	omitted        int
	truncatedAttrs int
}

// trimDomTree rebuilds the tree under a byte budget in a bounded number of
// passes: each pass keeps children greedily until the budget for that
// container is spent and replaces everything else with one marker, so a pass
// removes any amount of overshoot at once instead of one node at a time.
func trimDomTree(root any, budgetBytes int) (trimmedTree, error) {
	omitted := 0
	truncatedAttrs := 0
	tree := deepCopyValue(root)
	// Bounded passes: each pass either fits or strictly shrinks the tree
	// (every pass that overshoots replaces at least one subtree with a small
	// marker), so at most a handful of passes are needed; the guard makes the
	// bound explicit.
	for pass := 0; pass < 8; pass++ {
		encoded, err := json.Marshal(tree)
		if err != nil {
			return trimmedTree{}, err
		}
		if len(encoded) <= budgetBytes {
			return trimmedTree{value: tree, size: len(encoded), omitted: omitted, truncatedAttrs: truncatedAttrs}, nil
		}
		// keepBytes shrinks each pass so the greedy container budgets tighten.
		keep := budgetBytes / (pass + 1)
		if keep < 256 {
			keep = 256
		}
		tree = rebuildUnderBudget(tree, keep, &omitted)
		// Attribute truncation on the later passes finishes the job.
		if pass >= 2 {
			var count int
			tree, count = truncateLongAttributes(tree, 240)
			truncatedAttrs += count
		}
	}
	encoded, err := json.Marshal(tree)
	if err != nil {
		return trimmedTree{}, err
	}
	if len(encoded) > budgetBytes {
		return trimmedTree{}, fmt.Errorf(
			"dom tree cannot be trimmed below %d bytes (budget %d)",
			len(encoded), budgetBytes)
	}
	return trimmedTree{value: tree, size: len(encoded), omitted: omitted, truncatedAttrs: truncatedAttrs}, nil
}

// rebuildUnderBudget walks the tree once; every container (object or array)
// keeps only the leading children whose accumulated approximate size stays
// within keepBytes, and the remainder collapses into one marker node.
func rebuildUnderBudget(value any, keepBytes int, omitted *int) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		used := 0
		for _, key := range keys {
			child := typed[key]
			size := valueApproxSize(child)
			if used > 0 && used+size > keepBytes {
				// Skip the rest of this container's large children, but keep
				// small scalars (attributes) so structural context survives.
				rest := 0
				for _, restKey := range keys {
					rest += valueApproxSize(typed[restKey])
				}
				*omitted++
				out["__trimmed__"] = map[string]any{"omittedBytes": rest - used}
				break
			}
			out[key] = rebuildUnderBudget(child, keepBytes, omitted)
			used += size
		}
		return out
	case []any:
		kept := make([]any, 0, len(typed))
		used := 0
		for index, child := range typed {
			size := valueApproxSize(child)
			if used > 0 && used+size > keepBytes {
				restBytes := 0
				for _, restChild := range typed[index:] {
					restBytes += valueApproxSize(restChild)
				}
				*omitted += len(typed) - index
				kept = append(kept, map[string]any{
					"__trimmedSiblings__": len(typed) - index,
					"omittedBytes":        restBytes,
				})
				return kept
			}
			kept = append(kept, rebuildUnderBudget(child, keepBytes, omitted))
			used += size
		}
		return kept
	default:
		return value
	}
}

func truncateLongAttributes(value any, maxLen int) (any, int) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		truncated := 0
		for key, child := range typed {
			next, count := truncateLongAttributes(child, maxLen)
			out[key] = next
			truncated += count
		}
		return out, truncated
	case []any:
		out := make([]any, len(typed))
		truncated := 0
		for index, child := range typed {
			next, count := truncateLongAttributes(child, maxLen)
			out[index] = next
			truncated += count
		}
		return out, truncated
	case string:
		if len(typed) > maxLen {
			return typed[:maxLen] + "…[trimmed]", 1
		}
		return typed, 0
	default:
		return value, 0
	}
}

func deepCopyValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return value
	}
	return out
}

func valueApproxSize(value any) int {
	switch typed := value.(type) {
	case string:
		return len(typed) + 2
	case map[string]any:
		return mapApproxSize(typed)
	case []any:
		total := 2
		for _, item := range typed {
			total += valueApproxSize(item) + 1
		}
		return total
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return 8
		}
		return len(encoded)
	}
}

func mapApproxSize(value map[string]any) int {
	total := 2
	for key, child := range value {
		total += len(key) + 4 + valueApproxSize(child)
	}
	return total
}
