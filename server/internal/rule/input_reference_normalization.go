package rule

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const requirementInputReferencesNormalizedFlag = "requirement-inputs:canonicalized"

var workflowTemplateReference = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

// normalizeWorkflowRequirementInputReferences rewrites common provider-created
// input namespace aliases to PageAgent's actual top-level variable contract.
// It only rewrites names confirmed by the requirement and fails closed on
// unknown names so a replay cannot silently type an empty string.
func normalizeWorkflowRequirementInputReferences(
	rule *models.Rule,
	requirement models.CollectionRequirementSpec,
) (bool, error) {
	declared := make(map[string]bool, len(requirement.RequiredInputs)+len(requirement.OptionalInputs))
	for _, input := range requirement.RequiredInputs {
		declared[input.Name] = true
	}
	for _, input := range requirement.OptionalInputs {
		declared[input.Name] = true
	}

	changed := false
	for _, field := range []struct {
		name string
		raw  *models.JSON
	}{
		{name: "steps", raw: &rule.Steps},
		{name: "hooks", raw: &rule.Hooks},
		{name: "selectors", raw: &rule.Selectors},
	} {
		fieldChanged, err := normalizeRequirementInputReferencesJSON(field.raw, field.name, declared)
		if err != nil {
			return false, err
		}
		changed = changed || fieldChanged
	}
	return changed, nil
}

