package rule

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/singhand-labs/AegisCrawler/internal/models"
)

var (
	ErrInvalidTaskInput  = errors.New("task input does not conform to rule version schema")
	ErrInvalidTaskResult = errors.New("task result does not conform to rule version schema")
)

var allowedInputConstraints = map[string]bool{
	"enum": true, "minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
}

// BuildRequirementInputSchema preserves the required/optional distinction and
// portable constraints confirmed before DSL generation.
func BuildRequirementInputSchema(requirement models.CollectionRequirementSpec) (models.JSON, error) {
	properties := map[string]any{}
	required := make([]string, 0, len(requirement.RequiredInputs))
	add := func(input models.RequirementInput, isRequired bool) error {
		name := strings.TrimSpace(input.Name)
		if name == "" || !validRequirementType(input.Type) {
			return fmt.Errorf("%w: invalid input %q", ErrInvalidTaskInput, input.Name)
		}
		if _, exists := properties[name]; exists {
			return fmt.Errorf("%w: duplicate input %q", ErrInvalidTaskInput, name)
		}
		property := map[string]any{"type": string(input.Type), "description": input.Description}
		if input.Default != nil {
			property["default"] = input.Default
		}
		if input.Secret {
			property["x-secret"] = true
		}
		for key, value := range input.Constraints {
			if !allowedInputConstraints[key] {
				return fmt.Errorf("%w: unsupported constraint %q for %q", ErrInvalidTaskInput, key, name)
			}
			property[key] = value
		}
		if err := validateSchemaValue(property, input.Default, "default for "+name, input.Default == nil); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTaskInput, err)
		}
		properties[name] = property
		if isRequired {
			required = append(required, name)
		}
		return nil
	}
	for _, input := range requirement.RequiredInputs {
		if err := add(input, true); err != nil {
			return nil, err
		}
	}
	for _, input := range requirement.OptionalInputs {
		if err := add(input, false); err != nil {
			return nil, err
		}
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

// InferInputSchema treats existing rule variables as optional defaults. It is
// used only for backwards-compatible rule versions that predate requirements.
func InferInputSchema(defaults models.JSON) models.JSON {
	values := map[string]any{}
	_ = json.Unmarshal(defaults, &values)
	properties := make(map[string]any, len(values))
	for name, value := range values {
		properties[name] = map[string]any{"type": jsonType(value), "default": value}
	}
	encoded, _ := json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object",
		"properties": properties, "required": []string{}, "additionalProperties": false,
	})
	return models.JSON(encoded)
}

// ApplyAndValidateTaskInputs applies declared defaults and returns an immutable
// normalized input snapshot. Undeclared and secret values are rejected.
func ApplyAndValidateTaskInputs(schema models.JSON, supplied models.JSON) (models.JSON, error) {
	root, err := decodeSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTaskInput, err)
	}
	values := map[string]any{}
	if len(supplied) > 0 {
		if err := json.Unmarshal(supplied, &values); err != nil {
			return nil, fmt.Errorf("%w: inputs must be an object", ErrInvalidTaskInput)
		}
	}
	properties, _ := root["properties"].(map[string]any)
	required := stringSet(root["required"])
	if additional, ok := root["additionalProperties"].(bool); ok && !additional {
		for name := range values {
			if _, exists := properties[name]; !exists {
				return nil, fmt.Errorf("%w: undeclared input %q", ErrInvalidTaskInput, name)
			}
		}
	}
	for name, rawProperty := range properties {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: invalid schema for %q", ErrInvalidTaskInput, name)
		}
		value, exists := values[name]
		if !exists {
			if defaultValue, hasDefault := property["default"]; hasDefault {
				values[name] = defaultValue
				value, exists = defaultValue, true
			}
		}
		if !exists {
			if required[name] {
				return nil, fmt.Errorf("%w: missing required input %q", ErrInvalidTaskInput, name)
			}
			continue
		}
		if secret, _ := property["x-secret"].(bool); secret {
			return nil, fmt.Errorf("%w: secret input %q must use an approved secret reference", ErrInvalidTaskInput, name)
		}
		if err := validateSchemaValue(property, value, name, false); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidTaskInput, err)
		}
	}
	encoded, err := json.Marshal(values)
	return models.JSON(encoded), err
}

