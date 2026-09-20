package dsl

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/models"
)

const (
	dslStrictOutputName        = "submit_pageagent_rule"
	maxStrictOutputSchemaBytes = 512 * 1024
)

type strictCandidateIDs struct {
	scopes      []strictCandidateScope
	catalogHash string
}

type strictCandidateScope struct {
	id             string
	repeated       bool
	fieldsByType   map[string][]string
	fieldSelectors map[string]string
	fieldOrder     map[string]int
	directTypes    map[string]bool
}

func workflowStrictOutput(
	requirement models.CollectionRequirementSpec,
	baseline *models.Rule,
	catalogJSON string,
) (*llm.StructuredOutput, error) {
	if baseline == nil {
		return nil, fmt.Errorf("%w: strict output baseline is required", ErrInvalidLLMRule)
	}
	if len(requirement.OutputFields) == 0 {
		return nil, fmt.Errorf("%w: strict output requires confirmed output fields", ErrInvalidLLMRule)
	}
	candidates, err := strictCandidateCatalog(catalogJSON)
	if err != nil {
		return nil, err
	}
	if candidates.catalogHash == "" || len(candidates.scopes) == 0 {
		return nil, fmt.Errorf("%w: strict output requires a non-empty selector catalog", ErrInvalidLLMRule)
	}

	buildSchema := func(includeSingleFieldExtracts bool) ([]byte, error) {
		definitions, err := strictWorkflowDefinitions(requirement, candidates, includeSingleFieldExtracts)
		if err != nil {
			return nil, err
		}
		domainSchema, err := strictBaselineDomainSchema(baseline.Domain)
		if err != nil {
			return nil, err
		}
		ruleSchema := strictObject(map[string]any{
			"id":      strictStringEnum(baseline.ID),
			"version": strictStringEnum(baseline.Version),
			"name":    map[string]any{"type": "string"},
			"domain":  domainSchema,
			"entry":   strictStringEnum(baseline.Entry),
			"steps": strictArray(map[string]any{
				"$ref": "#/$defs/action",
			}),
		})
		schema := strictObject(map[string]any{
			"selectorCatalogHash": strictStringEnum(candidates.catalogHash),
			"rule":                ruleSchema,
		})
		schema["$defs"] = definitions
		return json.Marshal(schema)
	}
	// Prefer single-field extract variants; drop them when the assembled
	// schema would not leave headroom under the size bound.
	encoded, err := buildSchema(len(requirement.OutputFields) > 1)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxStrictOutputSchemaBytes*3/4 {
		encoded, err = buildSchema(false)
		if err != nil {
			return nil, err
		}
	}
	if len(encoded) > maxStrictOutputSchemaBytes {
		return nil, fmt.Errorf(
			"%w: strict workflow schema is %d bytes and exceeds the %d-byte bound",
			ErrInvalidLLMRule,
			len(encoded),
			maxStrictOutputSchemaBytes,
		)
	}
	return &llm.StructuredOutput{
		Name:        dslStrictOutputName,
		Description: "Submit one complete, selector-catalog-bound PageAgent rule.",
		Schema:      encoded,
	}, nil
}

