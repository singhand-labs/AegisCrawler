package rule

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const targetMetadataNormalizedFlag = "target-metadata:canonicalized"
const visibleExtractionNormalizedFlag = "visible-extraction:enforced"
const repeatedExtractionEmptyNormalizedFlag = "repeated-extraction:on-empty-fail"
const repeatedExtractionReadinessNormalizedFlag = "repeated-extraction:visible-readiness"

var (
	blankTargetStringKeys = []string{"$ref", "selector", "xpath", "text", "ariaLabel", "role", "roleName", "frame"}
	actionTargetKeys      = []string{"target", "source", "hoverTarget", "clickTarget", "to"}
	nestedActionListKeys  = []string{"steps", "then", "else", "default", "onOpen"}
)

// normalizeWorkflowTargetMetadata canonicalizes only known Target positions.
// It deliberately does not walk arbitrary payloads, schemas, or variables,
// where fields named role or ariaLabel may be legitimate collection data.
func normalizeWorkflowTargetMetadata(rule *models.Rule) (bool, error) {
	changed := false

	selectorsChanged, err := normalizeSelectorAliases(&rule.Selectors)
	if err != nil {
		return false, fmt.Errorf("selectors: %w", err)
	}
	changed = changed || selectorsChanged

	stepsChanged, err := normalizeActionListJSON(&rule.Steps, "steps")
	if err != nil {
		return false, err
	}
	changed = changed || stepsChanged

	hooksChanged, err := normalizeHookTargets(&rule.Hooks)
	if err != nil {
		return false, err
	}
	return changed || hooksChanged, nil
}

// normalizeWorkflowVisibleExtractionTargets makes rendered, visible DOM the
// deterministic default for every generated collection extraction. The
// recording-derived baseline is not normalized through this function, so
// existing rules retain their historical target semantics. An inline value is
// used for selector aliases because runtime alias resolution lets the action
// override shared selector metadata without changing non-extraction consumers.
func normalizeWorkflowVisibleExtractionTargets(rule *models.Rule) (bool, error) {
	stepsChanged, err := normalizeVisibleExtractionActionListJSON(&rule.Steps, "steps")
	if err != nil {
		return false, err
	}
	hooksChanged, err := normalizeVisibleExtractionHooks(&rule.Hooks)
	if err != nil {
		return false, err
	}
	return stepsChanged || hooksChanged, nil
}

// normalizeWorkflowRepeatedExtractionOnEmpty makes generated collection loops
// fail closed when their repeated item selector matches nothing. Baselines and
// approved legacy rules do not pass through this provisional-only normalizer,
// so their historical skip/sendEmpty behavior remains unchanged.
func normalizeWorkflowRepeatedExtractionOnEmpty(rule *models.Rule) (bool, error) {
	stepsChanged, err := normalizeRepeatedExtractionActionListJSON(&rule.Steps, "steps")
	if err != nil {
		return false, err
	}
	hooksChanged, err := normalizeRepeatedExtractionHooks(&rule.Hooks)
	if err != nil {
		return false, err
	}
	return stepsChanged || hooksChanged, nil
}

// normalizeWorkflowRepeatedExtractionReadiness gives every unconditional,
// fail-closed repeated extraction an observable readiness checkpoint derived
// from the extraction target that has already passed provider-candidate
// resolution. A fixed delay immediately before the extraction is retained
// only after this checkpoint as an optional settling period: elapsed time
// alone does not prove that the intended visible rows exist.
//
// Baselines and approved historical rules do not pass through this
// provisional-only normalizer, so their established semantics remain intact.
func normalizeWorkflowRepeatedExtractionReadiness(rule *models.Rule) (bool, error) {
	stepsChanged, err := normalizeRepeatedExtractionReadinessActionListJSON(&rule.Steps, "steps")
	if err != nil {
		return false, err
	}
	hooksChanged, err := normalizeRepeatedExtractionReadinessHooks(&rule.Hooks)
	if err != nil {
		return false, err
	}
	return stepsChanged || hooksChanged, nil
}

