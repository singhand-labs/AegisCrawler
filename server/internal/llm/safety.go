package llm

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// SafetyFlag marks a step that may be risky.
type SafetyFlag struct {
	StepIndex string `json:"stepIndex"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
}

var riskyActionTypes = map[string]bool{
	"submit": true,
}

var riskyPattern = regexp.MustCompile(`(?i)(purchase|buy|pay|checkout|delete|remove|transfer|publish|confirm.*order|place.*order|account.*(delete|remove))`)

// riskyKeywordHit returns true if `s` OR any of its decoded normalized forms
// matches the risky keyword pattern. WI-15: HTML-entity and URL encodings
// are realistic bypass vectors; NFKD dropped as ineffective without a
// homoglyph table.
func riskyKeywordHit(s string) bool {
	if riskyPattern.MatchString(s) {
		return true
	}
	if decoded := html.UnescapeString(s); decoded != s {
		if riskyPattern.MatchString(decoded) {
			return true
		}
	}
	if unescaped, err := url.QueryUnescape(s); err == nil && unescaped != s {
		if riskyPattern.MatchString(unescaped) {
			return true
		}
	}
	return false
}

// ScanSafety scans rule steps for high-risk actions or targets, recursing into
// then/else/steps branches and recording a path-like step index (e.g. "0.then.1").
func ScanSafety(rule map[string]any) []SafetyFlag {
	stepsRaw, _ := rule["steps"].([]any)
	return scanSteps(stepsRaw, "")
}

func scanSteps(steps []any, prefix string) []SafetyFlag {
	var flags []SafetyFlag
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := stepPath(prefix, i)
		action, _ := step["action"].(string)
		if riskyActionTypes[action] {
			flags = append(flags, SafetyFlag{StepIndex: path, Action: action, Reason: "高风险动作类型"})
		}
		target, _ := step["target"].(map[string]any)
		if target != nil {
			for _, field := range []string{"selector", "text", "ariaLabel", "roleName"} {
				v, _ := target[field].(string)
				if riskyKeywordHit(v) {
					flags = append(flags, SafetyFlag{
						StepIndex: path,
						Action:    action,
						Reason:    fmt.Sprintf("目标元素 %s 包含高风险关键词", field),
					})
				}
			}
		}
		for _, field := range []string{"then", "else", "steps"} {
			nested, _ := step[field].([]any)
			if len(nested) > 0 {
				flags = append(flags, scanSteps(nested, path+"."+field+".")...)
			}
		}
	}
	return flags
}

func stepPath(prefix string, i int) string {
	if prefix == "" {
		return strconv.Itoa(i)
	}
	return prefix + strconv.Itoa(i)
}

var (
	scriptTagPattern      = regexp.MustCompile(`(?i)<script\b`)
	eventHandlerPattern   = regexp.MustCompile(`(?i)\b(on\w+)\s*=\s*["']?[^"'>\s]+`)
	externalURLPattern    = regexp.MustCompile(`(?i)https?://[^"'\s<>]+`)
	embeddedIframePattern = regexp.MustCompile(`(?i)<iframe\b`)
)

// ContentFilter inspects an LLM-generated enhancement suggestion for unsafe
// content such as script tags, event handler attributes, external URLs inside
// selectors/actions, and iframes. It returns safety flags describing any hits.
func ContentFilter(s *EnhancementSuggestion) []SafetyFlag {
	var flags []SafetyFlag

	for alias, sel := range s.Selectors {
		if reason := scanStringForUnsafe(sel.Selector); reason != "" {
			flags = append(flags, SafetyFlag{
				StepIndex: "selector:" + alias,
				Action:    "selector",
				Reason:    reason,
			})
		}
	}

	for i, op := range s.Steps {
		if reason := scanValueForUnsafe(op.Value); reason != "" {
			flags = append(flags, SafetyFlag{
				StepIndex: fmt.Sprintf("step:%d", i),
				Action:    op.Op,
				Reason:    reason,
			})
		}
	}

	for k, v := range s.Variables {
		if reason := scanStringForUnsafe(v); reason != "" {
			flags = append(flags, SafetyFlag{
				StepIndex: "variable:" + k,
				Action:    "variable",
				Reason:    reason,
			})
		}
	}

	for i, v := range s.Suggestions {
		if reason := scanStringForUnsafe(v); reason != "" {
			flags = append(flags, SafetyFlag{
				StepIndex: fmt.Sprintf("suggestion:%d", i),
				Action:    "suggestion",
				Reason:    reason,
			})
		}
	}

	return flags
}

func scanStringForUnsafe(s string) string {
	if s == "" {
		return ""
	}
	if scriptTagPattern.MatchString(s) {
		return "内容包含 <script> 标签"
	}
	if embeddedIframePattern.MatchString(s) {
		return "内容包含 <iframe> 标签"
	}
	if matches := eventHandlerPattern.FindStringSubmatch(s); len(matches) > 1 {
		return fmt.Sprintf("内容包含事件处理器属性 %s", matches[1])
	}
	// Flag external URLs in selector/action strings, but allow harmless schemas
	// such as relative paths and common page identifiers.
	if externalURLPattern.MatchString(s) {
		return "内容包含外部 URL"
	}
	return ""
}

func scanValueForUnsafe(v any) string {
	switch x := v.(type) {
	case string:
		return scanStringForUnsafe(x)
	case map[string]any:
		for _, val := range x {
			if reason := scanValueForUnsafe(val); reason != "" {
				return reason
			}
		}
	case []any:
		for _, item := range x {
			if reason := scanValueForUnsafe(item); reason != "" {
				return reason
			}
		}
	}
	return ""
}

// UnsafeContentError returns true if any content filter flag is critical enough
// that the patch should be rejected. Currently script tags and event handlers
// are treated as hard rejections; external URLs are only flagged for review.
func UnsafeContentError(flags []SafetyFlag) (bool, string) {
	for _, f := range flags {
		lower := strings.ToLower(f.Reason)
		if strings.Contains(lower, "script") || strings.Contains(lower, "事件处理器") {
			return true, f.Reason
		}
	}
	return false, ""
}

// ScanSealingFlags walks the rule structure and emits machine-readable
// safety-flag keys for concerns that are NOT generation-fatal but should
// require human review before the rule becomes an immutable version.
//
// Unlike ScanSafety (which causes ErrUnsafeGeneratedRule and never reaches
// a successful job completion), ScanSealingFlags runs AFTER generation
// passes and produces keys consumed by the blocking taxonomy in
// models/safety.go. The output is the union of:
//   - script-tag-in-selector
//   - iframe-embed
//   - event-handler-attribute
//   - external-resource-load
//
// Steps are walked recursively (then/else/steps). Selectors, text,
// ariaLabel, roleName, and url fields are inspected. Each key appears at
// most once even if multiple steps trigger it.
func ScanSealingFlags(rule map[string]any) []string {
	stepsRaw, _ := rule["steps"].([]any)
	entry, _ := rule["entry"].(string)
	seen := map[string]struct{}{}
	collect := func(key string) {
		seen[key] = struct{}{}
	}
	scanStepsForSealing(stepsRaw, "", entry, collect)
	out := make([]string, 0, len(seen))
	// Stable order: emit in taxonomy declaration order.
	for _, key := range []string{
		"external-resource-load",
		"script-tag-in-selector",
		"iframe-embed",
		"event-handler-attribute",
	} {
		if _, ok := seen[key]; ok {
			out = append(out, key)
		}
	}
	return out
}

func scanStepsForSealing(steps []any, prefix string, entry string, collect func(string)) {
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := stepPath(prefix, i)
		// Mirror ScanSafety's safe target-access pattern (line 42-54):
		// nil-safe type assertion, do NOT chain index into a nil map.
		target, _ := step["target"].(map[string]any)
		if target != nil {
			for _, field := range []string{"selector", "text", "ariaLabel", "roleName"} {
				if s, ok := target[field].(string); ok {
					classifyForSealing(s, collect)
				}
			}
		}
		if url, ok := step["url"].(string); ok && url != "" {
			// Absolute navigation outside the trusted entry origin is a sealing
			// concern. Same-origin URLs remain subject to the ordinary domain and
			// replay security gates, but do not require a safety override merely
			// because they use an absolute URL.
			if externalURLPattern.MatchString(url) && !sameNavigationOrigin(url, entry) {
				collect("external-resource-load")
			}
		}
		for _, field := range []string{"then", "else", "steps"} {
			if nested, ok := step[field].([]any); ok && len(nested) > 0 {
				scanStepsForSealing(nested, path+"."+field+".", entry, collect)
			}
		}
	}
}

