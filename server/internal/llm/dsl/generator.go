// Package dsl generates an enhanced PageAgent rule from a baseline rule and a
// user-provided intent. It applies local intent templates and, for custom
// descriptions, optionally consults an LLM to map the description to a template.
package dsl

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/llm"
	"github.com/singhand-labs/AegisCrawler/internal/llm/intent"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Generator creates rules from recordings, baseline rules, and intent hints.
type Generator struct {
	cfg          *config.Config
	orchestrator *llm.Orchestrator
	logger       *zap.Logger
}

// NewGenerator creates a DSL generator.
func NewGenerator(cfg *config.Config, orchestrator *llm.Orchestrator, logger *zap.Logger) *Generator {
	return &Generator{
		cfg:          cfg,
		orchestrator: orchestrator,
		logger:       logger,
	}
}

// Generate returns an enhanced rule based on the supplied intent or custom
// description. When no intent matches, the baseline rule is returned unchanged.
func (g *Generator) Generate(ctx context.Context, recording map[string]any, baseline *models.Rule, intentCand *intent.Candidate, custom string) (*models.Rule, error) {
	if baseline == nil {
		return nil, errors.New("baseline rule is required")
	}

	// Start from a deep copy of the baseline rule as a map so templates can
	// mutate fields without side effects.
	ruleMap, err := ruleToMap(baseline)
	if err != nil {
		return nil, fmt.Errorf("convert baseline rule: %w", err)
	}

	intentType, err := g.resolveIntentType(ctx, intentCand, custom)
	if err != nil {
		return nil, err
	}
	switch intentType {
	case "list-collection":
		applyListCollectionTemplate(ruleMap)
	case "search-pagination":
		applySearchPaginationTemplate(ruleMap)
	case "form-submit":
		applyFormSubmitTemplate(ruleMap)
	}

	return mapToRule(ruleMap)
}

// resolveIntentType picks the intent template to apply. Custom descriptions are
// first classified by LLM (when enabled); if that fails or is unavailable, a
// local keyword classifier is used as fallback.
func (g *Generator) resolveIntentType(ctx context.Context, intentCand *intent.Candidate, custom string) (string, error) {
	if custom != "" {
		if g.cfg != nil && g.cfg.LLMEnabled && g.orchestrator != nil {
			t, err := g.classifyWithLLM(ctx, custom)
			if err == nil && t != "" {
				return t, nil
			}
			if err != nil && !g.cfg.AllowDegradedFallback() {
				return "", err
			}
		}
		return classifyKeyword(custom), nil
	}
	if intentCand != nil {
		return classifyKeyword(intentCand.Label + " " + intentCand.Description), nil
	}
	return "custom", nil
}