func normalizeRepeatedExtractionReadinessActionListJSON(raw *models.JSON, field string) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var steps []any
	if err := json.Unmarshal(*raw, &steps); err != nil {
		return false, fmt.Errorf("%s: %w", field, err)
	}
	normalized, changed := normalizeRepeatedExtractionReadinessActionList(steps)
	return rewriteJSON(raw, normalized, changed)
}

func normalizeRepeatedExtractionReadinessHooks(raw *models.JSON) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var hooks map[string]any
	if err := json.Unmarshal(*raw, &hooks); err != nil {
		return false, fmt.Errorf("hooks: %w", err)
	}
	changed := false
	for key, value := range hooks {
		if steps, ok := value.([]any); ok {
			normalized, listChanged := normalizeRepeatedExtractionReadinessActionList(steps)
			if listChanged {
				hooks[key] = normalized
				changed = true
			}
		}
	}
	return rewriteJSON(raw, hooks, changed)
}

func normalizeRepeatedExtractionReadinessActionList(steps []any) ([]any, bool) {
	nestedChanged := false
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range nestedActionListKeys {
			if children, ok := step[key].([]any); ok {
				normalized, changed := normalizeRepeatedExtractionReadinessActionList(children)
				if changed {
					step[key] = normalized
					nestedChanged = true
				}
			}
		}
		if trigger, ok := step["trigger"].(map[string]any); ok {
			normalized, changed := normalizeRepeatedExtractionReadinessActionList([]any{trigger})
			if changed {
				if len(normalized) == 1 {
					step["trigger"] = normalized[0]
				} else {
					step["trigger"] = map[string]any{"action": "group", "steps": normalized}
				}
				nestedChanged = true
			}
		}
		if cases, ok := step["cases"].([]any); ok {
			for _, rawCase := range cases {
				caseValue, ok := rawCase.(map[string]any)
				if !ok {
					continue
				}
				if caseSteps, ok := caseValue["steps"].([]any); ok {
					normalized, changed := normalizeRepeatedExtractionReadinessActionList(caseSteps)
					if changed {
						caseValue["steps"] = normalized
						nestedChanged = true
					}
				}
			}
		}
	}

	normalized := make([]any, 0, len(steps)+1)
	changed := nestedChanged
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok || step["action"] != "extract" || step["multiple"] != true ||
			step["onEmpty"] != "fail" || step["condition"] != nil {
			normalized = append(normalized, raw)
			continue
		}
		target, ok := step["target"].(map[string]any)
		if !ok || len(target) == 0 {
			normalized = append(normalized, raw)
			continue
		}
		insertAt := len(normalized)
		for insertAt > 0 && unconditionalFixedWait(normalized[insertAt-1]) {
			insertAt--
		}
		if insertAt > 0 {
			previous, _ := normalized[insertAt-1].(map[string]any)
			if equivalentVisibleReadiness(previous, target) {
				normalized = append(normalized, raw)
				continue
			}
		}
		wait := map[string]any{
			"action": "waitForElementVisible",
			"target": cloneWorkflowTarget(target),
		}
		normalized = append(normalized, nil)
		copy(normalized[insertAt+1:], normalized[insertAt:])
		normalized[insertAt] = wait
		normalized = append(normalized, raw)
		changed = true
	}
	return normalized, changed
}

func unconditionalFixedWait(raw any) bool {
	step, ok := raw.(map[string]any)
	return ok && step["action"] == "waitForTimeout" && step["condition"] == nil
}

func equivalentVisibleReadiness(step, extractionTarget map[string]any) bool {
	if step == nil || step["action"] != "waitForElementVisible" || step["condition"] != nil {
		return false
	}
	if onError, exists := step["onError"]; exists && onError != nil && onError != "stop" {
		return false
	}
	waitTarget, ok := step["target"].(map[string]any)
	if !ok || waitTarget["visible"] != true {
		return false
	}
	return workflowTargetIdentity(waitTarget) == workflowTargetIdentity(extractionTarget)
}

func workflowTargetIdentity(target map[string]any) string {
	identity := cloneWorkflowTarget(target)
	delete(identity, "visible")
	delete(identity, "timeout")
	delete(identity, "multiple")
	encoded, _ := json.Marshal(identity)
	return string(encoded)
}