func sameNavigationOrigin(candidateRaw string, entryRaw string) bool {
	candidate, candidateErr := url.Parse(candidateRaw)
	entry, entryErr := url.Parse(entryRaw)
	if candidateErr != nil || entryErr != nil || candidate.User != nil || entry.User != nil {
		return false
	}
	if candidate.Scheme == "" || candidate.Hostname() == "" || entry.Scheme == "" || entry.Hostname() == "" {
		return false
	}
	return strings.EqualFold(candidate.Scheme, entry.Scheme) &&
		strings.EqualFold(candidate.Hostname(), entry.Hostname()) &&
		candidate.Port() == entry.Port()
}

// classifyForSealing maps a single string to zero or more sealing-flag keys
// using the same regex patterns as ContentFilter but returning machine keys
// instead of Chinese reason text.
func classifyForSealing(s string, collect func(string)) {
	if s == "" {
		return
	}
	if scriptTagPattern.MatchString(s) {
		collect("script-tag-in-selector")
	}
	if embeddedIframePattern.MatchString(s) {
		collect("iframe-embed")
	}
	if eventHandlerPattern.MatchString(s) {
		collect("event-handler-attribute")
	}
	if externalURLPattern.MatchString(s) {
		collect("external-resource-load")
	}
}

// ContentFilterRule walks the rule structure and emits content-filter:*
// SafetyFlags for script tags, iframes, event handlers, and external URLs
// in selector, text, ariaLabel, roleName, and url fields.
//
// Distinct from ScanSealingFlags (which returns deduplicated machine keys
// for the blocking taxonomy): ContentFilterRule returns SafetyFlag structs
// carrying step-index context, suitable for appending to a job's
// safetyFlags list alongside the security-scan:* entries. The output is
// advisory — it does NOT hard-fail generation; ScanSafety remains the
// generation-time gate.
func ContentFilterRule(rule map[string]any) []SafetyFlag {
	stepsRaw, _ := rule["steps"].([]any)
	return scanStepsForContentFilter(stepsRaw, "")
}