func strictCandidateCatalog(raw string) (strictCandidateIDs, error) {
	var catalog map[string]any
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		return strictCandidateIDs{}, fmt.Errorf("%w: decode strict selector catalog: %v", ErrInvalidLLMRule, err)
	}
	hash, _ := catalog["catalogHash"].(string)
	result := strictCandidateIDs{
		catalogHash: strings.TrimSpace(hash),
	}
	seenTargets := map[string]struct{}{}
	seenFields := map[string]struct{}{}
	candidates, _ := catalog["candidates"].([]any)
	for index, rawCandidate := range candidates {
		candidate, ok := rawCandidate.(map[string]any)
		if !ok {
			return strictCandidateIDs{}, fmt.Errorf(
				"%w: strict selector catalog candidate %d is not an object",
				ErrInvalidLLMRule,
				index,
			)
		}
		rowID := strictCatalogString(candidate["rowCandidateId"])
		targetID := strictCatalogString(candidate["targetCandidateId"])
		if (rowID == "") == (targetID == "") {
			return strictCandidateIDs{}, fmt.Errorf(
				"%w: strict selector catalog candidate %d must contain exactly one target ID",
				ErrInvalidLLMRule,
				index,
			)
		}
		scope := strictCandidateScope{
			id:             targetID,
			fieldsByType:   map[string][]string{},
			fieldSelectors: map[string]string{},
			fieldOrder:     map[string]int{},
			directTypes:    map[string]bool{},
		}
		if rowID != "" {
			scope.id = rowID
			scope.repeated = true
		}
		if _, duplicate := seenTargets[scope.id]; duplicate {
			return strictCandidateIDs{}, fmt.Errorf(
				"%w: strict selector catalog target ID %q is duplicated",
				ErrInvalidLLMRule,
				scope.id,
			)
		}
		seenTargets[scope.id] = struct{}{}
		fields, _ := candidate["fieldCandidates"].([]any)
		for fieldIndex, rawField := range fields {
			field, ok := rawField.(map[string]any)
			if !ok {
				return strictCandidateIDs{}, fmt.Errorf(
					"%w: strict selector catalog candidate %d field %d is not an object",
					ErrInvalidLLMRule,
					index,
					fieldIndex,
				)
			}
			id := strictCatalogString(field["fieldCandidateId"])
			if id == "" {
				return strictCandidateIDs{}, fmt.Errorf(
					"%w: strict selector catalog candidate %d field %d has no ID",
					ErrInvalidLLMRule,
					index,
					fieldIndex,
				)
			}
			if _, duplicate := seenFields[id]; duplicate {
				return strictCandidateIDs{}, fmt.Errorf(
					"%w: strict selector catalog field ID %q is duplicated",
					ErrInvalidLLMRule,
					id,
				)
			}
			seenFields[id] = struct{}{}
			// A top-level extract.fields descriptor may select only a direct
			// row/target descendant. Nested candidates remain available to the
			// semantic resolver for future recursive schema support, but must
			// not be exposed as direct field choices.
			if strictCatalogString(field["parentFieldCandidateId"]) != "" {
				continue
			}
			scope.fieldSelectors[id] = strictCatalogString(field["observedRelativeSelector"])
			scope.fieldOrder[id] = fieldIndex
			types, _ := field["supportedTypes"].([]any)
			for _, rawType := range types {
				fieldType, _ := rawType.(string)
				if fieldType != "" {
					scope.fieldsByType[fieldType] = append(scope.fieldsByType[fieldType], id)
					if !scope.repeated &&
						strictCatalogString(field["parentFieldCandidateId"]) == "" &&
						strictCatalogString(field["observedRelativeSelector"]) == "" {
						scope.directTypes[fieldType] = true
					}
				}
			}
		}
		for fieldType, ids := range scope.fieldsByType {
			scope.fieldsByType[fieldType] = uniqueSortedStrings(ids)
		}
		result.scopes = append(result.scopes, scope)
	}
	sort.Slice(result.scopes, func(i, j int) bool {
		if result.scopes[i].repeated != result.scopes[j].repeated {
			return result.scopes[i].repeated
		}
		return result.scopes[i].id < result.scopes[j].id
	})
	return result, nil
}

