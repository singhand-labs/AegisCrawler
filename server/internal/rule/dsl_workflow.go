package rule

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

var (
	ErrInvalidProvisionalRule = errors.New("invalid provisional rule")
	ErrUnsafeProvisionalRule  = errors.New("unsafe provisional rule")
	ErrInvalidReplayOutput    = errors.New("replay output does not conform to requirement schema")
)

var allowedWorkflowActions = map[string]bool{
	"abort": true, "blockRequest": true, "blur": true, "break": true,
	"captureRequest": true, "check": true, "checkQuota": true, "checkpoint": true,
	"circuitBreaker": true, "cleanup": true, "clear": true, "click": true,
	"closeTab": true, "deduplicate": true, "doubleClick": true, "dragAndDrop": true,
	"dragBy": true, "emitEvent": true, "exit": true, "extract": true,
	"extractAttribute": true, "extractHtml": true, "extractJson": true,
	"extractPageInfo": true, "extractTable": true, "extractText": true,
	"filter": true, "flushResults": true, "focus": true, "goBack": true,
	"goForward": true, "group": true, "handleDialog": true, "handleDownload": true,
	"heartbeat": true, "hover": true, "hoverClick": true, "if": true,
	"keyCombination": true, "logMetric": true, "loop": true, "merge": true,
	"middleClick": true, "moveMouse": true, "navigate": true, "openTab": true,
	"pageDown": true, "parallel": true, "paste": true, "pressAndHold": true,
	"pressKey": true, "readPause": true, "recover": true, "reload": true,
	"removeElement": true, "requestHuman": true, "retry": true, "rightClick": true,
	"saveSnapshot": true, "screenshot": true, "scrollBy": true,
	"scrollIntoView": true, "scrollTo": true, "scrollToBottom": true,
	"scrollToTop": true, "select": true, "selectRadio": true, "sendHtml": true,
	"sendLog": true, "sendResult": true, "sendScreenshot": true,
	"setAttribute": true, "setLanguage": true, "setStyle": true, "setTag": true,
	"setUserAgent": true, "setViewport": true, "sleep": true, "slide": true,
	"switch": true, "switchTab": true, "tabToNext": true, "transform": true,
	"type": true, "typeAndSelect": true, "updateStatus": true,
	"validateData": true, "waitFor": true, "waitForElementHidden": true,
	"waitForElementVisible": true, "waitForNetworkIdle": true, "waitForText": true, "waitForTimeout": true,
	"waitForUrl": true,
}

var forbiddenWorkflowActions = map[string]string{
	"evaluate":        "arbitrary JavaScript is disabled",
	"waitForFunction": "arbitrary JavaScript is disabled",
	"solveCaptcha":    "CAPTCHA bypass is prohibited",
	"setCookie":       "browser credentials must remain in the named profile",
	"setLocalStorage": "browser credentials must remain in the named profile",
	"setExtraHeaders": "authorization material must not be embedded in a rule",
	"refreshSession":  "session credentials must remain in the named profile",
	"uploadFile":      "file upload requires explicit policy approval",
}

// replayUnsupportedWorkflowActions are schema-recognized actions that the
// browser replay workflow cannot execute safely or reliably yet. Keep these
// distinct from unknown actions so provider retries receive precise corrective
// feedback instead of producing a provisional rule that can only fail later.
var replayUnsupportedWorkflowActions = map[string]string{
	"goBack":             "browser replay cannot validate the history destination before navigation; use an observed same-origin navigate or interaction instead",
	"goForward":          "browser replay cannot validate the history destination before navigation; use an observed same-origin navigate or interaction instead",
	"setTag":             "setTag is legacy log-only metadata; v2 browser replay cannot validate tag scope or checkpoint/navigation resume semantics, so remove setTag from production workflows",
	"waitForNetworkIdle": "browser resource observation cannot prove that already-started requests have finished; use waitForElementVisible, waitForElementHidden, waitForText, or waitForTimeout",
}

var extractionWorkflowActions = map[string]bool{
	"extract": true, "extractAttribute": true, "extractHtml": true,
	"extractJson": true, "extractPageInfo": true, "extractTable": true,
	"extractText": true,
}

// ValidateWorkflowBaseline applies the server-side DSL and replay safety gate
// before a recording-derived baseline is sent to an LLM. Baselines describe
// the demonstrated interaction and therefore are not required to submit a
// collection result yet.
func ValidateWorkflowBaseline(candidate, source *models.Rule, requirement models.CollectionRequirementSpec) ([]string, error) {
	return validateWorkflowRule(candidate, source, requirement, false)
}

// ValidateProvisionalRule applies the server-side DSL, collection-output, and
// replay safety gates. It preserves the recording-derived entry/domain
// identity. The confirmed per-row output schema is stored separately on the
// immutable rule-version contract after approval; Rule.Output has different
// engine semantics and validates the rule's complete ctx.extracted namespace.
func ValidateProvisionalRule(generated, baseline *models.Rule, requirement models.CollectionRequirementSpec) ([]string, error) {
	return validateWorkflowRule(generated, baseline, requirement, true)
}