func scanStepsForContentFilter(steps []any, prefix string) []SafetyFlag {
	var flags []SafetyFlag
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := stepPath(prefix, i)
		target, _ := step["target"].(map[string]any)
		if target != nil {
			for _, field := range []string{"selector", "text", "ariaLabel", "roleName"} {
				if s, ok := target[field].(string); ok {
					flags = append(flags, classifyForContentFilter(s, path, field)...)
				}
			}
		}
		if urlStr, ok := step["url"].(string); ok && urlStr != "" {
			flags = append(flags, classifyForContentFilter(urlStr, path, "url")...)
		}
		for _, field := range []string{"then", "else", "steps"} {
			if nested, ok := step[field].([]any); ok && len(nested) > 0 {
				flags = append(flags, scanStepsForContentFilter(nested, path+"."+field+".")...)
			}
		}
	}
	return flags
}

// classifyForContentFilter applies the four existing regex patterns to a
// single string and returns one SafetyFlag per hit, tagged with the field
// name and step path so reviewers can locate the offending content.
func classifyForContentFilter(s, path, field string) []SafetyFlag {
	if s == "" {
		return nil
	}
	var flags []SafetyFlag
	if scriptTagPattern.MatchString(s) {
		flags = append(flags, SafetyFlag{StepIndex: path, Action: "content-filter:script-tag", Reason: field})
	}
	if embeddedIframePattern.MatchString(s) {
		flags = append(flags, SafetyFlag{StepIndex: path, Action: "content-filter:iframe", Reason: field})
	}
	if eventHandlerPattern.MatchString(s) {
		flags = append(flags, SafetyFlag{StepIndex: path, Action: "content-filter:event-handler", Reason: field})
	}
	if externalURLPattern.MatchString(s) {
		flags = append(flags, SafetyFlag{StepIndex: path, Action: "content-filter:external-url", Reason: field})
	}
	return flags
}
