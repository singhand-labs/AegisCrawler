package rule

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const maxSelectorRepairAssignments = 128

var errSelectorRepairAssignmentOverflow = fmt.Errorf(
	"%w: selector repair has more than %d complete compatible assignments",
	ErrInvalidProvisionalRule, maxSelectorRepairAssignments,
)

// SelectorRepairSlot is stored only in the sealed selector-repair job request.
// Path is the private server application mapping and is never included in an
// LLM prompt or response.
type SelectorRepairSlot struct {
	ID                  string   `json:"slotId"`
	Kind                string   `json:"kind"`
	CurrentCandidateID  string   `json:"currentCandidateId"`
	AllowedCandidateIDs []string `json:"allowedCandidateIds"`
	Path                []string `json:"path"`
}

// SelectorRepairPlan contains a bounded list of complete compatible tuples.
// Assignment keys are opaque slot IDs, never JSON/CSS paths. The provider may
// return a partial patch only when current+replacement values equal one tuple.
type SelectorRepairPlan struct {
	Slots              []SelectorRepairSlot `json:"slots"`
	AllowedAssignments []map[string]string  `json:"allowedAssignments"`
	actionDisplay      string
}

type selectorRepairFieldSlot struct {
	kind    string
	current string
	path    []string
}

// BuildSelectorRepairPlan derives one bounded mutable unit: all and only
// opaque-ID slots in the failing extraction action. Other actions, hooks,
// metadata, output, and field modifiers never receive slot IDs.
func (c *SelectorEvidenceCatalog) BuildSelectorRepairPlan(
	rule *models.Rule,
	failure *SelectorCandidateSelectionError,
	sourceNonce string,
) (*SelectorRepairPlan, error) {
	if c == nil || c.promptCatalogHash == "" || rule == nil || failure == nil ||
		strings.TrimSpace(sourceNonce) == "" {
		return nil, fmt.Errorf("%w: selector repair source is incomplete", ErrInvalidProvisionalRule)
	}
	_, steps, hooks, err := selectorCandidateRuleParts(rule)
	if err != nil {
		return nil, err
	}
	var selected *SelectorRepairPlan
	var selectedErr error
	var walkActions func([]any, string, []string)
	var walkAction func(map[string]any, string, []string)
	walkAction = func(step map[string]any, stepDisplay string, stepTokens []string) {
		ownsFailure := failure.Slot == stepDisplay+".target" ||
			strings.HasPrefix(failure.Slot, stepDisplay+".target.") ||
			failure.Slot == stepDisplay+".fields" ||
			strings.HasPrefix(failure.Slot, stepDisplay+".fields.")
		if selected == nil && selectedErr == nil && ownsFailure {
			selected, selectedErr = c.selectorRepairActionPlan(
				step, stepDisplay, stepTokens, sourceNonce,
			)
		}
		for _, branch := range nestedActionListKeys {
			if children, ok := step[branch].([]any); ok {
				walkActions(children, stepDisplay+"."+branch, append(stepTokens, branch))
			}
		}
		if trigger, ok := step["trigger"].(map[string]any); ok {
			// Candidate validation displays a map-valued trigger as trigger[0]
			// because it walks a synthetic one-element slice. Its private
			// storage path remains the actual object path without an array
			// token so a sealed slot can be applied to the original JSON.
			walkAction(trigger, stepDisplay+".trigger[0]", append(stepTokens, "trigger"))
		}
		if cases, ok := step["cases"].([]any); ok {
			for caseIndex, rawCase := range cases {
				caseValue, _ := rawCase.(map[string]any)
				caseSteps, _ := caseValue["steps"].([]any)
				walkActions(caseSteps, fmt.Sprintf("%s.cases[%d].steps", stepDisplay, caseIndex),
					append(stepTokens, "cases", "#"+strconv.Itoa(caseIndex), "steps"))
			}
		}
	}
	walkActions = func(values []any, display string, tokens []string) {
		for index, raw := range values {
			step, _ := raw.(map[string]any)
			if step == nil {
				continue
			}
			stepDisplay := fmt.Sprintf("%s[%d]", display, index)
			stepTokens := append(append([]string(nil), tokens...), "#"+strconv.Itoa(index))
			walkAction(step, stepDisplay, stepTokens)
		}
	}
	walkActions(steps, "steps", []string{"steps"})
	for _, name := range selectorCandidateHookNames {
		if selected != nil || selectedErr != nil {
			break
		}
		if values, ok := hooks[name]; ok {
			walkActions(values, "hooks."+name, []string{"hooks", name})
		}
	}
	if selectedErr != nil {
		return nil, selectedErr
	}
	if selected == nil || len(selected.Slots) == 0 ||
		len(selected.AllowedAssignments) == 0 {
		return nil, fmt.Errorf("%w: failed extraction action has no complete evidence-valid repair assignment", ErrInvalidProvisionalRule)
	}
	alternative := false
	for _, assignment := range selected.AllowedAssignments {
		for _, slot := range selected.Slots {
			location := selectorRepairActionRelativeDisplayPath(slot.Path)
			failureLocation := strings.TrimPrefix(failure.Slot, selected.actionDisplay+".")
			if location != failureLocation &&
				!strings.HasPrefix(location, failureLocation+".") {
				continue
			}
			if assignment[slot.ID] != "" && assignment[slot.ID] != slot.CurrentCandidateID {
				alternative = true
				break
			}
		}
	}
	if !alternative {
		return nil, fmt.Errorf("%w: failed extraction action has no different evidence-valid assignment", ErrInvalidProvisionalRule)
	}
	return selected, nil
}