func validateWorkflowRule(generated, baseline *models.Rule, requirement models.CollectionRequirementSpec, requireResult bool) ([]string, error) {
	if generated == nil || baseline == nil {
		return nil, fmt.Errorf("%w: generated and baseline rules are required", ErrInvalidProvisionalRule)
	}
	targetMetadataNormalized, err := normalizeWorkflowTargetMetadata(generated)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid target metadata: %v", ErrInvalidProvisionalRule, err)
	}
	visibleExtractionNormalized := false
	repeatedExtractionEmptyNormalized := false
	repeatedExtractionReadinessNormalized := false
	requirementInputReferencesNormalized := false
	extractionReferencesNormalized := false
	if requireResult {
		visibleExtractionNormalized, err = normalizeWorkflowVisibleExtractionTargets(generated)
		if err != nil {
			return nil, fmt.Errorf("%w: normalize visible extraction targets: %v", ErrInvalidProvisionalRule, err)
		}
		repeatedExtractionEmptyNormalized, err = normalizeWorkflowRepeatedExtractionOnEmpty(generated)
		if err != nil {
			return nil, fmt.Errorf("%w: normalize repeated extraction empty behavior: %v", ErrInvalidProvisionalRule, err)
		}
		repeatedExtractionReadinessNormalized, err = normalizeWorkflowRepeatedExtractionReadiness(generated)
		if err != nil {
			return nil, fmt.Errorf("%w: normalize repeated extraction readiness: %v", ErrInvalidProvisionalRule, err)
		}
		requirementInputReferencesNormalized, err = normalizeWorkflowRequirementInputReferences(generated, requirement)
		if err != nil {
			return nil, fmt.Errorf("%w: normalize requirement input references: %v", ErrInvalidProvisionalRule, err)
		}
		extractionReferencesNormalized, err = normalizeWorkflowExtractionReferences(generated)
		if err != nil {
			return nil, fmt.Errorf("%w: normalize extraction references: %v", ErrInvalidProvisionalRule, err)
		}
	}
	missing := make([]string, 0, 5)
	if strings.TrimSpace(generated.ID) == "" {
		missing = append(missing, "id")
	}
	if strings.TrimSpace(generated.Version) == "" {
		missing = append(missing, "version")
	}
	if strings.TrimSpace(generated.Name) == "" {
		missing = append(missing, "name")
	}
	if len(generated.Domain) == 0 {
		missing = append(missing, "domain")
	}
	if len(generated.Steps) == 0 {
		missing = append(missing, "steps")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: required fields are missing: %s", ErrInvalidProvisionalRule, strings.Join(missing, ", "))
	}
	if generated.ID != baseline.ID || generated.Version != baseline.Version || generated.Entry != baseline.Entry || !jsonEqual(generated.Domain, baseline.Domain) {
		return nil, fmt.Errorf("%w: generated rule changed recording-derived id, version, entry, or domain", ErrInvalidProvisionalRule)
	}
	// A collection requirement describes one transport result row, while the
	// engine-level Rule.Output schema validates the entire ctx.extracted map.
	// Repeated-row rules legitimately keep an internal array (for example
	// extracted.items) and emit one row per loop iteration, so copying the row
	// schema into Rule.Output makes those rules impossible to execute. Discard
	// provider/baseline output here and retain the server-owned schema only in
	// the replay and immutable rule-version contracts.
	generated.Output = nil
	allowedDomains, err := ruleDomains(generated.Domain)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProvisionalRule, err)
	}
	if err := validateEntryDomain(generated.Entry, allowedDomains); err != nil {
		return nil, err
	}
	var steps []any
	if err := json.Unmarshal(generated.Steps, &steps); err != nil {
		return nil, fmt.Errorf("%w: steps must be an array", ErrInvalidProvisionalRule)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: steps must not be empty", ErrInvalidProvisionalRule)
	}
	if err := validateWorkflowSteps(steps, "steps", allowedDomains, false, false); err != nil {
		return nil, err
	}
	// Validate action-specific required fields and types only after replay
	// policy checks so unsafe actions keep their terminal classification.
	if err := validateActionSchemaTree(steps, "steps"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProvisionalRule, err)
	}
	selectors := map[string]any{}
	if requireResult && len(generated.Selectors) > 0 {
		if err := json.Unmarshal(generated.Selectors, &selectors); err != nil {
			return nil, fmt.Errorf("%w: selectors must be an object", ErrInvalidProvisionalRule)
		}
	}
	if requireResult {
		if err := validateVisibleExtractionSources(steps, "steps", selectors); err != nil {
			return nil, err
		}
		if err := validateDuplicateAppendedInputBindings(steps, "steps", selectors, requirement); err != nil {
			return nil, err
		}
	}
	if len(generated.Hooks) > 0 {
		var hooks map[string]any
		if err := json.Unmarshal(generated.Hooks, &hooks); err != nil {
			return nil, fmt.Errorf("%w: hooks must be an object", ErrInvalidProvisionalRule)
		}
		for _, name := range []string{"beforeAll", "afterAll", "onError", "cleanup"} {
			if raw, ok := hooks[name]; ok {
				hookSteps, ok := raw.([]any)
				if !ok {
					return nil, fmt.Errorf("%w: hooks.%s must be an array", ErrInvalidProvisionalRule, name)
				}
				if err := validateWorkflowSteps(hookSteps, "hooks."+name, allowedDomains, true, false); err != nil {
					return nil, err
				}
				if err := validateActionSchemaTree(hookSteps, "hooks."+name); err != nil {
					return nil, fmt.Errorf("%w: %v", ErrInvalidProvisionalRule, err)
				}
				if requireResult {
					if err := validateVisibleExtractionSources(hookSteps, "hooks."+name, selectors); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	// Preserve the legacy rule-level name/entry/steps sanity checks as well.
	ruleMap, err := modelRuleMap(generated)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProvisionalRule, err)
	}
	if err := Validate(ruleMap); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProvisionalRule, err)
	}
	if requireResult {
		if err := validateCollectionResultContract(steps, generated.Hooks, requirement); err != nil {
			return nil, err
		}
		if err := validateRequiredWorkflowInputReferences(generated, requirement); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidProvisionalRule, err)
		}
	}
	if _, err := BuildRequirementOutputSchema(requirement); err != nil {
		return nil, err
	}
	mergeRequirementVariables(generated, requirement)
	flags := []string{"schema-validation:passed", "replay-preflight:passed", "output-contract:validated"}
	if targetMetadataNormalized {
		flags = append(flags, targetMetadataNormalizedFlag)
	}
	if visibleExtractionNormalized {
		flags = append(flags, visibleExtractionNormalizedFlag)
	}
	if repeatedExtractionEmptyNormalized {
		flags = append(flags, repeatedExtractionEmptyNormalizedFlag)
	}
	if repeatedExtractionReadinessNormalized {
		flags = append(flags, repeatedExtractionReadinessNormalizedFlag)
	}
	if requirementInputReferencesNormalized {
		flags = append(flags, requirementInputReferencesNormalizedFlag)
	}
	if extractionReferencesNormalized {
		flags = append(flags, "extraction-refs:canonicalized")
	}
	if requireResult {
		flags = append(flags, "result-contract:passed")
	}
	return flags, nil
}