// validateRequiredWorkflowInputReferences ensures every value the confirmed
// requirement says a task must supply can affect execution. Declaring an input
// only in Rule.Variables (or an unused selector alias) is not a binding.
func validateRequiredWorkflowInputReferences(
	rule *models.Rule,
	requirement models.CollectionRequirementSpec,
) error {
	referenced := make(map[string]bool, len(requirement.RequiredInputs))
	for _, field := range []struct {
		name string
		raw  models.JSON
	}{
		{name: "steps", raw: rule.Steps},
		{name: "hooks", raw: rule.Hooks},
	} {
		if len(field.raw) == 0 {
			continue
		}
		var value any
		if err := json.Unmarshal(field.raw, &value); err != nil {
			return fmt.Errorf("%s: invalid JSON: %w", field.name, err)
		}
		collectWorkflowInputReferences(value, referenced)
	}
	missing := make([]string, 0)
	for _, input := range requirement.RequiredInputs {
		if !referenced[input.Name] {
			missing = append(missing, input.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required requirement inputs are not referenced by executable steps or hooks: %s", strings.Join(missing, ", "))
	}
	return nil
}

func collectWorkflowInputReferences(value any, referenced map[string]bool) {
	switch current := value.(type) {
	case string:
		for _, match := range workflowTemplateReference.FindAllStringSubmatch(current, -1) {
			expression := strings.TrimSpace(match[1])
			name := strings.SplitN(expression, ".", 2)[0]
			if name != "" {
				referenced[name] = true
			}
		}
	case []any:
		for _, child := range current {
			collectWorkflowInputReferences(child, referenced)
		}
	case map[string]any:
		for _, child := range current {
			collectWorkflowInputReferences(child, referenced)
		}
	}
}

func normalizeRequirementInputReferencesJSON(
	raw *models.JSON,
	path string,
	declared map[string]bool,
) (bool, error) {
	if raw == nil || len(*raw) == 0 {
		return false, nil
	}
	var value any
	if err := json.Unmarshal(*raw, &value); err != nil {
		return false, fmt.Errorf("%s: invalid JSON: %w", path, err)
	}
	changed, err := normalizeRequirementInputReferencesValue(&value, path, declared)
	if err != nil || !changed {
		return changed, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("%s: marshal normalized input references: %w", path, err)
	}
	*raw = models.JSON(encoded)
	return true, nil
}

func normalizeRequirementInputReferencesValue(
	value *any,
	path string,
	declared map[string]bool,
) (bool, error) {
	switch current := (*value).(type) {
	case string:
		normalized, changed, err := normalizeRequirementInputReferenceString(current, path, declared)
		if err != nil {
			return false, err
		}
		if changed {
			*value = normalized
		}
		return changed, nil
	case []any:
		changed := false
		for index := range current {
			itemChanged, err := normalizeRequirementInputReferencesValue(
				&current[index],
				fmt.Sprintf("%s[%d]", path, index),
				declared,
			)
			if err != nil {
				return false, err
			}
			changed = changed || itemChanged
		}
		return changed, nil
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		changed := false
		for _, key := range keys {
			child := current[key]
			childChanged, err := normalizeRequirementInputReferencesValue(
				&child,
				path+"."+key,
				declared,
			)
			if err != nil {
				return false, err
			}
			if childChanged {
				current[key] = child
			}
			changed = changed || childChanged
		}
		return changed, nil
	default:
		return false, nil
	}
}

func normalizeRequirementInputReferenceString(
	value string,
	path string,
	declared map[string]bool,
) (string, bool, error) {
	changed := false
	var normalizationErr error
	normalized := workflowTemplateReference.ReplaceAllStringFunc(value, func(match string) string {
		if normalizationErr != nil {
			return match
		}
		expression := strings.TrimSpace(match[2 : len(match)-2])
		parts := strings.Split(expression, ".")
		if len(parts) == 0 {
			return match
		}
		alias := parts[0]
		switch alias {
		case "input", "inputs", "taskInputs":
		default:
			return match
		}
		// A requirement may legitimately declare an input with one of these
		// names. In that uncommon case, preserve normal PageAgent property
		// access instead of treating the name as a provider-created namespace.
		if declared[alias] {
			return match
		}
		if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			normalizationErr = fmt.Errorf(
				"%s references unsupported %q input namespace without a declared input name",
				path,
				alias,
			)
			return match
		}
		inputName := strings.TrimSpace(parts[1])
		if !declared[inputName] {
			normalizationErr = fmt.Errorf(
				"%s references undeclared requirement input %q through unsupported %q namespace",
				path,
				inputName,
				alias,
			)
			return match
		}
		parts[1] = inputName
		changed = true
		return "{{" + strings.Join(parts[1:], ".") + "}}"
	})
	return normalized, changed, normalizationErr
}

const extractionReferencesNormalizedFlag = "extraction-refs:canonicalized"

// normalizeWorkflowExtractionReferences rewrites provider-created extraction
// references that omit the canonical `extracted.` namespace (e.g.
// `{{dest.website}}` for an extraction named "dest") to the canonical
// `{{extracted.dest.website}}`. It only rewrites roots that are declared
// extraction names and never touches `extracted.`, `loopItem`, `inputs.`, or
// declared requirement inputs, so unambiguous property access fails closed in
// validation instead of being silently retargeted.
func normalizeWorkflowExtractionReferences(rule *models.Rule) (bool, error) {
	var root any
	if len(rule.Steps) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(rule.Steps, &root); err != nil {
		return false, fmt.Errorf("steps: invalid JSON: %w", err)
	}
	steps, ok := root.([]any)
	if !ok {
		return false, nil
	}
	extractionNames := make(map[string]bool)
	collectExtractionNames(steps, extractionNames)
	if len(extractionNames) == 0 {
		return false, nil
	}
	declaredInputs := make(map[string]bool)
	collectDeclaredStepInputs(steps, declaredInputs)
	changed := false
	var rewrite func(value *any)
	rewrite = func(value *any) {
		switch current := (*value).(type) {
		case string:
			normalized := workflowTemplateReference.ReplaceAllStringFunc(current, func(match string) string {
				expression := strings.TrimSpace(match[2 : len(match)-2])
				parts := strings.Split(expression, ".")
				if len(parts) < 2 {
					return match
				}
				root := strings.TrimSpace(parts[0])
				if root == "extracted" || root == "loopItem" || root == "loopIndex" ||
					declaredInputs[root] || !extractionNames[root] {
					return match
				}
				changed = true
				return "{{extracted." + expression + "}}"
			})
			if normalized != current {
				*value = normalized
			}
		case []any:
			for index := range current {
				rewrite(&current[index])
			}
		case map[string]any:
			for key, value := range current {
				rewrite(&value)
				current[key] = value
			}
		}
	}
	rewrite(&root)
	if !changed {
		return false, nil
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return false, fmt.Errorf("steps: marshal normalized extraction references: %w", err)
	}
	rule.Steps = models.JSON(encoded)
	return true, nil
}

func collectExtractionNames(steps []any, names map[string]bool) {
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
				names[name] = true
			}
		}
		for _, branch := range []string{"then", "else", "steps", "default"} {
			if children, ok := step[branch].([]any); ok {
				collectExtractionNames(children, names)
			}
		}
	}
}

// collectDeclaredStepInputs records variable bindings the rule declares so
// extraction canonicalization never retargets an intentional top-level input.
func collectDeclaredStepInputs(steps []any, names map[string]bool) {
	for _, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if variables, ok := step["variables"].(map[string]any); ok {
			for name := range variables {
				names[strings.TrimSpace(name)] = true
			}
		}
		for _, branch := range []string{"then", "else", "steps", "default"} {
			if children, ok := step[branch].([]any); ok {
				collectDeclaredStepInputs(children, names)
			}
		}
	}
}