func cloneWorkflowTarget(target map[string]any) map[string]any {
	cloned := make(map[string]any, len(target))
	for key, value := range target {
		switch typed := value.(type) {
		case map[string]any:
			cloned[key] = cloneWorkflowTarget(typed)
		case []any:
			items := make([]any, len(typed))
			copy(items, typed)
			cloned[key] = items
		default:
			cloned[key] = value
		}
	}
	return cloned
}

func normalizeRepeatedExtractionActionListJSON(raw *models.JSON, field string) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var steps []any
	if err := json.Unmarshal(*raw, &steps); err != nil {
		return false, fmt.Errorf("%s: %w", field, err)
	}
	changed := normalizeRepeatedExtractionActionList(steps)
	return rewriteJSON(raw, steps, changed)
}

func normalizeRepeatedExtractionHooks(raw *models.JSON) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var hooks map[string]any
	if err := json.Unmarshal(*raw, &hooks); err != nil {
		return false, fmt.Errorf("hooks: %w", err)
	}
	changed := false
	for _, value := range hooks {
		if steps, ok := value.([]any); ok {
			changed = normalizeRepeatedExtractionActionList(steps) || changed
		}
	}
	return rewriteJSON(raw, hooks, changed)
}

func normalizeRepeatedExtractionActionList(steps []any) bool {
	changed := false
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if step["action"] == "extract" && step["multiple"] == true && step["onEmpty"] != "fail" {
			step["onEmpty"] = "fail"
			changed = true
		}
		for _, key := range nestedActionListKeys {
			if children, ok := step[key].([]any); ok {
				changed = normalizeRepeatedExtractionActionList(children) || changed
			}
		}
		if trigger, ok := step["trigger"].(map[string]any); ok {
			changed = normalizeRepeatedExtractionActionList([]any{trigger}) || changed
		}
		if cases, ok := step["cases"].([]any); ok {
			for _, rawCase := range cases {
				if caseValue, ok := rawCase.(map[string]any); ok {
					if caseSteps, ok := caseValue["steps"].([]any); ok {
						changed = normalizeRepeatedExtractionActionList(caseSteps) || changed
					}
				}
			}
		}
	}
	return changed
}

func normalizeVisibleExtractionActionListJSON(raw *models.JSON, field string) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var steps []any
	if err := json.Unmarshal(*raw, &steps); err != nil {
		return false, fmt.Errorf("%s: %w", field, err)
	}
	changed := normalizeVisibleExtractionActionList(steps)
	return rewriteJSON(raw, steps, changed)
}

func normalizeVisibleExtractionHooks(raw *models.JSON) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var hooks map[string]any
	if err := json.Unmarshal(*raw, &hooks); err != nil {
		return false, fmt.Errorf("hooks: %w", err)
	}
	changed := false
	for _, value := range hooks {
		if steps, ok := value.([]any); ok {
			changed = normalizeVisibleExtractionActionList(steps) || changed
		}
	}
	return rewriteJSON(raw, hooks, changed)
}

func normalizeVisibleExtractionActionList(steps []any) bool {
	changed := false
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		action, _ := step["action"].(string)
		if extractionWorkflowActions[action] {
			if target, ok := step["target"].(map[string]any); ok && target["visible"] != true {
				target["visible"] = true
				changed = true
			}
		}
		for _, key := range nestedActionListKeys {
			if children, ok := step[key].([]any); ok {
				changed = normalizeVisibleExtractionActionList(children) || changed
			}
		}
		if trigger, ok := step["trigger"].(map[string]any); ok {
			changed = normalizeVisibleExtractionActionList([]any{trigger}) || changed
		}
		if cases, ok := step["cases"].([]any); ok {
			for _, rawCase := range cases {
				if caseValue, ok := rawCase.(map[string]any); ok {
					if caseSteps, ok := caseValue["steps"].([]any); ok {
						changed = normalizeVisibleExtractionActionList(caseSteps) || changed
					}
				}
			}
		}
	}
	return changed
}