func strictWorkflowDefinitions(
	requirement models.CollectionRequirementSpec,
	candidates strictCandidateIDs,
	includeSingleFieldExtracts bool,
) (map[string]any, error) {
	outputNames := make([]string, 0, len(requirement.OutputFields))
	outputDescriptions := make(map[string]string, len(requirement.OutputFields))
	payloadProperties := make(map[string]any, len(requirement.OutputFields))
	extractTargetDefs := make(map[string]any, len(candidates.scopes))
	seen := map[string]struct{}{}
	for _, field := range requirement.OutputFields {
		name := strings.TrimSpace(field.Name)
		if name == "" || name != field.Name {
			return nil, fmt.Errorf("%w: strict output field name must be exact", ErrInvalidLLMRule)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("%w: strict output field name %q is duplicated", ErrInvalidLLMRule, name)
		}
		seen[name] = struct{}{}
		outputNames = append(outputNames, name)
		outputDescriptions[name] = strings.TrimSpace(field.Description)
		// PageAgent payload values are templates at JSON generation time. The
		// existing result-contract gate proves their eventual runtime types.
		payloadProperties[name] = map[string]any{"type": "string"}
	}

	ordinaryTarget := strictObject(map[string]any{
		"family": strictStringEnum(
			"ref",
			"selector",
			"selectorVisible",
			"selectorUnfiltered",
			"text",
			"textVisible",
			"ariaLabel",
			"role",
		),
		"value": map[string]any{"type": "string"},
		"name":  map[string]any{"type": "string"},
	})
	actionReference := map[string]any{"$ref": "#/$defs/action"}
	actionArray := strictArray(actionReference)
	condition := strictObject(map[string]any{
		"type":   strictStringEnum("elementExists", "elementNotExists"),
		"target": ordinaryTarget,
	})
	urlCondition := strictObject(map[string]any{
		"type":    strictStringEnum("urlContains", "urlMatches"),
		"pattern": map[string]any{"type": "string"},
	})
	sendPayload := strictObject(payloadProperties)

	actions := []any{
		strictObject(map[string]any{
			"action":    strictStringEnum("navigate"),
			"url":       map[string]any{"type": "string"},
			"waitUntil": strictStringEnum("load", "domcontentloaded"),
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("navigate"),
			"url":    map[string]any{"type": "string"},
		}),
	}
	actions = append(actions,
		strictObject(map[string]any{
			"action": strictStringEnum("click"),
			"target": ordinaryTarget,
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("type"),
			"target": ordinaryTarget,
			"value":  map[string]any{"type": "string"},
		}),
	)
	actions = append(actions,
		strictObject(map[string]any{
			"action": strictStringEnum("type"),
			"target": ordinaryTarget,
			"value":  map[string]any{"type": "string"},
			"append": map[string]any{"type": "boolean"},
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("type"),
			"target": ordinaryTarget,
			"value":  map[string]any{"type": "string"},
			"submit": map[string]any{"type": "boolean"},
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("type"),
			"target": ordinaryTarget,
			"value":  map[string]any{"type": "string"},
			"append": map[string]any{"type": "boolean"},
			"submit": map[string]any{"type": "boolean"},
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("select"),
			"target": ordinaryTarget,
			"value":  map[string]any{"type": "string"},
		}),
	)
	actions = append(actions,
		strictObject(map[string]any{
			"action": strictStringEnum("pressKey"),
			"keys":   strictArray(map[string]any{"type": "string"}),
		}),
		strictObject(map[string]any{
			"action":    strictStringEnum("scrollBy"),
			"direction": strictStringEnum("up", "down", "left", "right"),
			"distance":  map[string]any{"type": "number"},
		}),
		strictObject(map[string]any{
			"action":    strictStringEnum("scrollBy"),
			"direction": strictStringEnum("up", "down", "left", "right"),
			"distance":  map[string]any{"type": "number"},
			"unit":      strictStringEnum("pixels", "pages"),
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("waitForTimeout"),
			"ms":     map[string]any{"type": "number"},
		}),
	)
	actions = append(actions,
		strictObject(map[string]any{
			"action": strictStringEnum("waitForElementVisible"),
			"target": ordinaryTarget,
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("waitForElementHidden"),
			"target": ordinaryTarget,
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("waitForText"),
			"target": ordinaryTarget,
			"text":   map[string]any{"type": "string"},
		}),
	)
	actions = append(actions,
		strictObject(map[string]any{
			"action": strictStringEnum("filter"),
			"from":   map[string]any{"type": "string"},
			"name":   map[string]any{"type": "string"},
			"criteria": strictObject(map[string]any{
				"field": strictStringEnum(outputNames...),
				// The rule engine supports the full op set below; restricting
				// the schema to "eq" rejected contract-legal filters (e.g.
				// matching a destination host with "contains"/"matches").
				"op": strictStringEnum(
					"eq", "ne", "gt", "gte", "lt", "lte", "contains", "matches", "exists", "notEmpty",
				),
				"value": map[string]any{"type": "string"},
			}),
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("loop"),
			"type":   strictStringEnum("fixedCount"),
			"count": map[string]any{
				"type": "integer", "minimum": 1, "maximum": 100,
			},
			"steps": actionArray,
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("loop"),
			"type":   strictStringEnum("forEach"),
			"items":  map[string]any{"type": "string"},
			"as":     map[string]any{"type": "string"},
			"steps":  actionArray,
		}),
		strictObject(map[string]any{
			"action":    strictStringEnum("if"),
			"condition": condition,
			"then":      actionArray,
			"else":      actionArray,
		}),
		strictObject(map[string]any{
			"action": strictStringEnum("break"),
		}),
		strictObject(map[string]any{
			"action":    strictStringEnum("sendResult"),
			"payload":   sendPayload,
			"immediate": map[string]any{"type": "boolean"},
		}),
	)
	appendExtraction := func(properties map[string]any) {
		actions = append(actions, strictObject(properties))
		if len(requirement.RequiredInputs)+len(requirement.OptionalInputs) == 0 {
			return
		}
		conditioned := make(map[string]any, len(properties)+1)
		for key, value := range properties {
			conditioned[key] = value
		}
		conditioned["condition"] = urlCondition
		actions = append(actions, strictObject(conditioned))
	}
	singleTargetsByType := map[string][]any{}
	extractBranches := 0
	for _, scope := range candidates.scopes {
		targetKey := "targetCandidateId"
		if scope.repeated {
			targetKey = "rowCandidateId"
		} else {
			for directType := range scope.directTypes {
				singleTargetsByType[directType] = append(
					singleTargetsByType[directType],
					strictObject(map[string]any{
						"targetCandidateId": strictStringEnum(scope.id),
						"visible":           strictBooleanEnum(true),
					}),
				)
			}
		}
		// Every confirmed output field needs its own direct selector. Exposing
		// a scope with fewer distinct direct candidates makes it impossible to
		// satisfy that contract and encourages providers to mix opaque IDs from
		// different scopes. Keep those impossible branches out of the strict
		// schema rather than rejecting them only after the paid response.
		if strictDistinctFieldCandidateCount(scope.fieldsByType) < len(outputNames) {
			continue
		}
		fieldProperties := make(map[string]any, len(outputNames))
		validScope := true
		for _, name := range outputNames {
			fieldsByType := strictSemanticFieldsForOutput(
				scope.fieldsByType,
				scope.fieldSelectors,
				scope.fieldOrder,
				name,
				outputDescriptions[name],
			)
			fieldSchema, fieldErr := strictExtractFieldSchema(fieldsByType)
			if fieldErr != nil {
				validScope = false
				break
			}
			property := make(map[string]any, len(fieldSchema)+1)
			for key, value := range fieldSchema {
				property[key] = value
			}
			if description := outputDescriptions[name]; description != "" {
				property["description"] = description
			}
			fieldProperties[name] = property
		}
		if !validScope {
			continue
		}
		// Hoist this scope's target object into $defs once and reference it
		// from every extract branch (full, conditioned, and single-field
		// variants): large catalogs otherwise duplicate the candidate-ID
		// enums per branch and blow the schema size bound.
		targetDefName := fmt.Sprintf("extractTarget%d", len(extractTargetDefs))
		extractTargetDefs[targetDefName] = strictObject(map[string]any{
			targetKey: strictStringEnum(scope.id),
			"visible": strictBooleanEnum(true),
		})
		targetRef := map[string]any{"$ref": "#/$defs/" + targetDefName}
		appendExtraction(map[string]any{
			"action":   strictStringEnum("extract"),
			"name":     map[string]any{"type": "string"},
			"target":   targetRef,
			"multiple": strictBooleanEnum(scope.repeated),
			"onEmpty":  strictStringEnum("fail"),
			"fields":   strictObject(fieldProperties),
		})
		// Also allow one extraction per single output field: an output whose
		// value is only observable after navigation (e.g. a destination host)
		// is correctly produced by a separate pageInfo extraction, so the
		// row extraction legitimately names a subset. Single-field variants
		// keep the all-properties-required strict-tool invariant; the shared
		// $defs target keeps even large catalogs under the size bound.
		if len(outputNames) > 1 && includeSingleFieldExtracts {
			for name, property := range fieldProperties {
				appendExtraction(map[string]any{
					"action":   strictStringEnum("extract"),
					"name":     map[string]any{"type": "string"},
					"target":   targetRef,
					"multiple": strictBooleanEnum(scope.repeated),
					"onEmpty":  strictStringEnum("fail"),
					"fields":   strictObject(map[string]any{name: property}),
				})
			}
		}
		extractBranches++
	}
	if extractBranches == 0 {
		return nil, fmt.Errorf(
			"%w: strict output selector catalog has no target with supported direct fields",
			ErrInvalidLLMRule,
		)
	}
	if targets := singleTargetsByType["text"]; len(targets) > 0 {
		appendExtraction(map[string]any{
			"action": strictStringEnum("extractText"),
			"name":   map[string]any{"type": "string"},
			"target": strictAnyOf(targets...),
		})
	}
	if targets := singleTargetsByType["attr"]; len(targets) > 0 {
		appendExtraction(map[string]any{
			"action": strictStringEnum("extractAttribute"),
			"name":   map[string]any{"type": "string"},
			"target": strictAnyOf(targets...),
			"attr":   map[string]any{"type": "string"},
		})
	}
	if targets := singleTargetsByType["html"]; len(targets) > 0 {
		appendExtraction(map[string]any{
			"action": strictStringEnum("extractHtml"),
			"name":   map[string]any{"type": "string"},
			"target": strictAnyOf(targets...),
		})
	}
	if targets := singleTargetsByType["json"]; len(targets) > 0 {
		appendExtraction(map[string]any{
			"action": strictStringEnum("extractJson"),
			"name":   map[string]any{"type": "string"},
			"target": strictAnyOf(targets...),
		})
	}
	// Page-info extraction is the evidence-safe way to produce URL/host
	// outputs: URL-bearing attributes are redacted from sanitized evidence,
	// so a destination host can only be observed after navigating. One
	// output-name entry per confirmed field, each choosing a page facet.
	pageInfoFacet := strictAnyOf(
		strictObject(map[string]any{
			"type": strictStringEnum("url", "title", "domain", "timestamp", "referrer", "userAgent", "viewport"),
		}),
		strictObject(map[string]any{
			"type": strictStringEnum("url", "title", "domain", "timestamp", "referrer", "userAgent", "viewport"),
			"trim": map[string]any{"type": "boolean"},
		}),
	)
	// A pageInfo extraction names the one facet it produces (typically the
	// output whose value is only observable after navigation). Emit one
	// branch per confirmed output name so every branch still requires all of
	// its properties (strict-tool compatibility); multiple facets are
	// expressed as separate pageInfo extractions with their own names.
	for _, name := range outputNames {
		actions = append(actions, strictObject(map[string]any{
			"action": strictStringEnum("extractPageInfo"),
			"name":   map[string]any{"type": "string"},
			"fields": strictObject(map[string]any{name: pageInfoFacet}),
		}))
	}
	definitions := map[string]any{
		"action": strictAnyOf(actions...),
	}
	for name, target := range extractTargetDefs {
		definitions[name] = target
	}
	return definitions, nil
}

