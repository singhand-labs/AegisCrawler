package rule

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// actionSchemaJSON mirrors the generated TypeScript DSL schema. The schema
// generator updates this embedded copy so the Go provisional gate shares the
// browser DSL's action-specific required-field and type contract.
//
//go:embed action.schema.json.gz
var actionSchemaGZIP []byte

var (
	actionSchemaOnce       sync.Once
	actionSchemaValidator  *jsonschema.Resolved
	actionSchemaNames      map[string]struct{}
	actionSchemaResolveErr error
)

func validateActionSchema(action string, step map[string]any) error {
	validator, names, err := resolvedActionSchema()
	if err != nil {
		return fmt.Errorf("initialize embedded DSL action schema: %w", err)
	}
	if _, ok := names[action]; !ok {
		return fmt.Errorf("action %q has no DSL schema definition", action)
	}
	if err := validator.Validate(map[string]any{action: step}); err != nil {
		message := err.Error()
		if runes := []rune(message); len(runes) > 2000 {
			message = string(runes[:2000]) + "..."
		}
		return fmt.Errorf("action %q does not match the DSL schema: %s", action, message)
	}
	return nil
}

func validateActionSchemaTree(steps []any, path string) error {
	for index, raw := range steps {
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		step, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", stepPath)
		}
		action, ok := step["action"].(string)
		if !ok || action == "" {
			return fmt.Errorf("%s.action is required", stepPath)
		}

		// Validate descendants first so a malformed leaf produces its precise
		// action error instead of an opaque parent anyOf failure.
		for _, branch := range []string{"then", "else", "steps", "default"} {
			if rawBranch, exists := step[branch]; exists {
				children, ok := rawBranch.([]any)
				if !ok {
					return fmt.Errorf("%s.%s must be an array", stepPath, branch)
				}
				if err := validateActionSchemaTree(children, stepPath+"."+branch); err != nil {
					return err
				}
			}
		}
		if cases, exists := step["cases"]; exists {
			caseList, ok := cases.([]any)
			if !ok {
				return fmt.Errorf("%s.cases must be an array", stepPath)
			}
			for caseIndex, rawCase := range caseList {
				caseValue, ok := rawCase.(map[string]any)
				if !ok {
					return fmt.Errorf("%s.cases[%d] must be an object", stepPath, caseIndex)
				}
				caseSteps, ok := caseValue["steps"].([]any)
				if !ok {
					return fmt.Errorf("%s.cases[%d].steps must be an array", stepPath, caseIndex)
				}
				if err := validateActionSchemaTree(caseSteps, fmt.Sprintf("%s.cases[%d].steps", stepPath, caseIndex)); err != nil {
					return err
				}
			}
		}
		if err := validateActionSchema(action, step); err != nil {
			return fmt.Errorf("%s: %w", stepPath, err)
		}
	}
	return nil
}

func resolvedActionSchema() (*jsonschema.Resolved, map[string]struct{}, error) {
	actionSchemaOnce.Do(func() {
		raw, err := embeddedActionSchemaJSON()
		if err != nil {
			actionSchemaResolveErr = err
			return
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			actionSchemaResolveErr = err
			return
		}
		actionUnion := schema.Definitions["Action"]
		if actionUnion == nil {
			actionSchemaResolveErr = fmt.Errorf("definitions.Action is missing")
			return
		}
		properties := map[string]*jsonschema.Schema{}
		names := map[string]struct{}{}
		for _, reference := range actionUnion.AnyOf {
			const prefix = "#/definitions/"
			if reference == nil || !strings.HasPrefix(reference.Ref, prefix) {
				continue
			}
			definition := schema.Definitions[strings.TrimPrefix(reference.Ref, prefix)]
			if definition == nil {
				continue
			}
			actionProperty := definition.Properties["action"]
			if actionProperty == nil {
				continue
			}
			register := func(value any) {
				name, ok := value.(string)
				if !ok || name == "" {
					return
				}
				properties[name] = &jsonschema.Schema{Ref: reference.Ref}
				names[name] = struct{}{}
			}
			if actionProperty.Const != nil {
				register(*actionProperty.Const)
			}
			for _, value := range actionProperty.Enum {
				register(value)
			}
		}
		if len(properties) == 0 {
			actionSchemaResolveErr = fmt.Errorf("no action definitions were registered")
			return
		}
		validationSchema := &jsonschema.Schema{
			Schema: schema.Schema, Definitions: schema.Definitions,
			Type: "object", Properties: properties,
		}
		actionSchemaValidator, actionSchemaResolveErr = validationSchema.Resolve(nil)
		if actionSchemaResolveErr == nil {
			actionSchemaNames = names
		}
	})
	return actionSchemaValidator, actionSchemaNames, actionSchemaResolveErr
}

func embeddedActionSchemaJSON() ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(actionSchemaGZIP))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