func (c *SelectorEvidenceCatalog) selectorRepairActionPlan(
	step map[string]any,
	display string,
	tokens []string,
	sourceNonce string,
) (*SelectorRepairPlan, error) {
	action := strings.TrimSpace(stringValue(step["action"]))
	if !extractionWorkflowActions[action] || action == "extractPageInfo" {
		return nil, fmt.Errorf("%w: failed action is not selector-repairable", ErrInvalidProvisionalRule)
	}
	multiple, err := extractionActionUsesRepeatedTarget(step, action, display)
	if err != nil {
		return nil, err
	}
	target, ok := step["target"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: failed action target is missing", ErrInvalidProvisionalRule)
	}
	targetKind := "targetCandidateId"
	if multiple {
		targetKind = "rowCandidateId"
	}
	currentTarget, exists := target[targetKind]
	if !exists {
		return nil, fmt.Errorf("%w: failed action target has no mutable candidate slot", ErrInvalidProvisionalRule)
	}
	targetPath := append(tokens, "target", targetKind)
	currentTargetID := strings.TrimSpace(stringValue(currentTarget))
	fields, _ := step["fields"].(map[string]any)
	fieldSlots := collectSelectorRepairFieldSlots(fields, append(tokens, "fields"))
	pathAssignments := []map[string]string{}
	ids := make([]string, 0, len(c.promptTargets))
	for id := range c.promptTargets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		candidate := c.promptTargets[id]
		if multiple != (candidate.prompt.RowCandidateID != "") {
			continue
		}
		if capabilityErr := validateStandaloneExtractionCapability(
			step, action, candidate.nodeSets, display,
		); capabilityErr != nil {
			continue
		}
		fieldAssignments := []map[string]string{{}}
		if action == "extract" {
			fieldAssignments, err = solveSelectorRepairFields(
				fields, candidate, id, "", display+".fields", append(tokens, "fields"),
			)
			if err != nil {
				return nil, err
			}
		}
		for _, assignment := range fieldAssignments {
			if len(pathAssignments) >= maxSelectorRepairAssignments {
				return nil, errSelectorRepairAssignmentOverflow
			}
			complete := cloneSelectorRepairAssignment(assignment)
			complete[selectorRepairPathKey(targetPath)] = id
			pathAssignments = append(pathAssignments, complete)
		}
	}
	if len(pathAssignments) == 0 {
		return nil, fmt.Errorf("%w: no complete evidence-valid target/field assignment exists", ErrInvalidProvisionalRule)
	}
	slots := []SelectorRepairSlot{newSelectorRepairSlot(
		sourceNonce, targetKind, currentTargetID, targetPath,
		assignmentValues(pathAssignments, selectorRepairPathKey(targetPath)),
	)}
	for _, fieldSlot := range fieldSlots {
		key := selectorRepairPathKey(fieldSlot.path)
		allowed := assignmentValues(pathAssignments, key)
		if len(allowed) == 0 {
			return nil, fmt.Errorf("%w: no complete evidence-valid field assignment exists", ErrInvalidProvisionalRule)
		}
		slots = append(slots, newSelectorRepairSlot(
			sourceNonce, fieldSlot.kind, fieldSlot.current, fieldSlot.path, allowed,
		))
	}
	slotByPath := map[string]string{}
	for _, slot := range slots {
		slotByPath[selectorRepairPathKey(slot.Path)] = slot.ID
	}
	assignments := make([]map[string]string, 0, len(pathAssignments))
	for _, pathAssignment := range pathAssignments {
		assignment := map[string]string{}
		for path, candidateID := range pathAssignment {
			slotID := slotByPath[path]
			if slotID == "" {
				return nil, fmt.Errorf("%w: repair assignment references an unknown private slot", ErrInvalidProvisionalRule)
			}
			assignment[slotID] = candidateID
		}
		if len(assignment) != len(slots) {
			return nil, fmt.Errorf("%w: repair assignment is incomplete", ErrInvalidProvisionalRule)
		}
		assignments = append(assignments, assignment)
	}
	return &SelectorRepairPlan{
		Slots: slots, AllowedAssignments: assignments, actionDisplay: display,
	}, nil
}