func strictSemanticFieldsForOutput(
	fieldsByType map[string][]string,
	selectors map[string]string,
	order map[string]int,
	name string,
	description string,
) map[string][]string {
	tag := strictOutputSemanticTag(name, description)
	if tag == "" {
		return fieldsByType
	}
	hasSemanticMetadata := false
	for _, selector := range selectors {
		if strings.TrimSpace(selector) != "" {
			hasSemanticMetadata = true
			break
		}
	}
	if !hasSemanticMetadata {
		return fieldsByType
	}
	filtered := make(map[string][]string, len(fieldsByType))
	for fieldType, ids := range fieldsByType {
		for _, id := range ids {
			if strictSelectorMatchesSemanticTag(selectors[id], tag) {
				filtered[fieldType] = append(filtered[fieldType], id)
			}
		}
	}
	if strings.Contains(strings.ToLower(name+" "+description), "first") {
		for fieldType, ids := range filtered {
			if len(ids) < 2 {
				continue
			}
			best := ids[0]
			for _, id := range ids[1:] {
				if order[id] < order[best] {
					best = id
				}
			}
			filtered[fieldType] = []string{best}
		}
	}
	return filtered
}

func strictSelectorMatchesSemanticTag(selector string, tag string) bool {
	return strictSelectorSubjectTag(selector) == strings.ToLower(strings.TrimSpace(tag))
}