// validateDuplicateAppendedInputBindings rejects provider rules that first
// enter one complete task input and then append that same complete binding to
// the same target. Such a sequence deterministically duplicates the runtime
// value (for example "New YorkNew York") and must fail before replay. A clear
// resets the target, branches retain independent path state, and loop bodies
// are checked independently so ordinary per-iteration input remains valid.
func validateDuplicateAppendedInputBindings(
	steps []any,
	path string,
	selectors map[string]any,
	requirement models.CollectionRequirementSpec,
) error {
	declared := make(map[string]bool)
	for _, input := range append(append([]models.RequirementInput{}, requirement.RequiredInputs...), requirement.OptionalInputs...) {
		declared[input.Name] = true
	}
	var walk func([]any, string, map[string]bool) error
	walk = func(values []any, currentPath string, seen map[string]bool) error {
		for index, raw := range values {
			step, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			stepPath := fmt.Sprintf("%s[%d]", currentPath, index)
			targetKey := workflowInputTargetIdentity(step["target"], selectors)
			action, _ := step["action"].(string)
			if action == "clear" && targetKey != "" {
				for key := range seen {
					if strings.HasPrefix(key, targetKey+"\x00") {
						delete(seen, key)
					}
				}
			}
			if action == "type" && targetKey != "" {
				value, _ := step["value"].(string)
				inputName := exactRequirementInputReference(value, declared)
				if inputName != "" {
					key := targetKey + "\x00" + inputName
					appendValue, _ := step["append"].(bool)
					if appendValue && seen[key] {
						return fmt.Errorf("%w: %s appends complete task input %q to a target that already received that binding", ErrInvalidProvisionalRule, stepPath, inputName)
					}
					seen[key] = true
				}
			}
			for _, branch := range []string{"then", "else", "default"} {
				if children, ok := step[branch].([]any); ok {
					if err := walk(children, stepPath+"."+branch, cloneBindingState(seen)); err != nil {
						return err
					}
				}
			}
			if children, ok := step["steps"].([]any); ok {
				if err := walk(children, stepPath+".steps", make(map[string]bool)); err != nil {
					return err
				}
			}
			if cases, ok := step["cases"].([]any); ok {
				for caseIndex, rawCase := range cases {
					caseValue, _ := rawCase.(map[string]any)
					caseSteps, _ := caseValue["steps"].([]any)
					if err := walk(caseSteps, fmt.Sprintf("%s.cases[%d].steps", stepPath, caseIndex), cloneBindingState(seen)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return walk(steps, path, make(map[string]bool))
}

func workflowInputTargetIdentity(raw any, selectors map[string]any) string {
	target, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	resolved := target
	if ref, _ := target["$ref"].(string); strings.TrimSpace(ref) != "" {
		if alias, exists := selectors[strings.TrimSpace(ref)].(map[string]any); exists {
			resolved = alias
		}
	}
	return workflowTargetIdentity(resolved)
}

func exactRequirementInputReference(value string, declared map[string]bool) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "{{"), "}}"))
	if declared[name] {
		return name
	}
	return ""
}

func cloneBindingState(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func validateVisibleExtractionSources(steps []any, path string, selectors map[string]any) error {
	for index, raw := range steps {
		step, _ := raw.(map[string]any)
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		action, _ := step["action"].(string)
		if extractionWorkflowActions[action] {
			if target, ok := step["target"].(map[string]any); ok {
				resolved := target
				if ref, _ := target["$ref"].(string); strings.TrimSpace(ref) != "" {
					alias, exists := selectors[strings.TrimSpace(ref)].(map[string]any)
					if !exists {
						return fmt.Errorf("%w: %s.target references unknown selector %q", ErrInvalidProvisionalRule, stepPath, ref)
					}
					resolved = alias
				}
				if targetAddressesHiddenFallback(target) || targetAddressesHiddenFallback(resolved) {
					return fmt.Errorf("%w: %s.target must not address hidden or noscript fallback content", ErrInvalidProvisionalRule, stepPath)
				}
				visible, declaredInline := target["visible"]
				if !declaredInline {
					visible = resolved["visible"]
				}
				if visible != true {
					return fmt.Errorf("%w: %s.target must set visible=true for generated collection extraction", ErrInvalidProvisionalRule, stepPath)
				}
			}
		}
		for _, branch := range nestedActionListKeys {
			if children, ok := step[branch].([]any); ok {
				if err := validateVisibleExtractionSources(children, stepPath+"."+branch, selectors); err != nil {
					return err
				}
			}
		}
		if trigger, ok := step["trigger"].(map[string]any); ok {
			if err := validateVisibleExtractionSources([]any{trigger}, stepPath+".trigger", selectors); err != nil {
				return err
			}
		}
		if cases, ok := step["cases"].([]any); ok {
			for caseIndex, rawCase := range cases {
				caseValue, _ := rawCase.(map[string]any)
				caseSteps, _ := caseValue["steps"].([]any)
				if err := validateVisibleExtractionSources(caseSteps, fmt.Sprintf("%s.cases[%d].steps", stepPath, caseIndex), selectors); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func targetAddressesHiddenFallback(target map[string]any) bool {
	encoded, _ := json.Marshal(target)
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "", "\"", "", "'", "").Replace(strings.ToLower(string(encoded)))
	for _, marker := range []string{"noscript", "[hidden]", "type=hidden", "aria-hidden=true", "display:none", "visibility:hidden"} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func validateCollectionResultContract(steps []any, rawHooks models.JSON, requirement models.CollectionRequirementSpec) error {
	expected := make(map[string]models.RequirementValueType, len(requirement.OutputFields))
	for _, field := range requirement.OutputFields {
		expected[field.Name] = field.Type
	}
	repeatedExtractions := make(map[string]bool)
	collectRepeatedExtractionNames(steps, repeatedExtractions)
	knownExtractionTypes := make(map[string]models.RequirementValueType)
	ambiguousExtractionTypes := make(map[string]bool)
	collectKnownExtractionTypes(steps, knownExtractionTypes, ambiguousExtractionTypes)
	producedExtractionPaths := make(map[string]bool)
	collectProducedExtractionPaths(steps, producedExtractionPaths)
	found := 0
	var validateSteps func(values []any, path string) error
	validateSteps = func(values []any, path string) error {
		for index, raw := range values {
			step, ok := raw.(map[string]any)
			if !ok {
				continue // The structural validator reports this before this function runs.
			}
			stepPath := fmt.Sprintf("%s[%d]", path, index)
			if step["action"] == "sendResult" {
				payload, ok := step["payload"].(map[string]any)
				if !ok {
					return fmt.Errorf("%w: %s.payload must be an object", ErrInvalidProvisionalRule, stepPath)
				}
				for name := range payload {
					if _, exists := expected[name]; !exists {
						return fmt.Errorf("%w: %s.payload has undeclared output field %q", ErrInvalidProvisionalRule, stepPath, name)
					}
				}
				for name, fieldType := range expected {
					value, exists := payload[name]
					if !exists {
						return fmt.Errorf("%w: %s.payload is missing confirmed output field %q", ErrInvalidProvisionalRule, stepPath, name)
					}
					if fieldType == models.RequirementValueArray && !isExtractedCollectionReference(value) {
						return fmt.Errorf("%w: %s.payload field %q is confirmed as array and must directly reference an extracted collection such as %q; literal arrays and scalar wrappers are not allowed", ErrInvalidProvisionalRule, stepPath, name, "{{extracted."+name+"}}")
					}
					if fieldType != models.RequirementValueArray {
						if referencePath, ok := extractedReferencePath(value); ok {
							root := strings.SplitN(referencePath, ".", 2)[0]
							if repeatedExtractions[root] {
								return fmt.Errorf("%w: %s.payload scalar field %q must not reference repeated extraction %q directly; iterate it with loop.forEach and reference the current loopItem", ErrInvalidProvisionalRule, stepPath, name, root)
							}
						}
					}
					if referencePath, ok := extractedReferencePath(value); ok {
						if !producedExtractionPaths[referencePath] {
							return fmt.Errorf("%w: %s.payload field %q references %q, but no extraction or transform produces that exact path", ErrInvalidProvisionalRule, stepPath, name, referencePath)
						}
						if actualType, known := knownExtractionTypes[referencePath]; known && actualType != fieldType {
							return fmt.Errorf("%w: %s.payload field %q is confirmed as %s but direct reference %q is produced as %s; use a type-compatible extraction or transform", ErrInvalidProvisionalRule, stepPath, name, fieldType, referencePath, actualType)
						}
					}
				}
				found++
			}
			for _, branch := range []string{"then", "else", "steps", "default"} {
				if children, ok := step[branch].([]any); ok {
					if err := validateSteps(children, stepPath+"."+branch); err != nil {
						return err
					}
				}
			}
			if cases, ok := step["cases"].([]any); ok {
				for caseIndex, rawCase := range cases {
					caseValue, _ := rawCase.(map[string]any)
					caseSteps, _ := caseValue["steps"].([]any)
					if err := validateSteps(caseSteps, fmt.Sprintf("%s.cases[%d].steps", stepPath, caseIndex)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := validateSteps(steps, "steps"); err != nil {
		return err
	}
	if len(rawHooks) > 0 {
		var hooks map[string]any
		if err := json.Unmarshal(rawHooks, &hooks); err == nil {
			// onError is not reachable during a successful replay and therefore
			// cannot satisfy the collection-result contract.
			for _, name := range []string{"beforeAll", "afterAll", "cleanup"} {
				if hookSteps, ok := hooks[name].([]any); ok {
					if err := validateSteps(hookSteps, "hooks."+name); err != nil {
						return err
					}
				}
			}
		}
	}
	if found == 0 {
		return fmt.Errorf("%w: successful execution must submit the confirmed output fields with sendResult", ErrInvalidProvisionalRule)
	}
	return nil
}

func collectProducedExtractionPaths(steps []any, paths map[string]bool) {
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := step["name"].(string)
		name = strings.TrimSpace(name)
		action, _ := step["action"].(string)
		if name != "" {
			switch action {
			case "extract", "extractText", "extractAttribute", "extractHtml", "extractTable",
				"extractJson", "extractPageInfo", "merge", "transform", "filter", "deduplicate":
				paths[name] = true
			}
			if action == "extract" {
				if fields, ok := step["fields"].(map[string]any); ok {
					collectProducedExtractFieldPaths(name, fields, paths)
				}
			}
			// extractPageInfo stores each facet under the extraction name:
			// a field "website" produces the path "<name>.website".
			if action == "extractPageInfo" {
				if fields, ok := step["fields"].(map[string]any); ok {
					for fieldName := range fields {
						paths[name+"."+fieldName] = true
					}
				}
			}
		}
		for _, branch := range []string{"then", "else", "steps", "default"} {
			if children, ok := step[branch].([]any); ok {
				collectProducedExtractionPaths(children, paths)
			}
		}
		if cases, ok := step["cases"].([]any); ok {
			for _, rawCase := range cases {
				caseValue, _ := rawCase.(map[string]any)
				caseSteps, _ := caseValue["steps"].([]any)
				collectProducedExtractionPaths(caseSteps, paths)
			}
		}
	}
}

func collectProducedExtractFieldPaths(parent string, fields map[string]any, paths map[string]bool) {
	for fieldName, rawField := range fields {
		path := parent + "." + fieldName
		paths[path] = true
		field, _ := rawField.(map[string]any)
		if nested, ok := field["fields"].(map[string]any); ok {
			collectProducedExtractFieldPaths(path, nested, paths)
		}
	}
}

func collectRepeatedExtractionNames(steps []any, names map[string]bool) {
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if step["action"] == "extract" && step["multiple"] == true {
			if name, ok := step["name"].(string); ok && strings.TrimSpace(name) != "" {
				names[strings.TrimSpace(name)] = true
			}
		}
		for _, branch := range []string{"then", "else", "steps", "default"} {
			if children, ok := step[branch].([]any); ok {
				collectRepeatedExtractionNames(children, names)
			}
		}
		if cases, ok := step["cases"].([]any); ok {
			for _, rawCase := range cases {
				caseValue, _ := rawCase.(map[string]any)
				caseSteps, _ := caseValue["steps"].([]any)
				collectRepeatedExtractionNames(caseSteps, names)
			}
		}
	}
}

func collectKnownExtractionTypes(steps []any, types map[string]models.RequirementValueType, ambiguous map[string]bool) {
	// Keep this index conservative: only record producer types guaranteed by
	// runtime action semantics. Unknown or conflicting paths remain replay-time
	// concerns instead of causing a speculative static rejection.
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := step["name"].(string)
		name = strings.TrimSpace(name)
		if name != "" {
			switch step["action"] {
			case "extractText", "extractAttribute", "extractHtml":
				setKnownExtractionType(types, ambiguous, name, models.RequirementValueString)
			case "extractTable", "filter", "deduplicate":
				setKnownExtractionType(types, ambiguous, name, models.RequirementValueArray)
			case "extractPageInfo":
				setKnownExtractionType(types, ambiguous, name, models.RequirementValueObject)
			case "extract":
				if step["multiple"] == true {
					setKnownExtractionType(types, ambiguous, name, models.RequirementValueArray)
				} else {
					setKnownExtractionType(types, ambiguous, name, models.RequirementValueObject)
					if fields, ok := step["fields"].(map[string]any); ok {
						collectKnownExtractFieldTypes(name, fields, types, ambiguous)
					}
				}
			case "transform":
				if transformedType, known := knownTransformResultType(step, types, ambiguous); known {
					setKnownExtractionType(types, ambiguous, name, transformedType)
				}
			}
		}
		for _, branch := range []string{"then", "else", "steps", "default"} {
			if children, ok := step[branch].([]any); ok {
				collectKnownExtractionTypes(children, types, ambiguous)
			}
		}
		if cases, ok := step["cases"].([]any); ok {
			for _, rawCase := range cases {
				caseValue, _ := rawCase.(map[string]any)
				caseSteps, _ := caseValue["steps"].([]any)
				collectKnownExtractionTypes(caseSteps, types, ambiguous)
			}
		}
	}
}

func collectKnownExtractFieldTypes(
	parent string,
	fields map[string]any,
	types map[string]models.RequirementValueType,
	ambiguous map[string]bool,
) {
	for fieldName, rawField := range fields {
		field, ok := rawField.(map[string]any)
		if !ok {
			continue
		}
		path := parent + "." + fieldName
		if nested, ok := field["fields"].(map[string]any); ok && len(nested) > 0 {
			// Runtime returns the recursively extracted object before switching
			// on the scalar `type`; the structural marker type is never emitted.
			setKnownExtractionType(types, ambiguous, path, models.RequirementValueObject)
			collectKnownExtractFieldTypes(path, nested, types, ambiguous)
			continue
		}
		if fieldType, known := knownExtractFieldType(field["type"]); known {
			setKnownExtractionType(types, ambiguous, path, fieldType)
		}
	}
}

func setKnownExtractionType(types map[string]models.RequirementValueType, ambiguous map[string]bool, path string, fieldType models.RequirementValueType) {
	if ambiguous[path] {
		return
	}
	if existing, ok := types[path]; ok && existing != fieldType {
		delete(types, path)
		ambiguous[path] = true
		return
	}
	types[path] = fieldType
}

func knownExtractFieldType(raw any) (models.RequirementValueType, bool) {
	fieldType, _ := raw.(string)
	switch fieldType {
	case "text", "html", "attr", "css", "regex":
		return models.RequirementValueString, true
	case "number", "count":
		return models.RequirementValueNumber, true
	case "boolean", "exists":
		return models.RequirementValueBoolean, true
	default:
		return "", false
	}
}

func knownTransformResultType(step map[string]any, types map[string]models.RequirementValueType, ambiguous map[string]bool) (models.RequirementValueType, bool) {
	from, _ := step["from"].(string)
	from = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(from), "extracted."))
	current, known := types[from]
	if ambiguous[from] {
		known = false
	}
	operations, _ := step["operations"].([]any)
	for _, raw := range operations {
		operation, _ := raw.(map[string]any)
		operationType, _ := operation["type"].(string)
		field, _ := operation["field"].(string)
		targetsField := strings.TrimSpace(field) != ""
		switch operationType {
		case "number":
			if !targetsField {
				current, known = models.RequirementValueNumber, true
			}
		case "filter":
			current, known = models.RequirementValueArray, true
		case "date":
			if !targetsField {
				current, known = models.RequirementValueString, true
			}
		case "jsonParse":
			if !targetsField {
				known = false
			}
		}
	}
	return current, known
}

func isExtractedCollectionReference(value any) bool {
	_, ok := extractedReferencePath(value)
	return ok
}

func extractedReferencePath(value any) (string, bool) {
	reference, ok := value.(string)
	if !ok {
		return "", false
	}
	reference = strings.TrimSpace(reference)
	if !strings.HasPrefix(reference, "{{extracted.") || !strings.HasSuffix(reference, "}}") {
		return "", false
	}
	path := strings.TrimSuffix(strings.TrimPrefix(reference, "{{extracted."), "}}")
	if path == "" || strings.ContainsAny(path, "{} \t\r\n") {
		return "", false
	}
	return path, true
}

func validateWorkflowSteps(steps []any, path string, allowedDomains []string, inHook, inWhileElementExists bool) error {
	for index, raw := range steps {
		step, ok := raw.(map[string]any)
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		if !ok {
			return fmt.Errorf("%w: %s must be an object", ErrInvalidProvisionalRule, stepPath)
		}
		action, ok := step["action"].(string)
		if !ok || action == "" {
			return fmt.Errorf("%w: %s.action is required", ErrInvalidProvisionalRule, stepPath)
		}
		if reason, forbidden := forbiddenWorkflowActions[action]; forbidden {
			return fmt.Errorf("%w: %s action %q: %s", ErrUnsafeProvisionalRule, stepPath, action, reason)
		}
		if reason, unsupported := replayUnsupportedWorkflowActions[action]; unsupported {
			return fmt.Errorf("%w: %s action %q: %s", ErrInvalidProvisionalRule, stepPath, action, reason)
		}
		if !allowedWorkflowActions[action] {
			return fmt.Errorf("%w: %s has unknown action %q", ErrInvalidProvisionalRule, stepPath, action)
		}
		if condition, ok := step["condition"].(map[string]any); ok && condition["type"] == "jsTruthy" {
			return fmt.Errorf("%w: %s.condition uses disabled JavaScript", ErrUnsafeProvisionalRule, stepPath)
		}
		if actionMayNavigateMap(step) {
			if inHook {
				return fmt.Errorf("%w: navigation inside %s cannot be checkpointed", ErrInvalidProvisionalRule, stepPath)
			}
			if inWhileElementExists {
				return fmt.Errorf(
					"%w: navigation inside loop.whileElementExists cannot be replayed because historical DOM cannot be re-entered at %s",
					ErrInvalidProvisionalRule,
					stepPath,
				)
			}
			if err := validateStaticNavigation(step, stepPath, allowedDomains); err != nil {
				return err
			}
		}
		for _, branch := range []string{"then", "else", "steps", "default"} {
			if rawBranch, exists := step[branch]; exists {
				children, ok := rawBranch.([]any)
				if !ok {
					return fmt.Errorf("%w: %s.%s must be an array", ErrInvalidProvisionalRule, stepPath, branch)
				}
				nestedWhileElementExists := inWhileElementExists ||
					(action == "loop" && step["type"] == "whileElementExists" && branch == "steps")
				if err := validateWorkflowSteps(
					children,
					stepPath+"."+branch,
					allowedDomains,
					inHook,
					nestedWhileElementExists,
				); err != nil {
					return err
				}
			}
		}
		if cases, exists := step["cases"]; exists {
			caseList, ok := cases.([]any)
			if !ok {
				return fmt.Errorf("%w: %s.cases must be an array", ErrInvalidProvisionalRule, stepPath)
			}
			for caseIndex, rawCase := range caseList {
				caseValue, ok := rawCase.(map[string]any)
				if !ok {
					return fmt.Errorf("%w: %s.cases[%d] must be an object", ErrInvalidProvisionalRule, stepPath, caseIndex)
				}
				caseSteps, ok := caseValue["steps"].([]any)
				if !ok {
					return fmt.Errorf("%w: %s.cases[%d].steps must be an array", ErrInvalidProvisionalRule, stepPath, caseIndex)
				}
				if err := validateWorkflowSteps(
					caseSteps,
					fmt.Sprintf("%s.cases[%d].steps", stepPath, caseIndex),
					allowedDomains,
					inHook,
					inWhileElementExists,
				); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func actionMayNavigateMap(step map[string]any) bool {
	action, _ := step["action"].(string)
	switch action {
	case "navigate", "reload", "goBack", "goForward", "openTab":
		return true
	case "click", "doubleClick", "middleClick", "rightClick", "hoverClick", "pressKey", "keyCombination", "typeAndSelect":
		return true
	default:
		return false
	}
}

func validateStaticNavigation(step map[string]any, path string, allowedDomains []string) error {
	action, _ := step["action"].(string)
	if action != "navigate" && action != "openTab" {
		return nil
	}
	raw, _ := step["url"].(string)
	if raw == "" || strings.Contains(raw, "{{") || strings.Contains(raw, "}}") {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s contains an invalid navigation URL", ErrInvalidProvisionalRule, path)
	}
	if !parsed.IsAbs() {
		return nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s navigation must use HTTP(S)", ErrUnsafeProvisionalRule, path)
	}
	if !domainAllowed(parsed.Hostname(), allowedDomains) {
		return fmt.Errorf("%w: %s navigates outside approved domains", ErrUnsafeProvisionalRule, path)
	}
	return nil
}

func validateEntryDomain(entry string, allowedDomains []string) error {
	parsed, err := url.Parse(entry)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w: entry must be an absolute HTTP(S) URL", ErrInvalidProvisionalRule)
	}
	if !domainAllowed(parsed.Hostname(), allowedDomains) {
		return fmt.Errorf("%w: entry is outside approved domains", ErrUnsafeProvisionalRule)
	}
	return nil
}

func ruleDomains(raw models.JSON) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && strings.TrimSpace(single) != "" {
		return []string{strings.ToLower(strings.TrimSpace(single))}, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil || len(multiple) == 0 {
		return nil, errors.New("rule domain must be a string or non-empty string array")
	}
	for index, domain := range multiple {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			return nil, errors.New("rule domain contains an empty value")
		}
		multiple[index] = domain
	}
	return multiple, nil
}

func domainAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, domain := range allowed {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// BuildRequirementOutputSchema returns the object schema for one collected
// row. Replay validation accepts either one row or an ordered batch of rows.
func BuildRequirementOutputSchema(requirement models.CollectionRequirementSpec) (models.JSON, error) {
	if len(requirement.OutputFields) == 0 {
		return nil, fmt.Errorf("%w: requirement output fields are empty", ErrInvalidProvisionalRule)
	}
	properties := make(map[string]any, len(requirement.OutputFields))
	required := make([]string, 0, len(requirement.OutputFields))
	for _, field := range requirement.OutputFields {
		if strings.TrimSpace(field.Name) == "" || !validRequirementType(field.Type) {
			return nil, fmt.Errorf("%w: invalid output field %q", ErrInvalidProvisionalRule, field.Name)
		}
		if _, exists := properties[field.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate output field %q", ErrInvalidProvisionalRule, field.Name)
		}
		properties[field.Name] = map[string]any{"type": string(field.Type), "description": field.Description}
		required = append(required, field.Name)
	}
	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
	encoded, err := json.Marshal(schema)
	return models.JSON(encoded), err
}

// ValidateReplayOutput validates every returned row against the confirmed
// requirement field contract before a workflow may be confirmed.
func ValidateReplayOutput(output any, requirement models.CollectionRequirementSpec) error {
	rows := []any{output}
	if list, ok := output.([]any); ok {
		rows = list
	}
	if len(rows) == 0 {
		return fmt.Errorf("%w: no result rows were collected", ErrInvalidReplayOutput)
	}
	const maxReplayOutputRowsHard = 5000
	if len(rows) > maxReplayOutputRowsHard {
		return fmt.Errorf("%w: output has %d rows, exceeding the hard cap of %d", ErrInvalidReplayOutput, len(rows), maxReplayOutputRowsHard)
	}
	allowed := make(map[string]models.RequirementValueType, len(requirement.OutputFields))
	for _, field := range requirement.OutputFields {
		allowed[field.Name] = field.Type
	}
	for rowIndex, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: row %d must be an object", ErrInvalidReplayOutput, rowIndex)
		}
		for _, field := range requirement.OutputFields {
			value, exists := row[field.Name]
			if !exists {
				return fmt.Errorf("%w: row %d is missing %q", ErrInvalidReplayOutput, rowIndex, field.Name)
			}
			if !requirementValueMatches(value, field.Type) {
				return fmt.Errorf("%w: row %d field %q must be %s", ErrInvalidReplayOutput, rowIndex, field.Name, field.Type)
			}
		}
		for name := range row {
			if _, exists := allowed[name]; !exists {
				return fmt.Errorf("%w: row %d has undeclared field %q", ErrInvalidReplayOutput, rowIndex, name)
			}
		}
	}
	return nil
}

func requirementValueMatches(value any, expected models.RequirementValueType) bool {
	switch expected {
	case models.RequirementValueString:
		_, ok := value.(string)
		return ok
	case models.RequirementValueNumber:
		switch value.(type) {
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		default:
			return false
		}
	case models.RequirementValueBoolean:
		_, ok := value.(bool)
		return ok
	case models.RequirementValueObject:
		_, ok := value.(map[string]any)
		return ok
	case models.RequirementValueArray:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func validRequirementType(value models.RequirementValueType) bool {
	switch value {
	case models.RequirementValueString, models.RequirementValueNumber, models.RequirementValueBoolean, models.RequirementValueObject, models.RequirementValueArray:
		return true
	default:
		return false
	}
}

func mergeRequirementVariables(rule *models.Rule, requirement models.CollectionRequirementSpec) {
	variables := map[string]any{}
	if len(rule.Variables) > 0 {
		_ = json.Unmarshal(rule.Variables, &variables)
	}
	for _, input := range append(append([]models.RequirementInput{}, requirement.RequiredInputs...), requirement.OptionalInputs...) {
		if _, exists := variables[input.Name]; exists {
			continue
		}
		if input.Default != nil {
			variables[input.Name] = input.Default
			continue
		}
		switch input.Type {
		case models.RequirementValueString:
			variables[input.Name] = ""
		case models.RequirementValueNumber:
			variables[input.Name] = 0
		case models.RequirementValueBoolean:
			variables[input.Name] = false
		case models.RequirementValueObject:
			variables[input.Name] = map[string]any{}
		case models.RequirementValueArray:
			variables[input.Name] = []any{}
		}
	}
	rule.Variables, _ = json.Marshal(variables)
}

func modelRuleMap(rule *models.Rule) (map[string]any, error) {
	encoded, err := json.Marshal(rule)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(encoded, &result)
	return result, err
}

func jsonEqual(left, right models.JSON) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}