func solveSelectorRepairFields(
	fields map[string]any,
	target *providerTargetEvidence,
	targetID, parentID, display string,
	tokens []string,
) ([]map[string]string, error) {
	combined := []map[string]string{{}}
	for _, name := range sortedMapKeys(fields) {
		field, _ := fields[name].(map[string]any)
		if field == nil {
			return nil, nil
		}
		fieldDisplay := display + "." + name
		fieldTokens := append(append([]string(nil), tokens...), name)
		if _, exists := field["fieldCandidateId"]; !exists {
			return nil, nil
		}
		ownKey := selectorRepairPathKey(append(fieldTokens, "fieldCandidateId"))
		options := []map[string]string{}
		for _, candidate := range target.fields {
			if candidate.targetID != targetID ||
				candidate.prompt.ParentFieldCandidateID != parentID {
				continue
			}
			nested, shapeErr := validateProviderFieldShape(field, fieldDisplay)
			if shapeErr != nil {
				continue
			}
			if _, capabilityErr := validateTypedFieldCapability(
				field, candidate.nodeSets, fieldDisplay, true,
			); capabilityErr != nil {
				continue
			}
			children := []map[string]string{{}}
			if nested != nil {
				children, shapeErr = solveSelectorRepairFields(
					nested, target, targetID, candidate.prompt.FieldCandidateID,
					fieldDisplay+".fields", append(fieldTokens, "fields"),
				)
				if shapeErr != nil {
					return nil, shapeErr
				}
			}
			for _, child := range children {
				if len(options) >= maxSelectorRepairAssignments {
					return nil, errSelectorRepairAssignmentOverflow
				}
				option := cloneSelectorRepairAssignment(child)
				option[ownKey] = candidate.prompt.FieldCandidateID
				options = append(options, option)
			}
		}
		if len(options) == 0 {
			return nil, nil
		}
		var combineErr error
		combined, combineErr = combineSelectorRepairAssignments(combined, options)
		if combineErr != nil {
			return nil, combineErr
		}
		if len(combined) == 0 {
			return nil, nil
		}
	}
	return combined, nil
}

func combineSelectorRepairAssignments(
	left, right []map[string]string,
) ([]map[string]string, error) {
	result := make([]map[string]string, 0)
	for _, first := range left {
		for _, second := range right {
			if len(result) >= maxSelectorRepairAssignments {
				return nil, errSelectorRepairAssignmentOverflow
			}
			combined := cloneSelectorRepairAssignment(first)
			for key, value := range second {
				combined[key] = value
			}
			result = append(result, combined)
		}
	}
	return result, nil
}

func cloneSelectorRepairAssignment(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func assignmentValues(assignments []map[string]string, key string) []string {
	values := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		values = append(values, assignment[key])
	}
	return sortedUniqueNonEmpty(values)
}

func collectSelectorRepairFieldSlots(fields map[string]any, tokens []string) []selectorRepairFieldSlot {
	result := []selectorRepairFieldSlot{}
	for _, name := range sortedMapKeys(fields) {
		field, _ := fields[name].(map[string]any)
		if field == nil {
			continue
		}
		fieldTokens := append(append([]string(nil), tokens...), name)
		if current, exists := field["fieldCandidateId"]; exists {
			result = append(result, selectorRepairFieldSlot{
				kind: "fieldCandidateId", current: strings.TrimSpace(stringValue(current)),
				path: append(fieldTokens, "fieldCandidateId"),
			})
		}
		if nested, ok := field["fields"].(map[string]any); ok {
			result = append(result,
				collectSelectorRepairFieldSlots(nested, append(fieldTokens, "fields"))...)
		}
	}
	return result
}

func newSelectorRepairSlot(
	sourceNonce, kind, current string,
	path, allowed []string,
) SelectorRepairSlot {
	sum := sha256.Sum256([]byte(sourceNonce + "\x00" + strings.Join(path, "\x00")))
	return SelectorRepairSlot{
		ID: "slot_" + fmt.Sprintf("%x", sum[:12]), Kind: kind,
		CurrentCandidateID: current, AllowedCandidateIDs: sortedUniqueNonEmpty(allowed),
		Path: append([]string(nil), path...),
	}
}

func selectorRepairPathKey(path []string) string {
	return strings.Join(path, "\x00")
}

func selectorRepairDisplayPath(path []string) string {
	var result strings.Builder
	for _, token := range path {
		if strings.HasPrefix(token, "#") {
			result.WriteByte('[')
			result.WriteString(strings.TrimPrefix(token, "#"))
			result.WriteByte(']')
			continue
		}
		if result.Len() > 0 {
			result.WriteByte('.')
		}
		result.WriteString(token)
	}
	return result.String()
}

func selectorRepairActionRelativeDisplayPath(path []string) string {
	for index, token := range path {
		if token == "target" || token == "fields" {
			return selectorRepairDisplayPath(path[index:])
		}
	}
	return selectorRepairDisplayPath(path)
}

func sortedUniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