func strictSelectorSubjectTag(selector string) string {
	normalized := strings.ToLower(strings.TrimSpace(selector))
	start := 0
	parentheses := 0
	brackets := 0
	quote := rune(0)
	escaped := false
	for index, current := range normalized {
		if escaped {
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if current == quote {
				quote = 0
			}
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			continue
		}
		switch current {
		case '(':
			parentheses++
		case ')':
			if parentheses > 0 {
				parentheses--
			}
		case '[':
			brackets++
		case ']':
			if brackets > 0 {
				brackets--
			}
		case '>', '+', '~':
			if parentheses == 0 && brackets == 0 {
				start = index + 1
			}
		case ' ', '\t', '\n', '\r':
			if parentheses == 0 && brackets == 0 {
				start = index + 1
			}
		}
	}
	compound := strings.TrimSpace(normalized[start:])
	end := 0
	for end < len(compound) {
		current := compound[end]
		if (current >= 'a' && current <= 'z') || (end > 0 && current >= '0' && current <= '9') || current == '-' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return ""
	}
	return compound[:end]
}

func strictOutputSemanticTag(name string, description string) string {
	normalized := strings.ToLower(strings.TrimSpace(name + " " + description))
	switch {
	case strings.Contains(normalized, "heading") &&
		containsAnySemanticPhrase(normalized, "level-two", "level two", "second-level", "h2"):
		return "h2"
	case strings.Contains(normalized, "heading") &&
		containsAnySemanticPhrase(normalized, "level-one", "level one", "first-level", "h1"):
		return "h1"
	case strings.Contains(normalized, "paragraph"):
		return "p"
	case strings.Contains(normalized, "image"):
		return "img"
	case strings.Contains(normalized, "list item"):
		return "li"
	case strings.Contains(normalized, "table row"):
		return "tr"
	case strings.Contains(normalized, "table cell"):
		return "td"
	case strings.Contains(normalized, "citation"):
		return "cite"
	case strings.Contains(normalized, "link"):
		return "a"
	case strings.Contains(normalized, "heading") &&
		containsAnySemanticPhrase(normalized, "title", "page heading", "guide heading", "main heading"):
		return "h1"
	default:
		return ""
	}
}