func normalizeSelectorAliases(raw *models.JSON) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var selectors map[string]any
	if err := json.Unmarshal(*raw, &selectors); err != nil {
		return false, err
	}
	changed := false
	for _, value := range selectors {
		if target, ok := value.(map[string]any); ok {
			changed = normalizeTargetMetadata(target) || changed
		}
	}
	return rewriteJSON(raw, selectors, changed)
}

func normalizeActionListJSON(raw *models.JSON, field string) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var steps []any
	if err := json.Unmarshal(*raw, &steps); err != nil {
		return false, fmt.Errorf("%s: %w", field, err)
	}
	changed := normalizeActionList(steps)
	return rewriteJSON(raw, steps, changed)
}

func normalizeHookTargets(raw *models.JSON) (bool, error) {
	if len(*raw) == 0 {
		return false, nil
	}
	var hooks map[string]any
	if err := json.Unmarshal(*raw, &hooks); err != nil {
		return false, fmt.Errorf("hooks: %w", err)
	}
	changed := false
	for _, value := range hooks {
		if steps, ok := value.([]any); ok {
			changed = normalizeActionList(steps) || changed
		}
	}
	return rewriteJSON(raw, hooks, changed)
}

func normalizeActionList(steps []any) bool {
	changed := false
	for _, raw := range steps {
		if step, ok := raw.(map[string]any); ok {
			changed = normalizeActionTargets(step) || changed
		}
	}
	return changed
}

func normalizeActionTargets(step map[string]any) bool {
	changed := false
	for _, key := range actionTargetKeys {
		if target, ok := step[key].(map[string]any); ok {
			changed = normalizeTargetMetadata(target) || changed
		}
	}
	if condition, ok := step["condition"].(map[string]any); ok {
		changed = normalizeConditionTarget(condition) || changed
	}
	if fields, ok := step["fields"].(map[string]any); ok {
		changed = normalizeExtractFields(fields) || changed
	}
	for _, key := range nestedActionListKeys {
		if children, ok := step[key].([]any); ok {
			changed = normalizeActionList(children) || changed
		}
	}
	if trigger, ok := step["trigger"].(map[string]any); ok {
		changed = normalizeActionTargets(trigger) || changed
	}
	if cases, ok := step["cases"].([]any); ok {
		for _, rawCase := range cases {
			if caseValue, ok := rawCase.(map[string]any); ok {
				if caseSteps, ok := caseValue["steps"].([]any); ok {
					changed = normalizeActionList(caseSteps) || changed
				}
			}
		}
	}
	return changed
}

func normalizeConditionTarget(condition map[string]any) bool {
	if target, ok := condition["target"].(map[string]any); ok {
		return normalizeTargetMetadata(target)
	}
	return false
}

func normalizeExtractFields(fields map[string]any) bool {
	changed := false
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if condition, ok := field["condition"].(map[string]any); ok {
			changed = normalizeConditionTarget(condition) || changed
		}
		if nested, ok := field["fields"].(map[string]any); ok {
			changed = normalizeExtractFields(nested) || changed
		}
	}
	return changed
}

func normalizeTargetMetadata(target map[string]any) bool {
	changed := false
	for _, key := range blankTargetStringKeys {
		if value, ok := target[key].(string); ok && strings.TrimSpace(value) == "" {
			delete(target, key)
			changed = true
		}
	}
	if _, hasRole := target["role"]; !hasRole {
		if _, hasRoleName := target["roleName"].(string); hasRoleName {
			delete(target, "roleName")
			changed = true
		}
	}
	if path, ok := target["shadowPath"].([]any); ok {
		filtered := make([]any, 0, len(path))
		for _, part := range path {
			if value, ok := part.(string); ok && strings.TrimSpace(value) == "" {
				changed = true
				continue
			}
			filtered = append(filtered, part)
		}
		if len(filtered) == 0 && len(path) > 0 {
			delete(target, "shadowPath")
		} else if len(filtered) != len(path) {
			target["shadowPath"] = filtered
		}
	}
	if _, exists := target["stableSelector"]; exists {
		delete(target, "stableSelector")
		changed = true
	}
	return changed
}

func rewriteJSON(raw *models.JSON, value any, changed bool) (bool, error) {
	if !changed {
		return false, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	*raw = models.JSON(encoded)
	return true, nil
}