// classifyWithLLM asks the LLM to map a custom description to an intent type.
// redact.Recording not required: only the short user intent string is sent; no recording serialized.
func (g *Generator) classifyWithLLM(ctx context.Context, custom string) (string, error) {
	sys := "You are a helpful assistant that classifies web scraping intents."
	user := fmt.Sprintf(`Given the user description: %q, classify the intent into one of: "list-collection", "search-pagination", "form-submit", or "custom".

Return ONLY a JSON object with the field "intentType". Example: {"intentType": "list-collection"}`, custom)

	req := llm.CompletionRequest{
		Model:       g.cfg.LLMModel,
		System:      sys,
		User:        user,
		Temperature: 0.0,
		JSONMode:    true,
	}

	resp, err := g.orchestrator.Complete(ctx, req)
	if err != nil {
		g.logger.Warn("dsl intent classification llm failed", zap.Error(err))
		return "", err
	}

	var parsed struct {
		IntentType string `json:"intentType"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &parsed); err != nil {
		contentHash := sha256.Sum256([]byte(resp.Content))
		g.logger.Warn("dsl intent classification parse failed",
			zap.Error(err),
			zap.String("contentHash", fmt.Sprintf("%x", contentHash)),
			zap.Int("contentBytes", len(resp.Content)),
		)
		return "", err
	}
	return normalizeIntentType(parsed.IntentType), nil
}

// classifyKeyword maps free-form text to a known intent type using keywords.
func classifyKeyword(text string) string {
	lower := strings.ToLower(text)
	if containsAny(lower, []string{"搜索", "翻页", "keyword"}) {
		return "search-pagination"
	}
	if containsAny(lower, []string{"表单", "提交", "submit"}) {
		return "form-submit"
	}
	if containsAny(lower, []string{"列表", "商品", "采集"}) {
		return "list-collection"
	}
	return "custom"
}

func normalizeIntentType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "list-collection", "list", "list_collection", "collection":
		return "list-collection"
	case "search-pagination", "search", "search_pagination", "pagination":
		return "search-pagination"
	case "form-submit", "form", "form_submit", "submit":
		return "form-submit"
	default:
		return "custom"
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// applyListCollectionTemplate adds an extract step after the last click and
// exposes a maxItems variable.
func applyListCollectionTemplate(rule map[string]any) {
	steps, ok := rule["steps"].([]any)
	if !ok {
		return
	}

	lastClick := -1
	for i := len(steps) - 1; i >= 0; i-- {
		step, ok := steps[i].(map[string]any)
		if !ok {
			continue
		}
		if action, _ := step["action"].(string); action == "click" {
			lastClick = i
			break
		}
	}

	var clickTarget any = map[string]any{"selector": "body"}
	if lastClick >= 0 {
		if step, ok := steps[lastClick].(map[string]any); ok {
			if target, ok := step["target"].(map[string]any); ok {
				clickTarget = target
			}
		}
	}

	extractStep := map[string]any{
		"action":   "extract",
		"name":     "itemData",
		"target":   clickTarget,
		"multiple": true,
		"fields": map[string]any{
			"title": map[string]any{"selector": "h1, h2, .title", "type": "text", "trim": true},
			"price": map[string]any{"selector": ".price", "type": "text", "trim": true},
			"url":   map[string]any{"selector": "a", "type": "attr", "attr": "href", "resolve": true},
		},
	}

	if lastClick >= 0 {
		steps = append(steps[:lastClick+1], append([]any{extractStep}, steps[lastClick+1:]...)...)
	} else {
		steps = append(steps, extractStep)
	}
	rule["steps"] = steps

	variables, _ := rule["variables"].(map[string]any)
	if variables == nil {
		variables = map[string]any{}
	}
	variables["maxItems"] = 10
	rule["variables"] = variables
}

// applySearchPaginationTemplate exposes keyword/pages variables, turns the
// first concrete input value into a template placeholder, and appends a
// results extraction step after the last submit-like action.
func applySearchPaginationTemplate(rule map[string]any) {
	variables, _ := rule["variables"].(map[string]any)
	if variables == nil {
		variables = map[string]any{}
	}
	variables["pages"] = 1
	rule["variables"] = variables

	steps, ok := rule["steps"].([]any)
	if !ok {
		variables["keyword"] = ""
		return
	}
	// Preserve a keyword already templatized by the baseline converter; only
	// capture a new default from the recorded type value when one is absent.
	recordedKeyword := ""
	if existing, ok := variables["keyword"].(string); ok && existing != "" {
		recordedKeyword = existing
	}
	for _, s := range steps {
		step, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if action, _ := step["action"].(string); action == "type" {
			if val, ok := step["value"].(string); ok && val != "" && val != "{{keyword}}" {
				if recordedKeyword == "" {
					recordedKeyword = val
				}
			}
			step["value"] = "{{keyword}}"
			break
		}
	}
	variables["keyword"] = recordedKeyword

	lastSubmitIdx := -1
	for i, s := range steps {
		step, ok := s.(map[string]any)
		if !ok {
			continue
		}
		action, _ := step["action"].(string)
		if action == "click" {
			lastSubmitIdx = i
			continue
		}
		if action == "pressKey" {
			lastSubmitIdx = i
			continue
		}
		if action == "type" {
			if submit, _ := step["submit"].(bool); submit {
				lastSubmitIdx = i
			}
		}
	}

	extractStep := map[string]any{
		"action":   "extract",
		"name":     "searchResults",
		"target":   map[string]any{"selector": ".result, .c-container"},
		"multiple": true,
		"fields": map[string]any{
			"title":   map[string]any{"selector": "h3 a, .t", "type": "text", "trim": true},
			"url":     map[string]any{"selector": "h3 a, .t", "type": "attr", "attr": "href", "resolve": true},
			"summary": map[string]any{"selector": ".c-abstract, .content-right_8Zs40", "type": "text", "trim": true},
		},
	}

	insertAfterIdx := lastSubmitIdx
	if lastSubmitIdx >= 0 {
		for i := lastSubmitIdx + 1; i < len(steps); i++ {
			step, ok := steps[i].(map[string]any)
			if !ok {
				break
			}
			if action, _ := step["action"].(string); action == "navigate" {
				insertAfterIdx = i
			} else {
				break
			}
		}
	}

	if insertAfterIdx >= 0 {
		steps = append(steps[:insertAfterIdx+1], append([]any{
			map[string]any{"action": "waitForTimeout", "ms": 1500},
			extractStep,
		}, steps[insertAfterIdx+1:]...)...)
	} else {
		steps = append(steps, extractStep)
	}
	rule["steps"] = steps
}

// applyFormSubmitTemplate exposes a formData variable.
func applyFormSubmitTemplate(rule map[string]any) {
	variables, _ := rule["variables"].(map[string]any)
	if variables == nil {
		variables = map[string]any{}
	}
	variables["formData"] = map[string]any{}
	rule["variables"] = variables
}

// ruleToMap serializes a models.Rule into a generic map.
func ruleToMap(r *models.Rule) (map[string]any, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// mapToRule deserializes a generic map into a models.Rule.
func mapToRule(m map[string]any) (*models.Rule, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var r models.Rule
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// RuleToYAML serializes a models.Rule to YAML.
func RuleToYAML(r *models.Rule) (string, error) {
	// models.JSON implements JSON marshaling but is byte-backed, so sending the
	// struct directly to yaml.v3 renders fields such as steps and domain as
	// character-code arrays. Normalize through JSON first to preserve the DSL's
	// actual object, array, and scalar shapes in the user-facing preview.
	m, err := ruleToMap(r)
	if err != nil {
		return "", err
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