func containsAnySemanticPhrase(value string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func strictDistinctFieldCandidateCount(fieldsByType map[string][]string) int {
	seen := map[string]struct{}{}
	for _, ids := range fieldsByType {
		for _, id := range ids {
			if id != "" {
				seen[id] = struct{}{}
			}
		}
	}
	return len(seen)
}

func strictExtractFieldSchema(fieldsByType map[string][]string) (map[string]any, error) {
	variants := make([]any, 0, len(fieldsByType)+8)
	for _, fieldType := range []string{"text", "number", "boolean", "count", "exists", "html"} {
		ids := fieldsByType[fieldType]
		if len(ids) == 0 {
			continue
		}
		variants = append(variants, strictObject(map[string]any{
			"type":             strictStringEnum(fieldType),
			"fieldCandidateId": strictStringEnum(ids...),
		}))
		// The engine contract permits optional trim/regex on text and regex
		// on number; the strict schema must accept those legal shapes.
		if fieldType == "text" {
			variants = append(variants,
				strictObject(map[string]any{
					"type":             strictStringEnum("text"),
					"fieldCandidateId": strictStringEnum(ids...),
					"trim":             map[string]any{"type": "boolean"},
				}),
				strictObject(map[string]any{
					"type":             strictStringEnum("text"),
					"fieldCandidateId": strictStringEnum(ids...),
					"regex":            map[string]any{"type": "string"},
				}),
				strictObject(map[string]any{
					"type":             strictStringEnum("text"),
					"fieldCandidateId": strictStringEnum(ids...),
					"trim":             map[string]any{"type": "boolean"},
					"regex":            map[string]any{"type": "string"},
				}))
		}
		if fieldType == "number" {
			variants = append(variants, strictObject(map[string]any{
				"type":             strictStringEnum("number"),
				"fieldCandidateId": strictStringEnum(ids...),
				"regex":            map[string]any{"type": "string"},
			}))
		}
	}
	if ids := fieldsByType["attr"]; len(ids) > 0 {
		variants = append(variants, strictObject(map[string]any{
			"type":             strictStringEnum("attr"),
			"fieldCandidateId": strictStringEnum(ids...),
			"attr":             map[string]any{"type": "string"},
		}))
		variants = append(variants, strictObject(map[string]any{
			"type":             strictStringEnum("attr"),
			"fieldCandidateId": strictStringEnum(ids...),
			"attr":             map[string]any{"type": "string"},
			"resolve":          strictBooleanEnum(true),
		}))
		// attr + regex (with or without resolve) is contract-legal and is how
		// providers naturally extract e.g. a destination host from an href.
		variants = append(variants,
			strictObject(map[string]any{
				"type":             strictStringEnum("attr"),
				"fieldCandidateId": strictStringEnum(ids...),
				"attr":             map[string]any{"type": "string"},
				"regex":            map[string]any{"type": "string"},
			}),
			strictObject(map[string]any{
				"type":             strictStringEnum("attr"),
				"fieldCandidateId": strictStringEnum(ids...),
				"attr":             map[string]any{"type": "string"},
				"regex":            map[string]any{"type": "string"},
				"resolve":          strictBooleanEnum(true),
			}))
	}
	if ids := fieldsByType["json"]; len(ids) > 0 {
		variants = append(variants, strictObject(map[string]any{
			"type":             strictStringEnum("json"),
			"fieldCandidateId": strictStringEnum(ids...),
		}))
		variants = append(variants, strictObject(map[string]any{
			"type":             strictStringEnum("json"),
			"fieldCandidateId": strictStringEnum(ids...),
			"path":             map[string]any{"type": "string"},
		}))
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("%w: strict output selector catalog has no supported fields", ErrInvalidLLMRule)
	}
	return strictAnyOf(variants...), nil
}

func strictBaselineDomainSchema(raw models.JSON) (map[string]any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%w: decode strict baseline domain: %v", ErrInvalidLLMRule, err)
	}
	switch domain := value.(type) {
	case string:
		return strictStringEnum(domain), nil
	case []any:
		values := make([]string, 0, len(domain))
		for _, rawValue := range domain {
			value, ok := rawValue.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("%w: strict baseline domain must contain strings", ErrInvalidLLMRule)
			}
			values = append(values, value)
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("%w: strict baseline domain is empty", ErrInvalidLLMRule)
		}
		return strictArray(strictStringEnum(uniqueSortedStrings(values)...)), nil
	default:
		return nil, fmt.Errorf("%w: strict baseline domain has unsupported shape", ErrInvalidLLMRule)
	}
}

func strictObject(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	sort.Strings(required)
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func strictArray(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func strictAnyOf(values ...any) map[string]any {
	return map[string]any{"anyOf": values}
}

func strictStringEnum(values ...string) map[string]any {
	encoded := make([]any, 0, len(values))
	for _, value := range uniqueSortedStrings(values) {
		encoded = append(encoded, value)
	}
	return map[string]any{"type": "string", "enum": encoded}
}

func strictBooleanEnum(values ...bool) map[string]any {
	encoded := make([]any, 0, len(values))
	for _, value := range values {
		encoded = append(encoded, value)
	}
	return map[string]any{"type": "boolean", "enum": encoded}
}

func strictCatalogString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; value == "" || exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