// ValidateTaskResult validates one row or an ordered non-empty array of rows.
func ValidateTaskResult(schema models.JSON, payload any) error {
	root, err := decodeSchema(schema)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTaskResult, err)
	}
	rows := []any{payload}
	if list, ok := payload.([]any); ok {
		if len(list) == 0 {
			return fmt.Errorf("%w: result batch is empty", ErrInvalidTaskResult)
		}
		rows = list
	}
	for index, row := range rows {
		if err := validateSchemaValue(root, row, fmt.Sprintf("row %d", index), false); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTaskResult, err)
		}
	}
	return nil
}

func decodeSchema(schema models.JSON) (map[string]any, error) {
	root := map[string]any{}
	if len(schema) == 0 || json.Unmarshal(schema, &root) != nil || len(root) == 0 {
		return nil, errors.New("schema is missing or invalid")
	}
	if root["type"] != "object" {
		return nil, errors.New("root schema must be an object")
	}
	return root, nil
}

func validateSchemaValue(schema map[string]any, value any, path string, absent bool) error {
	if absent {
		return nil
	}
	expected, _ := schema["type"].(string)
	if !matchesJSONType(value, expected) {
		return fmt.Errorf("%s must be %s", path, expected)
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed value", path)
		}
	}
	if numeric, ok := number(value); ok {
		if min, ok := number(schema["minimum"]); ok && numeric < min {
			return fmt.Errorf("%s is below its minimum", path)
		}
		if max, ok := number(schema["maximum"]); ok && numeric > max {
			return fmt.Errorf("%s exceeds its maximum", path)
		}
	}
	switch typed := value.(type) {
	case string:
		length := float64(utf8.RuneCountInString(typed))
		if min, ok := number(schema["minLength"]); ok && length < min {
			return fmt.Errorf("%s is shorter than %.0f characters", path, min)
		}
		if max, ok := number(schema["maxLength"]); ok && length > max {
			return fmt.Errorf("%s is longer than %.0f characters", path, max)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			re, err := regexp.Compile(pattern)
			if err != nil || !re.MatchString(typed) {
				return fmt.Errorf("%s does not match its pattern", path)
			}
		}
	case []any:
		length := float64(len(typed))
		if min, ok := number(schema["minItems"]); ok && length < min {
			return fmt.Errorf("%s has too few items", path)
		}
		if max, ok := number(schema["maxItems"]); ok && length > max {
			return fmt.Errorf("%s has too many items", path)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for index, item := range typed {
				if err := validateSchemaValue(itemSchema, item, fmt.Sprintf("%s[%d]", path, index), false); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		required := stringSet(schema["required"])
		for name := range required {
			if _, exists := typed[name]; !exists {
				return fmt.Errorf("%s is missing %q", path, name)
			}
		}
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for name := range typed {
				if _, exists := properties[name]; !exists {
					return fmt.Errorf("%s has undeclared field %q", path, name)
				}
			}
		}
		for name, child := range properties {
			if childSchema, ok := child.(map[string]any); ok {
				if childValue, exists := typed[name]; exists {
					if err := validateSchemaValue(childSchema, childValue, path+"."+name, false); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func stringSet(raw any) map[string]bool {
	result := map[string]bool{}
	if list, ok := raw.([]any); ok {
		for _, item := range list {
			if value, ok := item.(string); ok {
				result[value] = true
			}
		}
	}
	if list, ok := raw.([]string); ok {
		for _, value := range list {
			result[value] = true
		}
	}
	return result
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func matchesJSONType(value any, expected string) bool {
	switch expected {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := number(value)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

func jsonType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "string"
	}
}
